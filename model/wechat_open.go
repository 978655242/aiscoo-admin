package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	ErrWeChatOpenRegistrationDisabled = errors.New("New user registration is currently unavailable.")
	ErrWeChatOpenUserUnavailable      = errors.New("This account is unavailable.")
	ErrWeChatOpenIdentityInvalid      = errors.New("Invalid WeChat website identity.")
)

func ValidateWeChatOpenIdentity(identity string) error {
	appID, openID, ok := strings.Cut(identity, ":")
	if !ok || appID == "" || openID == "" || len(identity) > 191 {
		return ErrWeChatOpenIdentityInvalid
	}
	for _, part := range []string{appID, openID} {
		for _, ch := range part {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return ErrWeChatOpenIdentityInvalid
			}
		}
	}
	return nil
}

func FindOrCreateWeChatOpenUser(providerUserID, displayName string, inviterID int, allowRegistration bool) (*User, bool, error) {
	if err := ValidateWeChatOpenIdentity(providerUserID); err != nil {
		return nil, false, err
	}
	var existing User
	err := DB.Unscoped().Where("wechat_open_id = ?", providerUserID).First(&existing).Error
	if err == nil {
		if existing.DeletedAt.Valid || existing.Status != common.UserStatusEnabled {
			return nil, false, ErrWeChatOpenUserUnavailable
		}
		return &existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if !allowRegistration {
		return nil, false, ErrWeChatOpenRegistrationDisabled
	}
	nickname := []rune(displayName)
	if len(nickname) > 20 {
		displayName = string(nickname[:20])
	}
	if displayName == "" {
		displayName = "WeChat user"
	}
	user := &User{
		Username: "wechat_" + common.GetRandomString(12), DisplayName: displayName,
		Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
		InviterId: inviterID, AuthVersion: 1, WeChatOpenId: &providerUserID,
	}
	// The unique identity is inserted with the user; a losing insert rolls back all defaults.
	err = DB.Transaction(func(tx *gorm.DB) error { return user.InsertWithTx(tx, inviterID) })
	if err == nil {
		return user, true, nil
	}
	if !isWeChatOpenUniqueConflict(err) {
		return nil, false, err
	}
	var winner User
	lookupErr := DB.Unscoped().Where("wechat_open_id = ?", providerUserID).First(&winner).Error
	if lookupErr != nil {
		return nil, false, err
	}
	if winner.DeletedAt.Valid || winner.Status != common.UserStatusEnabled {
		return nil, false, ErrWeChatOpenUserUnavailable
	}
	return &winner, false, nil
}

// EnsureWeChatOpenIdentitySchema is shared by startup and the database migration matrix.
func EnsureWeChatOpenIdentitySchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(&User{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&User{}, "WeChatOpenId") {
		if err := db.Migrator().AddColumn(&User{}, "WeChatOpenId"); err != nil {
			return err
		}
	}
	// Empty values are unbound; SQL NULL permits multiple accounts without this login method.
	if err := db.Model(&User{}).Where("wechat_open_id = ?", "").Update("wechat_open_id", nil).Error; err != nil {
		return err
	}
	switch db.Dialector.Name() {
	case "mysql":
		var collation string
		if err := db.Raw("SELECT COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND COLUMN_NAME = 'wechat_open_id'").Scan(&collation).Error; err != nil {
			return err
		}
		if collation != "utf8mb4_bin" {
			if err := db.Exec("ALTER TABLE users MODIFY COLUMN wechat_open_id VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL").Error; err != nil {
				return err
			}
		}
	case "postgres":
		var collation string
		if err := db.Raw("SELECT COALESCE(collation_name, '') FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'wechat_open_id'").Scan(&collation).Error; err != nil {
			return err
		}
		if collation != "C" {
			if err := db.Exec("ALTER TABLE users ALTER COLUMN wechat_open_id TYPE VARCHAR(191) COLLATE \"C\"").Error; err != nil {
				return err
			}
		}
	}
	// 使用字段名让 GORM 解析缩写后的真实索引名，避免重复启动时再次创建同一索引。
	if !db.Migrator().HasIndex(&User{}, "WeChatOpenId") {
		return db.Migrator().CreateIndex(&User{}, "WeChatOpenId")
	}
	return nil
}

func isWeChatOpenUniqueConflict(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code() == 2067 || sqliteErr.Code() == 1555)
}

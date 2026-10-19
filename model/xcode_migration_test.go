package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Release fixture provenance (latest release observed 2026-10-08):
// https://api.github.com/repos/QuantumNous/new-api/releases/latest
// https://api.github.com/repos/QuantumNous/new-api/git/ref/tags/v1.0.0-rc.42
// v1.0.0-rc.42 = 6370b29424168039e94d40d610191e7d2e65dbf4.
// Persisted fields/types/GORM tags below are copied from that commit's
// model/token.go, model/user_session.go, model/subscription.go and
// model/model_meta.go; only JSON tags, methods and gorm:"-" fields are stripped.
// https://github.com/QuantumNous/new-api/tree/6370b29424168039e94d40d610191e7d2e65dbf4/model
// TableName restores release table/index names after renaming the fixture types.
// This is a representative five-table release schema, not a full release startup.
// Release and current go.mod use the same GORM/dialect versions.
type xcodeReleaseToken struct {
	Id                 int
	UserId             int    `gorm:"index"`
	Key                string `gorm:"type:varchar(128);uniqueIndex"`
	Status             int    `gorm:"default:1"`
	Name               string `gorm:"index"`
	CreatedTime        int64  `gorm:"bigint"`
	AccessedTime       int64  `gorm:"bigint"`
	ExpiredTime        int64  `gorm:"bigint;default:-1"`
	RemainQuota        int    `gorm:"default:0"`
	UnlimitedQuota     bool
	ModelLimitsEnabled bool
	ModelLimits        string  `gorm:"type:text"`
	AllowIps           *string `gorm:"default:''"`
	UsedQuota          int     `gorm:"default:0"`
	Group              string  `gorm:"default:''"`
	CrossGroupRetry    bool
	AutoGroups         string         `gorm:"type:text"`
	DeletedAt          gorm.DeletedAt `gorm:"index"`
}

func (xcodeReleaseToken) TableName() string { return "tokens" }

type xcodeReleaseUserSession struct {
	SID                 string `gorm:"column:sid;type:varchar(64);primaryKey"`
	UserID              int    `gorm:"column:user_id;not null;index:idx_user_sessions_user_status_expiry,priority:1;index:idx_user_sessions_user_created,priority:1"`
	Version             int64  `gorm:"type:bigint;not null;default:1"`
	UserAuthVersion     int64  `gorm:"type:bigint;not null"`
	Status              string `gorm:"type:varchar(16);not null;index:idx_user_sessions_user_status_expiry,priority:2;index:idx_user_sessions_status_revoked,priority:1"`
	RefreshHash         string `gorm:"type:char(64);not null"`
	PreviousRefreshHash string `gorm:"type:varchar(64)"`
	PreviousValidUntil  int64  `gorm:"type:bigint;not null;default:0"`
	LoginMethod         string `gorm:"type:varchar(32);not null"`
	IP                  string `gorm:"type:varchar(64)"`
	UserAgent           string `gorm:"type:text"`
	CreatedAt           int64  `gorm:"autoCreateTime;column:created_at;index:idx_user_sessions_user_created,priority:2"`
	LastActiveAt        int64  `gorm:"type:bigint;not null;column:last_active_at"`
	ExpiresAt           int64  `gorm:"type:bigint;not null;column:expires_at;index:idx_user_sessions_user_status_expiry,priority:3;index:idx_user_sessions_expires_at"`
	RevokedAt           int64  `gorm:"type:bigint;not null;default:0;column:revoked_at;index:idx_user_sessions_status_revoked,priority:2"`
	RevokedReason       string `gorm:"type:varchar(64);column:revoked_reason"`
}

func (xcodeReleaseUserSession) TableName() string { return "user_sessions" }

type xcodeReleaseUserSubscription struct {
	Id                  int
	UserId              int    `gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId              int    `gorm:"index"`
	AmountTotal         int64  `gorm:"type:bigint;not null;default:0"`
	AmountUsed          int64  `gorm:"type:bigint;not null;default:0"`
	StartTime           int64  `gorm:"bigint"`
	EndTime             int64  `gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status              string `gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"`
	Source              string `gorm:"type:varchar(32);default:'order'"`
	LastResetTime       int64  `gorm:"type:bigint;default:0"`
	NextResetTime       int64  `gorm:"type:bigint;default:0;index"`
	UpgradeGroup        string `gorm:"type:varchar(64);default:''"`
	PrevUserGroup       string `gorm:"type:varchar(64);default:''"`
	DowngradeGroup      string `gorm:"type:varchar(64);default:''"`
	AllowWalletOverflow bool
	CreatedAt           int64 `gorm:"bigint"`
	UpdatedAt           int64 `gorm:"bigint"`
}

func (xcodeReleaseUserSubscription) TableName() string { return "user_subscriptions" }

type xcodeReleasePreConsume struct {
	Id                 int
	RequestId          string `gorm:"type:varchar(64);uniqueIndex"`
	UserId             int    `gorm:"index"`
	UserSubscriptionId int    `gorm:"index"`
	PreConsumed        int64  `gorm:"type:bigint;not null;default:0"`
	Status             string `gorm:"type:varchar(32);index"`
	CreatedAt          int64  `gorm:"bigint"`
	UpdatedAt          int64  `gorm:"bigint;index"`
}

func (xcodeReleasePreConsume) TableName() string { return "subscription_pre_consume_records" }

type xcodeReleaseModel struct {
	Id           int
	ModelName    string         `gorm:"size:128;not null;uniqueIndex:uk_model_name_delete_at,priority:1"`
	Description  string         `gorm:"type:text"`
	Icon         string         `gorm:"type:varchar(128)"`
	Tags         string         `gorm:"type:varchar(255)"`
	VendorID     int            `gorm:"index"`
	Endpoints    string         `gorm:"type:text"`
	Status       int            `gorm:"default:1"`
	SyncOfficial int            `gorm:"default:1"`
	CreatedTime  int64          `gorm:"bigint"`
	UpdatedTime  int64          `gorm:"bigint"`
	DeletedAt    gorm.DeletedAt `gorm:"index;uniqueIndex:uk_model_name_delete_at,priority:2"`
	NameRule     int            `gorm:"default:0"`
}

func (xcodeReleaseModel) TableName() string { return "models" }

// The pre-T7 column was already present as TEXT; preserve even invalid legacy data.
type xcodePreT7ModelConfig struct {
	XCodeConfig string `gorm:"type:text"`
}

func (xcodePreT7ModelConfig) TableName() string { return "models" }

// External databases must be empty and named xcode_upgrade. This deliberately
// does not use TEST_MYSQL_DSN/TEST_POSTGRES_DSN, which other tests may be using.
func TestXCodePurposePersistenceDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "local"
			if dialect == "mysql" {
				dsn = os.Getenv("TEST_XCODE_UPGRADE_MYSQL_DSN")
			} else if dialect == "postgres" {
				dsn = os.Getenv("TEST_XCODE_UPGRADE_POSTGRES_DSN")
			}
			if dsn == "" {
				t.Skip("dedicated upgrade database DSN is not configured")
			}
			for _, origin := range []string{"fresh", "release_v1.0.0-rc.42", "pre_t7_text"} {
				t.Run(origin, func(t *testing.T) {
					testXCodeStartupMigration(t, dialect, dsn, origin)
				})
			}
		})
	}
}

func testXCodeStartupMigration(t *testing.T, dialect, dsn, origin string) {
	t.Helper()
	upgrade := origin != "fresh"
	legacyConfig := `{"slug":"preserved-local"}`
	previousDB, previousLogDB := DB, LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousSQLitePath, previousMaster := common.SQLitePath, common.IsMasterNode
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.SQLitePath, common.IsMasterNode = previousSQLitePath, previousMaster
		initCol()
	})
	common.IsMasterNode = true
	common.SQLitePath = filepath.Join(t.TempDir(), "xcode-upgrade.db")
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("LOG_SQL_DSN", "")
	db, _, err := chooseDB("SQL_DSN", false)
	require.NoError(t, err)
	connections := []*gorm.DB{db}
	t.Cleanup(func() {
		for _, connection := range connections {
			sqlDB, err := connection.DB()
			require.NoError(t, err)
			assert.NoError(t, sqlDB.Close())
		}
	})
	require.Equal(t, dialect, db.Dialector.Name())
	if dialect != "sqlite" {
		var name string
		query := "SELECT DATABASE()"
		if dialect == "postgres" {
			query = "SELECT current_database()"
		}
		require.NoError(t, db.Raw(query).Scan(&name).Error)
		require.Equal(t, "xcode_upgrade", name, "refusing to modify a non-fixture database")
	}
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "refusing to modify a non-empty fixture database")
	// Only tables created after the empty-database guard belong to this test.
	t.Cleanup(func() {
		tables, err := db.Migrator().GetTables()
		require.NoError(t, err)
		for _, table := range tables {
			if table != "sqlite_sequence" {
				assert.NoError(t, db.Migrator().DropTable(table))
			}
		}
	})

	ips := "192.0.2.0/24"
	nativeToken := &xcodeReleaseToken{
		UserId: 83, Key: "preserved-native-key", Status: 1, Name: "native-key",
		CreatedTime: 1700000000, AccessedTime: 1700000010, ExpiredTime: -1,
		RemainQuota: 9876, UnlimitedQuota: true, ModelLimitsEnabled: true,
		ModelLimits: "native-model", AllowIps: &ips, UsedQuota: 37, Group: "auto",
		CrossGroupRetry: true, AutoGroups: `["default","premium"]`,
	}
	nativeSession := &xcodeReleaseUserSession{
		SID: "native-sid", UserID: 83, Version: 4, UserAuthVersion: 7, Status: "active",
		RefreshHash: strings.Repeat("a", 64), PreviousRefreshHash: strings.Repeat("b", 64),
		PreviousValidUntil: 1700000030, LoginMethod: "password", IP: "192.0.2.1",
		UserAgent: "native-browser", CreatedAt: 1700000000, LastActiveAt: 1700000010, ExpiresAt: 4102444800,
	}
	nativeSubscription := &xcodeReleaseUserSubscription{
		UserId: 83, PlanId: 9, AmountTotal: 1000, AmountUsed: 37,
		StartTime: 1700000000, EndTime: 4102444800, Status: "active", Source: "admin",
		LastResetTime: 1700000000, NextResetTime: 4102444700, UpgradeGroup: "premium",
		PrevUserGroup: "default", DowngradeGroup: "default", AllowWalletOverflow: true,
		CreatedAt: 1700000000, UpdatedAt: 1700000010,
	}
	nativePreConsume := &xcodeReleasePreConsume{
		RequestId: "native-request", UserId: 83, PreConsumed: 37, Status: "consumed",
		CreatedAt: 1700000000, UpdatedAt: 1700000010,
	}
	nativeModel := &xcodeReleaseModel{
		ModelName: "native-model", Description: "existing metadata", Icon: "native-icon",
		Tags: "native-tag", VendorID: 9, Endpoints: `["/v1/chat/completions"]`, Status: 2,
		SyncOfficial: 2, CreatedTime: 1700000000, UpdatedTime: 1700000010, NameRule: 1,
		DeletedAt: gorm.DeletedAt{Time: time.Unix(1700000020, 0).UTC(), Valid: true},
	}
	rows := []struct {
		original any
		saved    any
		where    string
	}{
		{nativeToken, &xcodeReleaseToken{}, "name = 'native-key'"},
		{nativeSession, &xcodeReleaseUserSession{}, "sid = 'native-sid'"},
		{nativeSubscription, &xcodeReleaseUserSubscription{}, "user_id = 83"},
		{nativePreConsume, &xcodeReleasePreConsume{}, "request_id = 'native-request'"},
		{nativeModel, &xcodeReleaseModel{}, "model_name = 'native-model'"},
	}
	if upgrade {
		for _, row := range rows {
			require.NoError(t, db.AutoMigrate(row.original))
		}
		assert.False(t, db.Migrator().HasTable(&XCodeAuthorization{}))
		for _, column := range []struct {
			entity any
			name   string
		}{
			{&Token{}, "ClientPurpose"}, {&Token{}, "ClientSessionSID"},
			{&UserSession{}, "ClientPurpose"}, {&UserSubscription{}, "UsageScope"},
			{&UserSubscription{}, "QuotaResetPeriodSnapshot"}, {&UserSubscription{}, "QuotaResetCustomSecondsSnapshot"},
			{&UserSubscription{}, "QuotaResetGeneration"}, {&SubscriptionPreConsumeRecord{}, "FundingScope"},
			{&SubscriptionPreConsumeRecord{}, "ResetGeneration"}, {&SubscriptionPreConsumeRecord{}, "CycleReservedQuota"},
			{&SubscriptionPreConsumeRecord{}, "FinalQuota"}, {&Model{}, "XCodeConfig"},
		} {
			require.False(t, db.Migrator().HasColumn(column.entity, column.name), "release fixture already contains %s", column.name)
		}
		for _, row := range rows {
			if row.original == nativePreConsume {
				nativePreConsume.UserSubscriptionId = nativeSubscription.Id
			}
			require.NoError(t, db.Create(row.original).Error)
		}
	}
	if origin == "pre_t7_text" {
		require.NoError(t, db.Migrator().AddColumn(&xcodePreT7ModelConfig{}, "XCodeConfig"))
		require.NoError(t, db.Unscoped().Model(&xcodeReleaseModel{}).Where("id = ?", nativeModel.Id).Update("x_code_config", legacyConfig).Error)
	}

	managedToken := Token{
		UserId: 84, Key: "managed-key", ClientPurpose: "xcode", ClientSessionSID: "xcode-sid",
		Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: 4102444800,
	}
	managedSession := UserSession{
		SID: "xcode-sid", UserID: 84, Version: 1, UserAuthVersion: 1, Status: "active",
		ClientPurpose: "xcode", RefreshHash: strings.Repeat("c", 64), LoginMethod: "password",
		LastActiveAt: 1700000000, ExpiresAt: 4102444800,
	}
	managedSubscription := UserSubscription{
		UserId: 84, PlanId: 9, AmountTotal: 1000, AmountUsed: 43, Status: "active", Source: "admin",
		UsageScope: "xcode", QuotaResetPeriodSnapshot: "custom",
		QuotaResetCustomSecondsSnapshot: 18000,
	}
	managedPreConsume := SubscriptionPreConsumeRecord{
		RequestId: "managed-request", UserId: 84, PreConsumed: 45, Status: "settled",
		FundingScope: "xcode", ResetGeneration: 1, CycleReservedQuota: 43, FinalQuota: 43,
	}
	completeConfig := `{"schema_version":1,"api_type":"openai-chat-completions","model_config":{"enabled":false,"properties":{"requiresMfjsToolSchema":false,"contextWindow":8192,"inputFormat":{"supportsText":false,"supportsImage":false,"supportsVideo":false,"supportsAudio":false,"supportsPdf":false},"outputFormat":{"supportsText":false},"supportsToolCall":false,"supportsJsonSchemaOutput":false,"supportsNativeWebSearch":false,"supportsMidConversationSystem":false},"optionSpecs":{"reasoningLevel":{"values":["off"],"map":"{}"},"maxOutputTokens":{"max":128,"map":"{}"}}}}`
	fullConfig := completeConfig + strings.Repeat(" ", 65536-len(completeConfig))
	_, err = ParseXCodeModelMetadata(fullConfig)
	require.NoError(t, err)
	managedModel := Model{ModelName: "managed-model", XCodeConfig: LongText(fullConfig)}
	authorization := XCodeAuthorization{UserId: 84, Revision: 1}
	for pass := range 2 {
		err := InitDB()
		db = DB
		connections = append(connections, db)
		require.NoError(t, err, "production InitDB pass %d", pass+1)
		if pass == 0 && !upgrade {
			for _, row := range rows {
				if row.original == nativePreConsume {
					nativePreConsume.UserSubscriptionId = nativeSubscription.Id
				}
				require.NoError(t, db.Create(row.original).Error)
			}
		}
		for _, row := range rows {
			require.NoError(t, db.Unscoped().Where(row.where).First(row.saved).Error)
			if savedModel, ok := row.saved.(*xcodeReleaseModel); ok {
				savedModel.DeletedAt.Time = savedModel.DeletedAt.Time.UTC()
			}
			assert.Equal(t, row.original, row.saved, "every release field must survive startup")
		}
		var token Token
		require.NoError(t, db.First(&token, nativeToken.Id).Error)
		assert.Empty(t, token.ClientPurpose)
		assert.Empty(t, token.ClientSessionSID)
		var session UserSession
		require.NoError(t, db.First(&session, "sid = ?", nativeSession.SID).Error)
		assert.Empty(t, session.ClientPurpose)
		var subscription UserSubscription
		require.NoError(t, db.First(&subscription, nativeSubscription.Id).Error)
		assert.Empty(t, subscription.UsageScope)
		assert.Empty(t, subscription.QuotaResetPeriodSnapshot)
		assert.Zero(t, subscription.QuotaResetCustomSecondsSnapshot)
		assert.Zero(t, subscription.QuotaResetGeneration)
		var preConsume SubscriptionPreConsumeRecord
		require.NoError(t, db.First(&preConsume, nativePreConsume.Id).Error)
		assert.Empty(t, preConsume.FundingScope)
		assert.Zero(t, preConsume.ResetGeneration)
		assert.Zero(t, preConsume.CycleReservedQuota)
		assert.Zero(t, preConsume.FinalQuota)
		var metadata Model
		require.NoError(t, db.Unscoped().First(&metadata, nativeModel.Id).Error)
		if origin == "pre_t7_text" {
			assert.Equal(t, legacyConfig, string(metadata.XCodeConfig), "migration must not scrub invalid local configuration")
			_, err := ParseXCodeModelMetadata(string(metadata.XCodeConfig))
			assert.Error(t, err, "legacy malformed configuration remains non-executable")
		} else {
			assert.Empty(t, metadata.XCodeConfig)
		}
		var count int64
		require.NoError(t, db.Model(&XCodeAuthorization{}).Where("user_id = ?", 83).Count(&count).Error)
		assert.Zero(t, count, "native subscription must not become an XCode authorization")
		if pass == 0 {
			require.NoError(t, db.Model(&XCodeAuthorization{}).Count(&count).Error)
			assert.Zero(t, count, "migration must not grant any XCode authorization")
			require.NoError(t, db.Create(&managedSession).Error)
			require.NoError(t, db.Create(&managedToken).Error)
			require.NoError(t, db.Create(&managedSubscription).Error)
			assert.EqualValues(t, 1, managedSubscription.QuotaResetGeneration, "new dedicated instances start in generation one")
			managedPreConsume.UserSubscriptionId = managedSubscription.Id
			require.NoError(t, db.Create(&managedPreConsume).Error)
			require.NoError(t, db.Create(&managedModel).Error)
			authorization.SubscriptionId = managedSubscription.Id
			require.NoError(t, db.Create(&authorization).Error)
		}
		for _, row := range []struct {
			expected any
			saved    any
			id       any
		}{
			{&managedToken, &Token{}, managedToken.Id},
			{&managedSession, &UserSession{}, "sid = 'xcode-sid'"},
			{&managedSubscription, &UserSubscription{}, managedSubscription.Id},
			{&managedPreConsume, &SubscriptionPreConsumeRecord{}, managedPreConsume.Id},
			{&managedModel, &Model{}, managedModel.Id},
			{&authorization, &XCodeAuthorization{}, authorization.Id},
		} {
			require.NoError(t, db.First(row.saved, row.id).Error)
			assert.Equal(t, row.expected, row.saved, "new XCode fields must survive production restart")
		}
		for _, index := range []struct {
			entity any
			name   string
		}{
			{&Token{}, "idx_tokens_key"}, {&Token{}, "idx_tokens_client_session_sid"},
			{&UserSession{}, "idx_user_sessions_user_status_expiry"}, {&UserSession{}, "idx_user_sessions_user_created"},
			{&UserSubscription{}, "idx_user_sub_active"}, {&UserSubscription{}, "idx_user_subscriptions_usage_scope"},
			{&SubscriptionPreConsumeRecord{}, "idx_subscription_pre_consume_records_request_id"},
			{&SubscriptionPreConsumeRecord{}, "idx_subscription_pre_consume_records_funding_scope"},
			{&Model{}, "uk_model_name_delete_at"},
			{&XCodeAuthorization{}, "idx_x_code_authorizations_user_id"},
			{&XCodeAuthorization{}, "idx_x_code_authorizations_subscription_id"},
		} {
			assert.True(t, db.Migrator().HasIndex(index.entity, index.name), "%s must survive startup", index.name)
		}
		duplicateToken := *nativeToken
		duplicateToken.Id = 0
		require.Error(t, db.Create(&duplicateToken).Error, "native token key remains unique")
		duplicatePreConsume := *nativePreConsume
		duplicatePreConsume.Id = 0
		require.Error(t, db.Create(&duplicatePreConsume).Error, "native request remains idempotent")
		duplicateSession := *nativeSession
		require.Error(t, db.Create(&duplicateSession).Error, "native session SID remains unique")
		duplicateModel := *nativeModel
		duplicateModel.Id = 0
		require.Error(t, db.Create(&duplicateModel).Error, "model name/deleted_at uniqueness remains intact")
		require.Error(t, db.Create(&XCodeAuthorization{
			UserId: 84, SubscriptionId: nativeSubscription.Id, Revision: 1,
		}).Error, "only one designated XCode relationship per user")
	}
	var version string
	query := "SELECT VERSION()"
	if dialect == "sqlite" {
		query = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("database=%s version=%s origin=%s release=v1.0.0-rc.42 sha=6370b29424168039e94d40d610191e7d2e65dbf4 InitDB_starts=2 xcode_config_bytes=%d", dialect, version, origin, len(fullConfig))
}

package model

import (
	"crypto/rand"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MigrationIdentityFields struct {
	ID        int    `gorm:"primaryKey"`
	Name      string `gorm:"size:64;unique"`
	Reference string `gorm:"size:64;uniqueIndex"`
	Provider  string `gorm:"size:32;uniqueIndex:,composite:provider_subject"`
	Subject   string `gorm:"size:64;uniqueIndex:,composite:provider_subject"`
}

type migrationIdentityV1 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(32)"`
}

type migrationIdentityV2 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(64)"`
	Note   string `gorm:"size:128"`
}

type migrationConstraintV1 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64"`
}

type migrationConstraintV2 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64;unique"`
}

type migrationDecimalV1 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(10,6);default:0"`
}

type migrationDecimalV2 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:0"`
}

type migrationDecimalV3 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:1.25"`
}

func TestMigrationSchemaStability(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dsn string
			switch dialect {
			case "sqlite":
				dsn = "local"
				previousPath := common.SQLitePath
				common.SQLitePath = filepath.Join(t.TempDir(), "migration.db")
				t.Cleanup(func() { common.SQLitePath = previousPath })
			case "mysql":
				dsn = os.Getenv("TEST_MYSQL_DSN")
			case "postgres":
				dsn = os.Getenv("TEST_POSTGRES_DSN")
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			t.Setenv("MIGRATION_TEST_DSN", dsn)
			db, _, err := chooseDB("MIGRATION_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			recorder := &migrationSQLRecorder{}
			db = db.Session(&gorm.Session{Logger: recorder})

			t.Run("identity_and_indexes", func(t *testing.T) {
				const table = "migration_identity_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				row := migrationIdentityV1{
					MigrationIdentityFields: MigrationIdentityFields{ID: 1, Name: "root", Reference: "token-reference", Provider: "oidc", Subject: "subject"},
					Digest:                  "old-digest",
				}
				require.NoError(t, db.Table(table).Create(&row).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				assert.Empty(t, recorder.schemaMutations())

				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				columns, err := db.Table(table).Migrator().ColumnTypes(&migrationIdentityV2{})
				require.NoError(t, err)
				for _, column := range columns {
					if column.Name() == "digest" {
						length, ok := column.Length()
						require.True(t, ok)
						assert.EqualValues(t, 64, length)
					}
				}
				assert.True(t, db.Table(table).Migrator().HasColumn(&migrationIdentityV2{}, "note"))
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				assert.Empty(t, recorder.schemaMutations())
				var saved migrationIdentityV2
				require.NoError(t, db.Table(table).First(&saved, 1).Error)
				assert.Equal(t, row.MigrationIdentityFields, saved.MigrationIdentityFields)
				expectedDigest := row.Digest
				if dialect == "postgres" {
					expectedDigest += strings.Repeat(" ", 64-len(row.Digest))
				}
				assert.Equal(t, expectedDigest, saved.Digest)
				for _, duplicate := range []migrationIdentityV2{
					{MigrationIdentityFields: MigrationIdentityFields{Name: "root", Reference: "other-1", Provider: "other", Subject: "1"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-2", Reference: "token-reference", Provider: "other", Subject: "2"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-3", Reference: "other-3", Provider: "oidc", Subject: "subject"}},
				} {
					assert.Error(t, db.Table(table).Create(&duplicate).Error)
				}
			})

			t.Run("unique_constraint_changes", func(t *testing.T) {
				const table = "migration_constraint_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Error(t, db.Table(table).Create(&migrationConstraintV2{Name: "existing"}).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Empty(t, recorder.schemaMutations())
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
			})

			if dialect == "postgres" {
				t.Run("renamed_unique_constraints", func(t *testing.T) {
					const table = "migration_renamed_unique"
					t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
					tableDB := db.Table(table).Session(&gorm.Session{})
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					original := migrationConstraintV1{Name: "existing"}
					require.NoError(t, tableDB.Create(&original).Error)
					for _, name := range []string{"models_model_name_key", `imported "model" name`} {
						require.NoError(t, db.Exec("ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (name)", clause.Table{Name: table}, clause.Column{Name: name}).Error)
					}
					require.NoError(t, db.Exec("ALTER TABLE ? ADD CONSTRAINT keep_composite UNIQUE (id, name)", clause.Table{Name: table}).Error)
					require.NoError(t, db.Exec("CREATE INDEX keep_name_lookup ON ? (name)", clause.Table{Name: table}).Error)
					// A unique field must keep the old names and still reject duplicates.
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV2{}))
					require.Error(t, tableDB.Create(&migrationConstraintV2{Name: "existing"}).Error)
					require.NoError(t, db.Exec("CREATE TABLE migration_unique_reference (name varchar(64) REFERENCES migration_renamed_unique(name))").Error)
					t.Cleanup(func() { _ = db.Migrator().DropTable("migration_unique_reference") })
					require.NoError(t, db.Exec("INSERT INTO migration_unique_reference (name) VALUES (?)", "existing").Error)
					require.Error(t, tableDB.AutoMigrate(&migrationConstraintV1{}), "dependent foreign keys must not be cascaded away")
					for _, name := range []string{"models_model_name_key", `imported "model" name`} {
						assert.True(t, tableDB.Migrator().HasConstraint(&migrationConstraintV1{}, name), "all old constraints must survive rollback")
					}
					var references int64
					require.NoError(t, db.Table("migration_unique_reference").Count(&references).Error)
					assert.EqualValues(t, 1, references)
					require.NoError(t, db.Migrator().DropTable("migration_unique_reference"))
					// Removing column uniqueness must resolve both actual constraint names.
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					recorder.reset()
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					assert.Empty(t, recorder.schemaMutations())
					require.NoError(t, tableDB.Create(&migrationConstraintV1{Name: "existing"}).Error)
					assert.True(t, tableDB.Migrator().HasConstraint(&migrationConstraintV1{}, "keep_composite"))
					assert.True(t, tableDB.Migrator().HasIndex(&migrationConstraintV1{}, "keep_name_lookup"))
					var rows []migrationConstraintV1
					require.NoError(t, tableDB.Order("id").Find(&rows).Error)
					require.Len(t, rows, 2)
					assert.Equal(t, original, rows[0])
				})
			}

			if dialect == "mysql" {
				t.Run("decimal_default_and_real_changes", func(t *testing.T) {
					const table = "migration_decimal_test"
					t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
					require.NoError(t, db.Table(table).AutoMigrate(&migrationDecimalV1{}))
					require.NoError(t, db.Table(table).Create(&migrationDecimalV1{ID: 1, Price: 12.345678}).Error)
					for _, target := range []any{&migrationDecimalV1{}, &migrationDecimalV2{}, &migrationDecimalV3{}} {
						require.NoError(t, db.Table(table).AutoMigrate(target))
						recorder.reset()
						require.NoError(t, db.Table(table).AutoMigrate(target))
						assert.Empty(t, recorder.schemaMutations())
					}
					columns, err := db.Table(table).Migrator().ColumnTypes(&migrationDecimalV3{})
					require.NoError(t, err)
					for _, column := range columns {
						if column.Name() == "price" {
							precision, scale, ok := column.DecimalSize()
							require.True(t, ok)
							assert.EqualValues(t, 12, precision)
							assert.EqualValues(t, 6, scale)
							nullable, ok := column.Nullable()
							require.True(t, ok)
							assert.False(t, nullable)
						}
					}
					require.NoError(t, db.Table(table).Create(&map[string]any{"id": 2}).Error)
					var prices []float64
					require.NoError(t, db.Table(table).Order("id").Pluck("price", &prices).Error)
					assert.Equal(t, []float64{12.345678, 1.25}, prices)
				})
			}
		})
	}
}

func TestWeChatOpenStartupMigrationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "local"
			if dialect == "mysql" {
				dsn = os.Getenv("TEST_MYSQL_DSN")
			} else if dialect == "postgres" {
				dsn = os.Getenv("TEST_POSTGRES_DSN")
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			for _, origin := range []string{"fresh", "release_d2abf9c"} {
				t.Run(origin, func(t *testing.T) {
					testWeChatOpenStartupMigration(t, dialect, dsn, origin)
				})
			}
		})
	}
}

func testWeChatOpenStartupMigration(t *testing.T, dialect, dsn, origin string) {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousSQLitePath, previousRetireAt := common.SQLitePath, legacyAccessTokenRetireAt.Load()
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.SQLitePath = previousSQLitePath
		legacyAccessTokenRetireAt.Store(previousRetireAt)
		initCol()
	})
	common.SQLitePath = filepath.Join(t.TempDir(), "wechat-startup.db")
	t.Setenv("WECHAT_MIGRATION_TEST_DSN", dsn)
	db, dbType, err := chooseDB("WECHAT_MIGRATION_TEST_DSN", false)
	require.NoError(t, err)
	require.Equal(t, dialect, db.Dialector.Name())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	if dialect != "sqlite" {
		// The supplied database is used only to create/drop a namespace owned by this subtest.
		admin := db
		namespace := "wechat_open_" + strings.ToLower(rand.Text())
		if dialect == "mysql" {
			config, err := mysqldriver.ParseDSN(dsn)
			require.NoError(t, err)
			require.NoError(t, admin.Exec("CREATE DATABASE ? CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci", clause.Table{Name: namespace}).Error)
			t.Cleanup(func() {
				assert.NoError(t, admin.Exec("DROP DATABASE ?", clause.Table{Name: namespace}).Error)
			})
			config.DBName = namespace
			dsn = config.FormatDSN()
		} else {
			config, err := url.Parse(dsn)
			require.NoError(t, err)
			require.NoError(t, admin.Exec("CREATE SCHEMA ?", clause.Table{Name: namespace}).Error)
			t.Cleanup(func() {
				assert.NoError(t, admin.Exec("DROP SCHEMA ? CASCADE", clause.Table{Name: namespace}).Error)
			})
			query := config.Query()
			query.Set("search_path", namespace)
			config.RawQuery = query.Encode()
			dsn = config.String()
		}
		t.Setenv("WECHAT_MIGRATION_TEST_DSN", dsn)
		db, dbType, err = chooseDB("WECHAT_MIGRATION_TEST_DSN", false)
		require.NoError(t, err)
		isolatedSQLDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, isolatedSQLDB.Close()) })
		var actualNamespace string
		query := "SELECT DATABASE()"
		if dialect == "postgres" {
			query = "SELECT current_schema()"
		}
		require.NoError(t, db.Raw(query).Scan(&actualNamespace).Error)
		require.Equal(t, namespace, actualNamespace, "refusing to migrate outside the owned namespace")
	}
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Empty(t, tables, "refusing to migrate a non-empty fixture")
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(dbType, dbType)
	initCol()

	var legacyUsers []User
	if origin == "release_d2abf9c" {
		// Verified against d2abf9c95a44c6a3f3655f72d12bec6281a58471: only WeChatOpenId was added to User.
		userType := reflect.TypeFor[User]()
		fields := make([]reflect.StructField, 0, userType.NumField()-1)
		for i := range userType.NumField() {
			if field := userType.Field(i); field.Name != "WeChatOpenId" {
				fields = append(fields, field)
			}
		}
		legacyUser := reflect.New(reflect.StructOf(fields)).Interface()
		require.NoError(t, db.Table("users").AutoMigrate(legacyUser))
		require.False(t, db.Migrator().HasColumn(&User{}, "WeChatOpenId"))
		password, err := common.HashAccountPassword("migration-preserved-password")
		require.NoError(t, err)
		settings, err := common.Marshal(map[string]any{"language": "zh", "theme": "dark"})
		require.NoError(t, err)
		legacyUsers = []User{
			{Username: "legacy_wechat", Password: password, DisplayName: "保留昵称", Email: "legacy@example.test", WeChatId: "legacy-shared", Quota: 12345, UsedQuota: 678, RequestCount: 9, Group: "premium", AffCode: "legacy-one", Setting: string(settings), Remark: "retained profile", AuthVersion: 7, CreatedAt: 1700000000},
			{Username: "legacy_disabled", Password: password, WeChatId: "legacy-shared", Status: common.UserStatusDisabled, AffCode: "legacy-two", AuthVersion: 3, CreatedAt: 1700000001},
			{Username: "legacy_deleted", Password: password, AffCode: "legacy-three", DeletedAt: gorm.DeletedAt{Time: time.Unix(1700000002, 0).UTC(), Valid: true}, CreatedAt: 1700000002},
		}
		require.NoError(t, db.Omit("WeChatOpenId").Create(&legacyUsers).Error)
		// Reload database defaults and timestamp precision before comparing migration results.
		require.NoError(t, db.Unscoped().Order("id").Find(&legacyUsers).Error)
	} else {
		require.False(t, db.Migrator().HasTable(&User{}))
	}
	require.NoError(t, migrateDB())

	identities := []string{"wx-app:OpenID", "wx-app:openid"}
	users := []User{
		{Username: "website_upper", AffCode: "website-upper", WeChatOpenId: &identities[0]},
		{Username: "website_lower", AffCode: "website-lower", WeChatOpenId: &identities[1]},
		{Username: "unbound_one", AffCode: "unbound-one"},
		{Username: "unbound_two", AffCode: "unbound-two"},
	}
	require.NoError(t, db.Create(&users).Error)
	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&User{}))
	for range 2 {
		require.NoError(t, migrateDB())
		if len(legacyUsers) > 0 {
			var saved []User
			require.NoError(t, db.Unscoped().Where("id IN ?", []int{legacyUsers[0].Id, legacyUsers[1].Id, legacyUsers[2].Id}).Order("id").Find(&saved).Error)
			assert.Equal(t, legacyUsers, saved, "all released user data must survive startup")
		}
		for _, index := range statement.Schema.ParseIndexes() {
			assert.True(t, db.Migrator().HasIndex(&User{}, index.Name), "missing index %s", index.Name)
		}
		for name := range statement.Schema.ParseUniqueConstraints() {
			assert.True(t, db.Migrator().HasConstraint(&User{}, name), "missing constraint %s", name)
		}
		var unbound int64
		require.NoError(t, db.Unscoped().Model(&User{}).Where("wechat_open_id IS NULL").Count(&unbound).Error)
		assert.EqualValues(t, len(legacyUsers)+2, unbound, "every unbound value must be SQL NULL")
		for i, identity := range identities {
			var saved User
			require.NoError(t, db.Where("wechat_open_id = ?", identity).First(&saved).Error)
			assert.Equal(t, users[i].Id, saved.Id, "lookup must distinguish identity case")
			require.NotNil(t, saved.WeChatOpenId)
			assert.Equal(t, identity, *saved.WeChatOpenId)
			assert.Error(t, db.Model(&User{}).Where("id = ?", users[2].Id).Update("wechat_open_id", identity).Error, "database uniqueness must survive startup")
		}
		assert.Error(t, db.Model(&User{}).Where("id = ?", users[2].Id).Update("username", users[0].Username).Error, "released username uniqueness must survive startup")
	}
	var version string
	query := "SELECT sqlite_version()"
	if dialect != "sqlite" {
		query = "SELECT version()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("database=%s version=%s origin=%s baseline=d2abf9c95a44c6a3f3655f72d12bec6281a58471 migrateDB_starts=3", dialect, version, origin)
}

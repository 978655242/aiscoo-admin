package model

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestWeChatOpenDefaultOptionDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(dsn)
			}

			prefix := "wc_opt_" + strings.ReplaceAll(uuid.NewString(), "-", "") + "_"
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: prefix}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType := DB, common.MainDatabaseType()
			previousGroupCol, previousKeyCol := commonGroupCol, commonKeyCol
			previousTrueVal, previousFalseVal := commonTrueVal, commonFalseVal
			previousLogGroupCol, previousLogKeyCol := logGroupCol, logKeyCol
			previousPolicy := requestPolicySnapshot.Load()
			previousRegistry := jsplugin.DefaultRegistry
			common.OptionMapRWMutex.Lock()
			previousOptions := maps.Clone(common.OptionMap)
			previousEnabled, previousID := common.WeChatOpenEnabled, common.WeChatOpenAppID
			previousSecret, previousCallback := common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL
			previousAddress := system_setting.ServerAddress
			previousPasskey := *system_setting.GetPasskeySettings()
			common.OptionMapRWMutex.Unlock()
			t.Cleanup(func() {
				assert.NoError(t, db.Migrator().DropTable(&Option{}))
				assert.NoError(t, sqlDB.Close())
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousOptions
				common.WeChatOpenEnabled, common.WeChatOpenAppID = previousEnabled, previousID
				common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL = previousSecret, previousCallback
				system_setting.ServerAddress = previousAddress
				*system_setting.GetPasskeySettings() = previousPasskey
				common.OptionMapRWMutex.Unlock()
				requestPolicySnapshot.Store(previousPolicy)
				jsplugin.DefaultRegistry = previousRegistry
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				commonGroupCol, commonKeyCol = previousGroupCol, previousKeyCol
				commonTrueVal, commonFalseVal = previousTrueVal, previousFalseVal
				logGroupCol, logKeyCol = previousLogGroupCol, previousLogKeyCol
			})
			jsplugin.DefaultRegistry = jsplugin.NewRegistry()
			common.OptionMapRWMutex.Lock()
			common.WeChatOpenEnabled = true
			common.WeChatOpenAppID, common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL = "", "", ""
			system_setting.ServerAddress = "http://localhost:3000"
			*system_setting.GetPasskeySettings() = system_setting.PasskeySettings{}
			common.OptionMapRWMutex.Unlock()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			require.NoError(t, db.AutoMigrate(&Option{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)

			InitOptionMap()
			assert.True(t, common.WeChatOpenEnabled, "website login should be requested when no option is stored")
			assert.ErrorIs(t, system_setting.ValidateWeChatOpenConfiguration(common.WeChatOpenAppID, common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL, system_setting.ServerAddress), system_setting.ErrWeChatOpenConfiguration)
			assert.ErrorIs(t, UpdateOption("WeChatOpenEnabled", "true"), system_setting.ErrWeChatOpenConfiguration)

			for _, option := range []struct{ key, value string }{
				{"ServerAddress", "https://wechat.example.test"},
				{"WeChatOpenAppID", "wx1234567890abcdef"},
				{"WeChatOpenAppSecret", "synthetic-secret"},
				{"WeChatOpenCallbackURL", "https://wechat.example.test/oauth/wechat-open"},
			} {
				require.NoError(t, UpdateOption(option.key, option.value), option.key)
			}
			assert.True(t, common.WeChatOpenEnabled)
			require.NoError(t, system_setting.ValidateWeChatOpenConfiguration(common.WeChatOpenAppID, common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL, system_setting.ServerAddress), "complete configuration must activate the default")
			assert.ErrorIs(t, UpdateOption("ServerAddress", "http://insecure.example.test"), system_setting.ErrWeChatOpenConfiguration)
			assert.Equal(t, "https://wechat.example.test", system_setting.ServerAddress)

			loadOptionsFromDatabase()
			loadOptionsFromDatabase()
			assert.True(t, common.WeChatOpenEnabled, "configured default must survive option reloads")
			require.NoError(t, system_setting.ValidateWeChatOpenConfiguration(common.WeChatOpenAppID, common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL, system_setting.ServerAddress))
			require.NoError(t, UpdateOption("WeChatOpenEnabled", "false"))
			common.WeChatOpenEnabled = true
			InitOptionMap()
			loadOptionsFromDatabase()
			assert.False(t, common.WeChatOpenEnabled, "explicit disable must survive option reloads")
			require.NoError(t, UpdateOption("WeChatOpenEnabled", "true"))
			assert.True(t, common.WeChatOpenEnabled)
		})
	}
}

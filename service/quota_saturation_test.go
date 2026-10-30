package service

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	sqlmysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestAttachQuotaSaturationNestsUnderAdminInfo verifies the saturation marker
// is nested under other.admin_info.quota_saturation so it is admin-only (the
// log formatter strips admin_info for non-admin viewers).
func TestAttachQuotaSaturationNestsUnderAdminInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	relayInfo := &relaycommon.RelayInfo{
		UserId:          7,
		OriginModelName: "gpt-image-1",
		QuotaClamp: &common.QuotaClamp{
			Op:       "QuotaFromDecimal",
			Kind:     common.QuotaClampOverflow,
			Original: 1.8e19,
			Clamped:  common.MaxQuota,
		},
	}

	other := model.NewLogOther()
	other.SetPublic("model_price", 0.004)
	attachQuotaSaturation(ctx, relayInfo, other)

	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok, "admin_info should be created")
	sat, ok := adminInfo["quota_saturation"].(map[string]any)
	require.True(t, ok, "quota_saturation should be nested under admin_info")
	require.Equal(t, "QuotaFromDecimal", sat["op"])
	require.Equal(t, common.QuotaClampOverflow, sat["kind"])
	require.Equal(t, common.MaxQuota, sat["clamped"])
}

func TestCalcViolationFeeQuotaSaturates(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	require.Equal(t, common.MaxQuota, calcViolationFeeQuota(1e20, 1))
}

func TestCalcOpenRouterCacheCreateTokensDoesNotWrap(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	got := CalcOpenRouterCacheCreateTokens(dto.Usage{Cost: math.Inf(1)}, hosttypes.PriceData{
		ModelRatio:         1,
		CacheCreationRatio: 2,
		CacheRatio:         1,
		CompletionRatio:    1,
	})
	require.Equal(t, -1, got)
}

// TestAttachQuotaSaturationPreservesExistingAdminInfo verifies the marker is
// merged into a pre-existing admin_info map without clobbering it.
func TestAttachQuotaSaturationPreservesExistingAdminInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	relayInfo := &relaycommon.RelayInfo{
		QuotaClamp: &common.QuotaClamp{Op: "QuotaFromFloat", Kind: common.QuotaClampUnderflow, Clamped: common.MinQuota},
	}
	other := model.NewLogOther()
	other.SetAdmin("admin_username", "root")
	attachQuotaSaturation(ctx, relayInfo, other)

	adminInfo := other.Snapshot()["admin_info"].(map[string]any)
	require.Equal(t, "root", adminInfo["admin_username"], "existing admin_info fields preserved")
	require.NotNil(t, adminInfo["quota_saturation"])
}

// TestAttachQuotaSaturationNoClampNoMarker verifies the common case (no
// saturation) leaves the log untouched.
func TestAttachQuotaSaturationNoClampNoMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)

	relayInfo := &relaycommon.RelayInfo{QuotaClamp: nil}
	other := model.NewLogOther()
	other.SetPublic("model_price", 0.004)
	attachQuotaSaturation(ctx, relayInfo, other)

	_, hasAdmin := other.Snapshot()["admin_info"]
	require.False(t, hasAdmin, "no admin_info should be added when there is no clamp")
}

func TestPreConsumeBillingRejectsSaturatedQuotaBeforeDeduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{
		QuotaClamp: &common.QuotaClamp{
			Op:       "QuotaFromFloat",
			Kind:     common.QuotaClampOverflow,
			Original: 1e30,
			Clamped:  common.MaxQuota,
		},
	}

	apiErr := PreConsumeBilling(c, common.MaxQuota, info)

	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeModelPriceError, apiErr.GetErrorCode())
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Same(t, info.QuotaClamp, apiErr.Err)
	var clamp *common.QuotaClamp
	require.ErrorAs(t, apiErr, &clamp)
	require.Same(t, info.QuotaClamp, clamp)
	require.Nil(t, info.Billing)
}

func TestPreConsumeBillingRejectsNegativeQuotaBeforeDeduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	info := &relaycommon.RelayInfo{}

	apiErr := PreConsumeBilling(c, -1, info)

	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeModelPriceError, apiErr.GetErrorCode())
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Nil(t, info.Billing)
}

func TestManagedPreConsumeRejectsUnmanagedSession(t *testing.T) {
	for _, info := range []*relaycommon.RelayInfo{
		{ClientPurpose: model.ClientPurposeXCode, Billing: &recordingBillingSettler{}},
		{FundingScope: model.SubscriptionUsageScopeXCode, Billing: &recordingBillingSettler{}},
		{ClientPurpose: model.ClientPurposeXCode, Billing: &BillingSession{funding: &WalletFunding{}}},
	} {
		apiErr := PreConsumeBilling(nil, 10, info)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCode("XCODE_CREDENTIAL_SCOPE_DENIED"), apiErr.GetErrorCode())
	}
}

func TestReserveBeforeSubmitRejectsOverflowAndInvalidQuota(t *testing.T) {
	for _, session := range []*BillingSession{
		{settled: true},
		{preConsumedQuota: -1},
	} {
		require.Error(t, session.ReserveBeforeSubmit(-1))
	}
	session := &BillingSession{funding: &WalletFunding{}, tokenConsumed: math.MaxInt}
	require.ErrorContains(t, session.ReserveBeforeSubmit(1), "overflow")
	assert.Zero(t, session.GetPreConsumedQuota())
}

// DSNs must point at isolated test databases. No production database is used.
func TestRetryReservationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct {
		name common.DatabaseType
		env  string
	}{
		{common.DatabaseTypeSQLite, ""},
		{common.DatabaseTypeMySQL, "TEST_FIXED_MYSQL_DSN"},
		{common.DatabaseTypePostgreSQL, "TEST_FIXED_POSTGRES_DSN"},
	} {
		t.Run(string(dialect.name), func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if dialect.env != "" {
				dsn := os.Getenv(dialect.env)
				if dsn == "" {
					t.Skip(dialect.env + " is not configured")
				}
				if dialect.name == common.DatabaseTypeMySQL {
					parsed, err := sqlmysql.ParseDSN(dsn)
					require.NoError(t, err)
					host, _, err := net.SplitHostPort(parsed.Addr)
					require.NoError(t, err)
					require.True(t, net.ParseIP(host).IsLoopback(), "database tests only permit loopback instances")
					require.Equal(t, "billing_test", parsed.DBName)
					driver = mysql.Open(dsn)
				} else {
					parsed, err := url.Parse(dsn)
					require.NoError(t, err)
					require.True(t, net.ParseIP(parsed.Hostname()).IsLoopback(), "database tests only permit loopback instances")
					require.Equal(t, "/billing_test", parsed.Path)
					driver = postgres.Open(dsn)
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			oldDB, oldLogDB := model.DB, model.LOG_DB
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			oldMaster, oldSQLite := common.IsMasterNode, common.SQLitePath
			common.IsMasterNode = false
			common.SQLitePath = fmt.Sprintf("file:retry_initializer_%s?mode=memory&cache=shared", dialect.name)
			initializer, err := gorm.Open(sqlite.Open(common.SQLitePath), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, initializer.AutoMigrate(&model.Option{}))
			initializerConnection, err := initializer.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, initializerConnection.Close()) })
			require.NoError(t, db.AutoMigrate(&model.Option{}))
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
			}
			t.Setenv("SQL_DSN", dsn)
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitDB())
			initializerDB, err := model.DB.DB()
			require.NoError(t, err)
			require.NoError(t, initializerDB.Close())
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dialect.name, dialect.name)
			t.Cleanup(func() {
				// TestMain's database is SQLite; restore its quoted-column state
				// using only a fresh in-memory database.
				require.NoError(t, os.Setenv("SQL_DSN", ""))
				require.NoError(t, model.InitDB())
				connection, err := model.DB.DB()
				require.NoError(t, err)
				require.NoError(t, connection.Close())
				common.IsMasterNode, common.SQLitePath = oldMaster, oldSQLite
				model.DB, model.LOG_DB = oldDB, oldLogDB
				common.SetDatabaseTypes(oldMain, oldLog)
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.SubscriptionPlan{},
				&model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.XCodeAuthorization{}))
			query := "SELECT version()"
			if dialect.name == common.DatabaseTypeSQLite {
				query = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database: %s", version)

			t.Run("trusted wallet retry enforces balance and rolls back failed token reserve", func(t *testing.T) {
				oldTrust := operation_setting.GetQuotaSetting().TrustQuotaUSD
				operation_setting.GetQuotaSetting().TrustQuotaUSD = 1 / common.QuotaPerUnit
				t.Cleanup(func() { operation_setting.GetQuotaSetting().TrustQuotaUSD = oldTrust })
				user := model.User{Username: "retry-wallet", AffCode: "retry-wallet", Quota: 100, Status: common.UserStatusEnabled}
				require.NoError(t, db.Create(&user).Error)
				token := model.Token{UserId: user.Id, Key: "retry-wallet-key", Name: "retry-wallet", RemainQuota: 100, Status: common.TokenStatusEnabled, ExpiredTime: -1}
				require.NoError(t, db.Create(&token).Error)
				t.Cleanup(func() {
					require.NoError(t, db.Unscoped().Delete(&token).Error)
					require.NoError(t, db.Unscoped().Delete(&user).Error)
				})
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
				ctx.Set("token_quota", 100)
				info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: token.Key,
					UserSetting: dto.UserSetting{BillingPreference: "wallet_only"}}
				require.Nil(t, PreConsumeBilling(ctx, 20, info))
				require.Zero(t, info.Billing.GetPreConsumedQuota(), "initial wallet request uses the trust bypass")
				info.ForcePreConsume = true
				apiErr := PreConsumeBilling(ctx, 120, info)
				require.NotNil(t, apiErr)
				assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
				require.NoError(t, db.Model(&token).Update("remain_quota", 10).Error)
				apiErr = PreConsumeBilling(ctx, 60, info)
				require.NotNil(t, apiErr)
				assert.Equal(t, types.ErrorCodePreConsumeTokenQuotaFailed, apiErr.GetErrorCode())
				require.NoError(t, db.First(&user, user.Id).Error)
				assert.Equal(t, 100, user.Quota, "a failed token reservation restores wallet funds")
				assert.Zero(t, info.Billing.GetPreConsumedQuota())
				require.NoError(t, db.Model(&token).Update("remain_quota", 100).Error)
				require.Nil(t, PreConsumeBilling(ctx, 60, info))
				require.Nil(t, PreConsumeBilling(ctx, 60, info))
				require.NoError(t, db.First(&user, user.Id).Error)
				require.NoError(t, db.First(&token, token.Id).Error)
				assert.Equal(t, 40, user.Quota)
				assert.Equal(t, 40, token.RemainQuota)
				assert.Equal(t, 60, info.Billing.GetPreConsumedQuota())
				require.NoError(t, info.Billing.Settle(40))
				require.NoError(t, db.First(&user, user.Id).Error)
				require.NoError(t, db.First(&token, token.Id).Error)
				assert.Equal(t, 60, user.Quota)
				assert.Equal(t, 60, token.RemainQuota)
			})

			for _, tc := range []struct {
				name       string
				reset      bool
				refund     bool
				finalQuota int
			}{
				{"settle", false, false, 80},
				{"refund", false, true, 0},
				{"settle after reset", true, false, 80},
				{"refund after reset", true, true, 0},
			} {
				t.Run("managed retry retains reservation/"+tc.name, func(t *testing.T) {
					now := time.Now().Unix()
					user := model.User{Username: "highest-retry", AffCode: "highest-retry", Quota: 1000, Status: common.UserStatusEnabled}
					require.NoError(t, db.Create(&user).Error)
					token := model.Token{UserId: user.Id, Key: "highest-retry-key", Name: "highest-retry", RemainQuota: 500, Status: common.TokenStatusEnabled, ExpiredTime: -1}
					require.NoError(t, db.Create(&token).Error)
					sub := model.UserSubscription{UserId: user.Id, UsageScope: model.SubscriptionUsageScopeXCode, Status: "active", AmountTotal: 150,
						QuotaResetPeriodSnapshot: model.SubscriptionResetNever, StartTime: now - 60, EndTime: now + 3600}
					require.NoError(t, db.Create(&sub).Error)
					require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)
					t.Cleanup(func() {
						require.NoError(t, db.Where("user_id = ?", user.Id).Delete(&model.SubscriptionPreConsumeRecord{}).Error)
						require.NoError(t, db.Where("user_id = ?", user.Id).Delete(&model.XCodeAuthorization{}).Error)
						require.NoError(t, db.Unscoped().Delete(&sub).Error)
						require.NoError(t, db.Unscoped().Delete(&token).Error)
						require.NoError(t, db.Unscoped().Delete(&user).Error)
					})
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
					info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: token.Key,
						RequestId: "highest-retry-request", ClientPurpose: model.ClientPurposeXCode,
						FundingScope: model.SubscriptionUsageScopeXCode, FundingSubscriptionID: sub.Id,
						FundingAuthorizationRevision: 1, ForcePreConsume: true}
					require.Nil(t, PreConsumeBilling(ctx, 100, info))
					if tc.reset {
						require.NoError(t, db.Model(&sub).Updates(map[string]any{"amount_used": 0, "quota_reset_generation": 2}).Error)
					}
					target := model.SubscriptionFundingTarget{Scope: model.SubscriptionUsageScopeXCode, SubscriptionID: sub.Id, AuthorizationRevision: 1}
					for _, retryQuota := range []int{60, 80} {
						require.Nil(t, PreConsumeBilling(ctx, retryQuota, info))
						require.NoError(t, db.First(&sub, sub.Id).Error)
						require.NoError(t, db.First(&token, token.Id).Error)
						assert.EqualValues(t, 100, sub.AmountUsed)
						assert.Equal(t, 400, token.RemainQuota)
						assert.Equal(t, 100, info.Billing.GetPreConsumedQuota())
						_, err := model.PreConsumeUserSubscription("competing-request", user.Id, "test-model", 0, 75, target)
						require.ErrorIs(t, err, model.ErrSubscriptionQuotaInsufficient)
					}
					if tc.refund {
						info.Billing.Refund(ctx)
						info.Billing.Refund(ctx)
						require.Eventually(t, func() bool {
							var updatedSub model.UserSubscription
							var updatedToken model.Token
							return db.First(&updatedSub, sub.Id).Error == nil && db.First(&updatedToken, token.Id).Error == nil &&
								updatedSub.AmountUsed == 0 && updatedToken.RemainQuota == 500
						}, 3*time.Second, 10*time.Millisecond)
					} else {
						require.NoError(t, info.Billing.Settle(tc.finalQuota))
						require.NoError(t, info.Billing.Settle(tc.finalQuota))
					}
					require.NoError(t, db.First(&sub, sub.Id).Error)
					require.NoError(t, db.First(&token, token.Id).Error)
					require.NoError(t, db.First(&user, user.Id).Error)
					var fact model.SubscriptionPreConsumeRecord
					require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&fact).Error)
					if tc.refund {
						assert.Equal(t, "refunded", fact.Status)
					} else {
						assert.Equal(t, "settled", fact.Status)
					}
					assert.EqualValues(t, tc.finalQuota, sub.AmountUsed)
					assert.Equal(t, 500-tc.finalQuota, token.RemainQuota)
					assert.Equal(t, 1000, user.Quota)
				})
			}

			t.Run("managed zero reservation and paid retry stay on subscription", func(t *testing.T) {
				now := time.Now().Unix()
				user := model.User{Username: "retry-xcode", AffCode: "retry-xcode", Quota: 900, Status: common.UserStatusEnabled}
				require.NoError(t, db.Create(&user).Error)
				token := model.Token{UserId: user.Id, Key: "retry-xcode-key", Name: "retry-xcode", RemainQuota: 100, Status: common.TokenStatusEnabled, ExpiredTime: -1}
				require.NoError(t, db.Create(&token).Error)
				plan := model.SubscriptionPlan{Title: "retry-xcode", TotalAmount: 100, QuotaResetPeriod: model.SubscriptionResetNever}
				require.NoError(t, db.Create(&plan).Error)
				sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, UsageScope: model.SubscriptionUsageScopeXCode,
					Source: "admin", Status: "active", AmountTotal: 100, StartTime: now - 60, EndTime: now + 3600,
					QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
				require.NoError(t, db.Create(&sub).Error)
				authorization := model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}
				require.NoError(t, db.Create(&authorization).Error)
				t.Cleanup(func() {
					require.NoError(t, db.Where("user_id = ?", user.Id).Delete(&model.SubscriptionPreConsumeRecord{}).Error)
					require.NoError(t, db.Where("user_id = ?", user.Id).Delete(&model.XCodeAuthorization{}).Error)
					require.NoError(t, db.Unscoped().Delete(&sub).Error)
					require.NoError(t, db.Unscoped().Delete(&plan).Error)
					require.NoError(t, db.Unscoped().Delete(&token).Error)
					require.NoError(t, db.Unscoped().Delete(&user).Error)
				})
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
				info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, RequestId: fmt.Sprintf("retry-xcode-%s", dialect.name),
					OriginModelName: "retry-xcode", ForcePreConsume: true, UserSetting: dto.UserSetting{BillingPreference: "wallet_only"},
					ClientPurpose: model.ClientPurposeXCode, FundingScope: model.SubscriptionUsageScopeXCode,
					FundingSubscriptionID: sub.Id, FundingAuthorizationRevision: 1}
				require.Nil(t, PreConsumeBilling(ctx, 0, info))
				require.True(t, info.Billing.NeedsRefund())
				var fact model.SubscriptionPreConsumeRecord
				require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&fact).Error)
				assert.Equal(t, "consumed", fact.Status)
				assert.Zero(t, fact.CycleReservedQuota)
				require.NoError(t, db.Model(&token).Update("remain_quota", 10).Error)
				require.NotNil(t, PreConsumeBilling(ctx, 60, info))
				require.NoError(t, db.First(&sub, sub.Id).Error)
				require.NoError(t, db.First(&fact, fact.Id).Error)
				assert.Zero(t, sub.AmountUsed)
				assert.Zero(t, fact.CycleReservedQuota)
				assert.Zero(t, info.Billing.GetPreConsumedQuota())
				require.NoError(t, db.Model(&token).Update("remain_quota", 100).Error)
				require.Nil(t, PreConsumeBilling(ctx, 60, info))
				apiErr := PreConsumeBilling(ctx, 120, info)
				require.NotNil(t, apiErr)
				assert.Equal(t, types.ErrorCode("XCODE_QUOTA_EXHAUSTED"), apiErr.GetErrorCode())
				require.NoError(t, info.Billing.Settle(60))
				require.NoError(t, db.First(&user, user.Id).Error)
				require.NoError(t, db.First(&token, token.Id).Error)
				require.NoError(t, db.First(&sub, sub.Id).Error)
				require.NoError(t, db.First(&fact, fact.Id).Error)
				assert.Equal(t, 900, user.Quota, "managed funding must never use the wallet")
				assert.Equal(t, 40, token.RemainQuota)
				assert.EqualValues(t, 60, sub.AmountUsed)
				assert.Equal(t, "settled", fact.Status)
				assert.EqualValues(t, 60, fact.FinalQuota)
			})
		})
	}
}

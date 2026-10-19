package controller

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

// The numeric probe widens only its separately owned ClickHouse fixture, not the native-schema fixture.
func xcodeUsageDB(t *testing.T, kind, dsn string, numericProbe bool) *gorm.DB {
	t.Helper()
	mainKind, mainDSN := kind, dsn
	if kind == "clickhouse" {
		mainKind, mainDSN = "sqlite", ""
	}
	db := modelManagementDB(t, mainKind, mainDSN)
	previousConsume, previousQuota := common.LogConsumeEnabled, common.QuotaPerUnit
	common.LogConsumeEnabled, common.QuotaPerUnit = true, 500000
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.QuotaPerUnit = previousConsume, previousQuota
	})
	if kind != "clickhouse" {
		require.NoError(t, db.AutoMigrate(&model.Log{}))
		return db
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.True(t, net.ParseIP(parsed.Hostname()).IsLoopback(), "ClickHouse tests require a loopback disposable instance")
	fixture := "native"
	if numericProbe {
		fixture = "numeric"
	}
	name := fmt.Sprintf("newapi_xcode_usage_%s_%d", fixture, time.Now().UnixNano())
	admin, err := gorm.Open(clickhouse.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, admin.Exec("CREATE DATABASE "+name).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec("DROP DATABASE "+name).Error)
		connection, err := admin.DB()
		require.NoError(t, err)
		require.NoError(t, connection.Close())
	})
	parsed.Path = "/" + name
	t.Setenv("LOG_SQL_DSN", parsed.String())
	t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "0")
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, model.InitLogDB())
	logDB := model.LOG_DB
	t.Cleanup(func() {
		connection, err := logDB.DB()
		require.NoError(t, err)
		require.NoError(t, connection.Close())
	})
	var version string
	require.NoError(t, logDB.Raw("SELECT version()").Scan(&version).Error)
	t.Logf("ClickHouse %s; owned %s fixture: %s", version, fixture, name)
	if numericProbe {
		require.NoError(t, logDB.Exec("ALTER TABLE logs MODIFY COLUMN quota Int64, MODIFY COLUMN prompt_tokens Int64, MODIFY COLUMN completion_tokens Int64").Error)
	} else {
		var columns []struct {
			Name string
			Type string
		}
		require.NoError(t, logDB.Raw("SELECT name, type FROM system.columns WHERE database = ? AND table = ? AND name IN (?, ?, ?)", name, "logs", "quota", "prompt_tokens", "completion_tokens").Scan(&columns).Error)
		require.Len(t, columns, 3)
		for _, column := range columns {
			assert.Equal(t, "Int32", column.Type, column.Name+" must remain the native production type")
		}
	}
	return logDB
}

func TestXCodeAccountUsageDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}, {"clickhouse", "TEST_CLICKHOUSE_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := xcodeUsageDB(t, dialect.kind, os.Getenv(dialect.env), false)
			xcodeUsageCalendarContracts(t, db)
			owner := model.User{Username: "usage-old", AffCode: "usage-owner"}
			require.NoError(t, model.DB.Create(&owner).Error)
			other := model.User{Username: "usage-other", AffCode: "usage-other"}
			require.NoError(t, model.DB.Create(&other).Error)
			require.NoError(t, model.DB.Model(&owner).Update("username", "usage-renamed").Error)
			modelOwner := model.User{Username: "usage-models", AffCode: "usage-models"}
			require.NoError(t, model.DB.Create(&modelOwner).Error)
			start := time.Date(2025, 3, 9, 5, 0, 0, 0, time.UTC)
			logs := []model.Log{
				{UserId: owner.Id, Username: "usage-old", CreatedAt: start.Unix(), Type: model.LogTypeConsume, ModelName: "z-retired", TokenId: 1, TokenName: "wallet-key", Quota: 10, PromptTokens: 2, CompletionTokens: 3, Other: `{"funding_source":"wallet","admin_info":{"secret":"hidden"}}`, Ip: "private-ip"},
				{UserId: owner.Id, Username: "usage-renamed", CreatedAt: start.Add(time.Hour).Unix(), Type: model.LogTypeConsume, ModelName: "a-historical", TokenId: 2, TokenName: "subscription-key", Quota: 20, PromptTokens: 5, CompletionTokens: 7, Other: `{"funding_source":"subscription"}`},
				{UserId: owner.Id, Username: "usage-old", CreatedAt: start.Add(2 * time.Hour).Unix(), Type: model.LogTypeConsume, ModelName: "a-historical", TokenId: 3, Quota: 4, Content: "task supplement", Other: `{"funding_source":"xcode"}`},
				{UserId: owner.Id, CreatedAt: start.Add(22*time.Hour + 59*time.Minute + 59*time.Second).Unix(), Type: model.LogTypeRefund, ModelName: "a-historical", Quota: 50, PromptTokens: 999, CompletionTokens: 999},
				{UserId: owner.Id, CreatedAt: start.Add(time.Hour).Unix(), Type: model.LogTypeRefund, ModelName: "a-historical", Quota: -9},
				{UserId: other.Id, Username: "usage-old", CreatedAt: start.Unix(), Type: model.LogTypeConsume, ModelName: "other-user", Quota: 999},
				{UserId: owner.Id, CreatedAt: start.Add(-time.Second).Unix(), Type: model.LogTypeConsume, ModelName: "before-range", Quota: 999},
				{UserId: owner.Id, CreatedAt: start.Add(23 * time.Hour).Unix(), Type: model.LogTypeRefund, ModelName: "after-range", Quota: 999},
				{UserId: owner.Id, CreatedAt: start.Unix(), Type: model.LogTypeTopup, ModelName: "not-consumption", Quota: 999},
				{UserId: modelOwner.Id, CreatedAt: start.Unix(), Type: model.LogTypeConsume, ModelName: "CaseModel", Quota: 3},
				{UserId: modelOwner.Id, CreatedAt: start.Unix(), Type: model.LogTypeConsume, ModelName: "casemodel", Quota: 5},
				{UserId: modelOwner.Id, CreatedAt: start.Unix(), Type: model.LogTypeConsume, ModelName: "casemodel ", Quota: 7},
			}
			for i := range logs {
				logs[i].RequestId = fmt.Sprintf("usage-semantic-%d", i)
			}
			require.NoError(t, db.Create(&logs).Error)
			// A separate selected LOG_DB must not fall back to main DB logs.
			if dialect.kind == "clickhouse" {
				require.NoError(t, model.DB.AutoMigrate(&model.Log{}))
				require.NoError(t, model.DB.Create(&model.Log{UserId: owner.Id, CreatedAt: start.Unix(), Type: model.LogTypeConsume, ModelName: "wrong-main-db", Quota: 999}).Error)
			}
			now := time.Date(2025, 3, 9, 12, 34, 56, 789000000, time.UTC)
			snapshot, err := service.GetXCodeAccountUsage(owner.Id, dto.XCodeUsageQuery{TimeZone: "America/New_York"}, now)
			require.NoError(t, err)
			assert.Equal(t, 1, snapshot.SchemaVersion)
			assert.Equal(t, "America/New_York", snapshot.TimeZone)
			assert.Equal(t, dto.XCodeUsageSummary{ConsumeRecordCount: "3", PromptTokens: "7", CompletionTokens: "10", TotalTokens: "17", ChargedQuota: "34", RefundedQuota: "41", NetQuota: "-7"}, snapshot.Summary)
			assert.Equal(t, "new-api-account", snapshot.Scope)
			assert.Equal(t, "consume-refund-logs", snapshot.Source)
			assert.NotEmpty(t, snapshot.RetentionBasis)
			assert.Equal(t, "2025-03-09", snapshot.RangeStartDate)
			assert.Equal(t, "2025-03-09", snapshot.RangeEndDate)
			assert.Equal(t, "hour", snapshot.Granularity)
			assert.Equal(t, now.UnixMilli(), snapshot.ServerTime)
			assert.Equal(t, now.UnixMilli(), snapshot.AsOf)
			assert.Equal(t, "500000", snapshot.Display.QuotaPerUnit)
			assert.Equal(t, []string{"agent_duration", "tool_execution", "tool_error_rate", "decode_speed", "reasoning_tokens", "cache_token_breakdown"}, snapshot.UnsupportedMetrics)
			require.Len(t, snapshot.Buckets, 23)
			assert.Equal(t, start.UnixMilli(), snapshot.Buckets[0].StartTime)
			assert.Equal(t, start.Add(23*time.Hour).UnixMilli(), snapshot.Buckets[22].EndTime)
			assert.Equal(t, "50", snapshot.Buckets[22].RefundedQuota)
			require.Len(t, snapshot.Models, 2)
			assert.Equal(t, "a-historical", snapshot.Models[0].ModelID)
			assert.Equal(t, "-17", snapshot.Models[0].NetQuota)
			assert.Equal(t, "z-retired", snapshot.Models[1].ModelID)
			require.Len(t, snapshot.Models[0].Buckets, 23)
			assert.Equal(t, "0", snapshot.Models[0].Buckets[0].ChargedQuota)
			encoded, err := common.Marshal(snapshot)
			require.NoError(t, err)
			for _, private := range []string{"private-ip", "admin_info", "funding_source", "wallet-key", "subscription-key", "usage-old"} {
				assert.NotContains(t, string(encoded), private)
			}
			modelSnapshot, err := service.GetXCodeAccountUsage(modelOwner.Id, dto.XCodeUsageQuery{TimeZone: "America/New_York"}, now)
			require.NoError(t, err)
			require.Len(t, modelSnapshot.Models, 3)
			assert.Equal(t, "CaseModel", modelSnapshot.Models[0].ModelID)
			assert.Equal(t, "3", modelSnapshot.Models[0].ChargedQuota)
			assert.Equal(t, "casemodel", modelSnapshot.Models[1].ModelID)
			assert.Equal(t, "5", modelSnapshot.Models[1].ChargedQuota)
			assert.Equal(t, "casemodel ", modelSnapshot.Models[2].ModelID)
			assert.Equal(t, "7", modelSnapshot.Models[2].ChargedQuota)
			assert.Equal(t, "15", modelSnapshot.Summary.NetQuota)
			common.LogConsumeEnabled = false
			_, err = service.GetXCodeAccountUsage(owner.Id, dto.XCodeUsageQuery{TimeZone: "UTC"}, now)
			assert.ErrorIs(t, err, service.ErrXCodeUsageUnavailable)
			common.LogConsumeEnabled = true
			selectedDB := model.LOG_DB
			model.LOG_DB = nil
			_, err = service.GetXCodeAccountUsage(owner.Id, dto.XCodeUsageQuery{}, now)
			assert.ErrorIs(t, err, service.ErrXCodeUsageUnavailable)
			model.LOG_DB = selectedDB
			require.NoError(t, db.Migrator().DropTable(&model.Log{}))
			_, err = service.GetXCodeAccountUsage(owner.Id, dto.XCodeUsageQuery{}, now)
			assert.ErrorIs(t, err, service.ErrXCodeUsageUnavailable)
		})
	}
}

func xcodeUsageCalendarContracts(t *testing.T, db *gorm.DB) {
	t.Helper()
	for index, tc := range []struct {
		name                 string
		now                  time.Time
		query                dto.XCodeUsageQuery
		start, end, firstEnd string
		buckets              int
		startDate, endDate   string
	}{
		{"spring-23-hours", time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC), dto.XCodeUsageQuery{TimeZone: "America/New_York"}, "2025-03-09T05:00:00Z", "2025-03-10T04:00:00Z", "2025-03-09T06:00:00Z", 23, "2025-03-09", "2025-03-09"},
		{"fall-25-hours", time.Date(2025, 11, 2, 12, 0, 0, 0, time.UTC), dto.XCodeUsageQuery{TimeZone: "America/New_York"}, "2025-11-02T04:00:00Z", "2025-11-03T05:00:00Z", "2025-11-02T05:00:00Z", 25, "2025-11-02", "2025-11-02"},
		{"midnight-gap", time.Date(2025, 9, 7, 12, 0, 0, 0, time.UTC), dto.XCodeUsageQuery{TimeZone: "America/Santiago"}, "2025-09-07T04:00:00Z", "2025-09-08T03:00:00Z", "2025-09-07T05:00:00Z", 23, "2025-09-07", "2025-09-07"},
		{"repeated-midnight", time.Date(2025, 11, 2, 12, 0, 0, 0, time.UTC), dto.XCodeUsageQuery{TimeZone: "America/Havana"}, "2025-11-02T04:00:00Z", "2025-11-03T05:00:00Z", "2025-11-02T05:00:00Z", 25, "2025-11-02", "2025-11-02"},
		{"leap-month-7d", time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), dto.XCodeUsageQuery{Range: "7d", TimeZone: "UTC"}, "2024-02-24T00:00:00Z", "2024-03-02T00:00:00Z", "2024-02-25T00:00:00Z", 7, "2024-02-24", "2024-03-01"},
		{"leap-month-30d", time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), dto.XCodeUsageQuery{Range: "30d", TimeZone: "UTC"}, "2024-02-01T00:00:00Z", "2024-03-02T00:00:00Z", "2024-02-02T00:00:00Z", 30, "2024-02-01", "2024-03-01"},
		{"custom-30-dates-dst", time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC), dto.XCodeUsageQuery{Range: "custom", TimeZone: "America/New_York", StartDate: "2025-03-01", EndDate: "2025-03-30"}, "2025-03-01T05:00:00Z", "2025-03-31T04:00:00Z", "2025-03-02T05:00:00Z", 30, "2025-03-01", "2025-03-30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, err := time.Parse(time.RFC3339, tc.start)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339, tc.end)
			require.NoError(t, err)
			firstEnd, err := time.Parse(time.RFC3339, tc.firstEnd)
			require.NoError(t, err)
			userID := 42 + index
			logs := []model.Log{
				{UserId: userID, CreatedAt: start.Add(-time.Second).Unix(), Type: model.LogTypeConsume, Quota: 999},
				{UserId: userID, CreatedAt: start.Unix(), Type: model.LogTypeConsume, Quota: 11},
				{UserId: userID, CreatedAt: firstEnd.Unix(), Type: model.LogTypeConsume, Quota: 7},
				{UserId: userID, CreatedAt: end.Add(-time.Second).Unix(), Type: model.LogTypeRefund, Quota: 19},
				{UserId: userID, CreatedAt: end.Unix(), Type: model.LogTypeRefund, Quota: 999},
			}
			for i := range logs {
				logs[i].RequestId = fmt.Sprintf("usage-calendar-%d-%d", index, i)
			}
			require.NoError(t, db.Create(&logs).Error)
			snapshot, err := service.GetXCodeAccountUsage(userID, tc.query, tc.now)
			require.NoError(t, err)
			require.Len(t, snapshot.Buckets, tc.buckets)
			assert.Equal(t, tc.startDate, snapshot.RangeStartDate)
			assert.Equal(t, tc.endDate, snapshot.RangeEndDate)
			assert.Equal(t, start.UnixMilli(), snapshot.Buckets[0].StartTime)
			assert.Equal(t, end.UnixMilli(), snapshot.Buckets[tc.buckets-1].EndTime)
			assert.Equal(t, "11", snapshot.Buckets[0].ChargedQuota)
			assert.Equal(t, "7", snapshot.Buckets[1].ChargedQuota)
			assert.Equal(t, "18", snapshot.Summary.ChargedQuota)
			assert.Equal(t, "19", snapshot.Summary.RefundedQuota)
			assert.Equal(t, "-1", snapshot.Summary.NetQuota)
			if tc.name == "fall-25-hours" {
				assert.Contains(t, snapshot.Buckets[1].Label, "01:00")
				assert.Contains(t, snapshot.Buckets[2].Label, "01:00")
				assert.Contains(t, snapshot.Buckets[1].Label, "-04:00")
				assert.Contains(t, snapshot.Buckets[2].Label, "-05:00")
			}
			if tc.name == "custom-30-dates-dst" {
				assert.Equal(t, int64(23*time.Hour/time.Millisecond), snapshot.Buckets[8].EndTime-snapshot.Buckets[8].StartTime)
			}
		})
	}
	previousLocal := time.Local
	time.Local = time.FixedZone("fixture-local", 19800)
	defer func() { time.Local = previousLocal }()
	snapshot, err := service.GetXCodeAccountUsage(99, dto.XCodeUsageQuery{}, time.Date(2025, 3, 8, 20, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, "2025-03-09", snapshot.RangeStartDate)
	assert.Equal(t, "fixture-local", snapshot.TimeZone)
	assert.Empty(t, snapshot.Models)
	assert.Equal(t, "0", snapshot.Summary.NetQuota)
}

func TestXCodeAccountUsageRejectsInvalidRequest(t *testing.T) {
	xcodeUsageDB(t, "sqlite", "", false)
	for _, query := range []dto.XCodeUsageQuery{
		{Range: "week"}, {TimeZone: "Local"}, {TimeZone: "server-local"}, {TimeZone: "Not/A_Zone"},
		{Range: "custom"}, {Range: "custom", StartDate: "2025-02-29", EndDate: "2025-03-01"},
		{Range: "custom", StartDate: "2025-3-01", EndDate: "2025-03-02"},
		{Range: "custom", StartDate: "2025-03-02", EndDate: "2025-03-01"},
		{Range: "custom", StartDate: "2025-03-01", EndDate: "2025-03-31"},
	} {
		_, err := service.GetXCodeAccountUsage(1, query, time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC))
		assert.ErrorIs(t, err, service.ErrXCodeUsageRequestInvalid, "%+v", query)
	}
	for _, id := range []int{0, -1} {
		_, err := service.GetXCodeAccountUsage(id, dto.XCodeUsageQuery{}, time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC))
		assert.ErrorIs(t, err, service.ErrXCodeUsageRequestInvalid)
	}
}

func TestXCodeAccountUsageExactNumericBoundaryMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}, {"clickhouse", "TEST_CLICKHOUSE_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env)
			}
			db := xcodeUsageDB(t, dialect.kind, os.Getenv(dialect.env), true)
			now := time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC)
			stamp := now.Unix()
			logs := []model.Log{
				{UserId: 1, CreatedAt: stamp, Type: model.LogTypeConsume, ModelName: "exact", Quota: 9007199254740993, PromptTokens: 9007199254740993, CompletionTokens: 2},
				{UserId: 1, CreatedAt: stamp, Type: model.LogTypeConsume, ModelName: "exact", Quota: 2},
				{UserId: 1, CreatedAt: stamp, Type: model.LogTypeRefund, ModelName: "exact", Quota: 1},
				{UserId: 2, CreatedAt: stamp, Type: model.LogTypeConsume, ModelName: "sum-overflow", Quota: math.MaxInt64},
				{UserId: 2, CreatedAt: stamp, Type: model.LogTypeConsume, ModelName: "sum-overflow", Quota: 1},
				{UserId: 3, CreatedAt: stamp, Type: model.LogTypeConsume, ModelName: "a", Quota: math.MaxInt64},
				{UserId: 3, CreatedAt: stamp + 3600, Type: model.LogTypeConsume, ModelName: "b", Quota: 1},
				{UserId: 4, CreatedAt: stamp, Type: model.LogTypeConsume, ModelName: "tokens-overflow", PromptTokens: math.MaxInt64, CompletionTokens: 1},
				{UserId: 5, CreatedAt: stamp, Type: model.LogTypeConsume, ModelName: "net-underflow", Quota: math.MinInt64},
				{UserId: 5, CreatedAt: stamp, Type: model.LogTypeRefund, ModelName: "net-underflow", Quota: 1},
			}
			for i := range logs {
				logs[i].RequestId = fmt.Sprintf("usage-numeric-%d", i)
			}
			require.NoError(t, db.Create(&logs).Error)
			snapshot, err := service.GetXCodeAccountUsage(1, dto.XCodeUsageQuery{TimeZone: "UTC"}, now)
			require.NoError(t, err)
			assert.Equal(t, "9007199254740995", snapshot.Summary.ChargedQuota)
			assert.Equal(t, "9007199254740994", snapshot.Summary.NetQuota)
			assert.Equal(t, "9007199254740993", snapshot.Summary.PromptTokens)
			assert.Equal(t, "9007199254740995", snapshot.Summary.TotalTokens)
			encoded, err := common.Marshal(snapshot)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"charged_quota":"9007199254740995"`)
			for _, id := range []int{2, 3, 4, 5} {
				result, err := service.GetXCodeAccountUsage(id, dto.XCodeUsageQuery{TimeZone: "UTC"}, now)
				assert.ErrorIs(t, err, service.ErrXCodeUsageUnavailable, "user %d", id)
				assert.Nil(t, result)
			}
		})
	}
}

func TestXCodeReadFacadeDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env)
			}
			db := xcodeUsageDB(t, dialect.kind, os.Getenv(dialect.env), false)
			require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.XCodeAuthorization{}, &model.UserSession{}))
			previousSecret := common.SessionSecret
			previousLogEnabled := common.LogConsumeEnabled
			common.LogConsumeEnabled = true
			previousPayment := *operation_setting.GetPaymentSetting()
			common.SessionSecret = "owned-xcode-read-facade-secret"
			operation_setting.GetPaymentSetting().ComplianceConfirmed = true
			operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
			t.Cleanup(func() {
				common.SessionSecret = previousSecret
				*operation_setting.GetPaymentSetting() = previousPayment
				common.LogConsumeEnabled = previousLogEnabled
			})
			user := model.User{Username: "read-owner", DisplayName: "Current account", AffCode: "read-owner", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1, Password: "owned-private-password", Email: "owned-private-email"}
			require.NoError(t, db.Create(&user).Error)
			plan := model.SubscriptionPlan{Title: "Visible plan", PriceAmount: 7.5, Currency: "USD", DurationUnit: "month", DurationValue: 1, Enabled: true, TotalAmount: 500, QuotaResetPeriod: "never", StripePriceId: "owned-private-product"}
			require.NoError(t, db.Create(&plan).Error)
			disabled := model.SubscriptionPlan{Title: "Hidden plan", Enabled: true}
			require.NoError(t, db.Create(&disabled).Error)
			require.NoError(t, db.Model(&disabled).Update("enabled", false).Error)
			now := model.GetDBTimestamp()
			sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 86400, AmountTotal: 500, AmountUsed: 17, QuotaResetPeriodSnapshot: "never", QuotaResetGeneration: 1}
			require.NoError(t, db.Create(&sub).Error)
			require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 9007199254740993}).Error)
			native, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "read-facade-fixture")
			require.NoError(t, err)
			stored, err := model.GetUserSessionBySID(native.Session.SID)
			require.NoError(t, err)
			stored.ClientPurpose = model.ClientPurposeXCode
			require.NoError(t, db.Model(stored).Update("client_purpose", stored.ClientPurpose).Error)
			control, _, err := service.IssueXCodeAccessToken(stored)
			require.NoError(t, err)
			engine := gin.New()
			engine.Use(middleware.XCodeControlAuth())
			for path, handler := range map[string]gin.HandlerFunc{
				"me": GetXCodeMe, "plans": GetXCodePlans, "entitlements": GetXCodeEntitlements,
				"usage": GetXCodeUsage, "client-config": GetXCodeClientConfig,
			} {
				engine.GET("/api/xcode/v1/"+path, handler)
			}
			get := func(path, token string) *httptest.ResponseRecorder {
				t.Helper()
				request := httptest.NewRequest(http.MethodGet, "/api/xcode/v1/"+path+"?user_id=99999", nil)
				request.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, request)
				return response
			}
			response := get("me", control)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var profile struct {
				Data dto.XCodeMeSnapshot `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &profile))
			assert.Equal(t, strconv.Itoa(user.Id), profile.Data.User.ID)
			assert.Equal(t, "Current account", profile.Data.User.DisplayName)
			assert.Nil(t, profile.Data.User.AvatarURL)
			assert.Equal(t, stored.SID, profile.Data.SessionID)
			assert.Equal(t, "9007199254740993", profile.Data.AuthorizationRevision)
			for _, path := range []string{"me", "plans", "entitlements", "usage", "client-config"} {
				response := get(path, control)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				for _, private := range []string{"owned-private-password", "owned-private-email", "owned-private-product", native.RefreshToken, control} {
					assert.NotContains(t, response.Body.String(), private)
				}
				assert.Equal(t, http.StatusForbidden, get(path, native.AccessToken).Code)
			}
			plans, err := service.GetXCodePlans()
			require.NoError(t, err)
			require.Len(t, plans.Items, 1)
			assert.Equal(t, strconv.Itoa(plan.Id), plans.Items[0].ID)
			assert.Equal(t, "7.5", plans.Items[0].PriceAmount)
			assert.Equal(t, "500", plans.Items[0].TotalQuota)
			operation_setting.GetPaymentSetting().ComplianceConfirmed = false
			plans, err = service.GetXCodePlans()
			require.NoError(t, err)
			assert.Empty(t, plans.Items)
			operation_setting.GetPaymentSetting().ComplianceConfirmed = true
			require.NoError(t, db.Model(&sub).Updates(map[string]any{
				"quota_reset_period_snapshot": "daily", "amount_used": 17,
				"last_reset_time": now - 86400, "next_reset_time": now - 1,
			}).Error)
			response = get("entitlements", control)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var entitlements struct {
				Data dto.XCodeAuthorizationSnapshot `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &entitlements))
			require.NotNil(t, entitlements.Data.Quota)
			assert.Equal(t, "0", entitlements.Data.Quota.Used)
			require.NoError(t, db.First(&sub, sub.Id).Error)
			assert.Zero(t, sub.AmountUsed)
			assert.Equal(t, int64(2), sub.QuotaResetGeneration)
			require.NoError(t, db.Migrator().DropTable(&model.SubscriptionPlan{}))
			response = get("plans", control)
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			assert.Contains(t, response.Body.String(), "XCODE_BACKEND_UNAVAILABLE")
		})
	}
}

func TestXCodeReadDisplayUnits(t *testing.T) {
	general := operation_setting.GetGeneralSetting()
	previousGeneral, previousUnit, previousRate := *general, common.QuotaPerUnit, operation_setting.USDExchangeRate
	t.Cleanup(func() {
		*general, common.QuotaPerUnit, operation_setting.USDExchangeRate = previousGeneral, previousUnit, previousRate
	})
	common.QuotaPerUnit, operation_setting.USDExchangeRate = 500000, 7.3
	general.CustomCurrencySymbol, general.CustomCurrencyExchangeRate = "¤S", 2.5
	for _, tc := range []struct{ kind, currency, symbol, rate string }{
		{"USD", "USD", "$", "1"}, {"CNY", "CNY", "¥", "7.3"},
		{"CUSTOM", "", "¤S", "2.5"}, {"TOKENS", "", "", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			general.QuotaDisplayType = tc.kind
			config, err := service.GetXCodeClientConfig()
			require.NoError(t, err)
			assert.Equal(t, "500000", config.Display.QuotaPerUnit)
			assert.Equal(t, tc.kind, config.Display.DisplayType)
			assert.Equal(t, tc.symbol, config.Display.CurrencySymbol)
			if tc.currency == "" {
				assert.Nil(t, config.Display.Currency)
			} else {
				require.NotNil(t, config.Display.Currency)
				assert.Equal(t, tc.currency, *config.Display.Currency)
			}
			if tc.rate == "" {
				assert.Nil(t, config.Display.USDToCurrencyRate)
			} else {
				require.NotNil(t, config.Display.USDToCurrencyRate)
				assert.Equal(t, tc.rate, *config.Display.USDToCurrencyRate)
			}
		})
	}
	general.QuotaDisplayType = "CUSTOM"
	general.CustomCurrencyExchangeRate = 0
	_, err := service.GetXCodeClientConfig()
	require.Error(t, err)
	general.QuotaDisplayType = "USD"
	common.QuotaPerUnit = math.Inf(1)
	_, err = service.GetXCodeClientConfig()
	require.Error(t, err)
}

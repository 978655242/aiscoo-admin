package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	xcodeDTO "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionRejectsInvalidTaskBillingExpressions(t *testing.T) {
	const pluginKey = "billing-save-probe"
	const modelName = "billing-save-model"
	source := `
export const meta = {
  apiVersion: 1, key: "billing-save-probe", name: "Billing Save Probe", version: "1.0.0", author: {name: "Test"},
  models: ["billing-save-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}}
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	tests := []struct {
		name       string
		expression string
		errorText  string
	}{
		{
			name:       "invalid syntax",
			expression: `tier("base",`,
			errorText:  "expr compile error",
		},
		{
			name:       "undeclared usage key",
			expression: `tier("base", u("clips") * 0.1)`,
			errorText:  `usage key \"clips\" is not declared`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			expressions, marshalErr := common.Marshal(map[string]string{modelName: testCase.expression})
			require.NoError(t, marshalErr)
			body, marshalErr := common.Marshal(OptionUpdateRequest{
				Key:   "billing_setting.billing_expr",
				Value: string(expressions),
			})
			require.NoError(t, marshalErr)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))

			UpdateOption(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), modelName)
			assert.Contains(t, recorder.Body.String(), testCase.errorText)
		})
	}
}

func TestUpdateOptionRejectsUsageExpressionWithoutTaskPlugin(t *testing.T) {
	const modelName = "billing-save-model-without-plugin"
	expressions, err := common.Marshal(map[string]string{
		modelName: `u("mode") == "std" ? 1 : 2`,
	})
	require.NoError(t, err)
	body, err := common.Marshal(OptionUpdateRequest{
		Key:   "billing_setting.billing_expr",
		Value: string(expressions),
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(string(body)),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Contains(t, recorder.Body.String(), modelName)
	assert.Contains(t, recorder.Body.String(), "mode")
	assert.Contains(t, recorder.Body.String(), "no task plugin usage schema")
}

func TestUpdateOptionAliasBillingExprUsesPluginSchema(t *testing.T) {
	database := modelManagementDB(t, "sqlite", "")
	require.NoError(t, database.AutoMigrate(&model.Log{}))
	const pluginKey = "billing-alias-probe"
	source := `
export const meta = {
  apiVersion: 1, key: "billing-alias-probe", name: "Billing Alias Probe", version: "1.0.0", author: {name: "Test"},
  models: ["declared-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}, image_count: {type: "number", unit: "count"}},
  usageProfiles: [{models: ["declared-model"], schema: {seconds: {type: "number", unit: "second"}}}]
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	mapping := `{"alias-model":"declared-model"}`
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:           1,
		Type:         54,
		Key:          "key-1",
		Status:       common.ChannelStatusEnabled,
		Name:         "ch-1",
		Group:        "default",
		Models:       "alias-model,declared-model",
		ModelMapping: &mapping,
	}).Error)
	model.InitChannelCache()

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	putExpr := func(modelName, expression string) *httptest.ResponseRecorder {
		t.Helper()
		expressions, marshalErr := common.Marshal(map[string]string{modelName: expression})
		require.NoError(t, marshalErr)
		body, marshalErr := common.Marshal(OptionUpdateRequest{
			Key:   "billing_setting.billing_expr",
			Value: string(expressions),
		})
		require.NoError(t, marshalErr)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))
		UpdateOption(context)
		return recorder
	}

	accepted := putExpr("alias-model", `u("seconds")`)
	assert.Equal(t, http.StatusOK, accepted.Code)
	assert.Contains(t, accepted.Body.String(), `"success":true`)

	rejectedKey := putExpr("alias-model", `u("image_count")`)
	assert.Equal(t, http.StatusOK, rejectedKey.Code)
	assert.Contains(t, rejectedKey.Body.String(), `"success":false`)
	assert.Contains(t, rejectedKey.Body.String(), `usage key \"image_count\" is not declared`)

	rejectedDeclared := putExpr("declared-model", `u("image_count")`)
	assert.Contains(t, rejectedDeclared.Body.String(), `"success":false`)
	assert.Contains(t, rejectedDeclared.Body.String(), `usage key \"image_count\" is not declared`)

	unresolvable := putExpr("unknown-alias-model", `u("seconds")`)
	assert.Equal(t, http.StatusOK, unresolvable.Code)
	assert.Contains(t, unresolvable.Body.String(), `"success":false`)
	assert.Contains(t, unresolvable.Body.String(), "no task plugin usage schema")
}

// Covers configuration persistence, validation, reservation and reconciliation
// through the same public entry points used by the relay.
func TestPreConsumePolicyDatabaseMatrix(t *testing.T) {
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	previousUnit, previousBatch := common.QuotaPerUnit, common.BatchUpdateEnabled
	common.QuotaPerUnit, common.BatchUpdateEnabled = 500000, false
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(previousConfig))
		common.QuotaPerUnit, common.BatchUpdateEnabled = previousUnit, previousBatch
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.Token{}))
			for key, value := range map[string]string{
				"quota_setting.trust_quota_usd":        "10.5",
				"quota_setting.pre_consume_multiplier": "1.5",
			} {
				var response struct{ Success bool }
				modelManagementRequest(t, UpdateOption, http.MethodPut, "/api/option/", OptionUpdateRequest{Key: key, Value: value}, &response)
				require.True(t, response.Success)
				var saved model.Option
				require.NoError(t, db.Where(&model.Option{Key: key}).First(&saved).Error)
				assert.Equal(t, value, saved.Value)
			}
			var savedOptions []model.Option
			require.NoError(t, db.Find(&savedOptions).Error)
			stored := map[string]string{}
			for _, option := range savedOptions {
				stored[option.Key] = option.Value
			}
			for range 2 {
				operation_setting.GetQuotaSetting().TrustQuotaUSD = 10
				operation_setting.GetQuotaSetting().PreConsumeMultiplier = 1
				require.NoError(t, config.GlobalConfig.LoadFromDB(stored))
				assert.Equal(t, 10.5, operation_setting.GetQuotaSetting().TrustQuotaUSD)
				assert.Equal(t, 1.5, operation_setting.GetQuotaSetting().PreConsumeMultiplier)
			}
			for _, tc := range []struct{ key, value string }{
				{"trust_quota_usd", "-1"}, {"trust_quota_usd", "NaN"}, {"trust_quota_usd", "+Inf"},
				{"pre_consume_multiplier", "0"}, {"pre_consume_multiplier", "-0.5"},
				{"pre_consume_multiplier", "NaN"}, {"pre_consume_multiplier", "Inf"},
				{"pre_consume_multiplier", "1e309"}, {"pre_consume_multiplier", ""},
			} {
				key := "quota_setting." + tc.key
				var response struct{ Success bool }
				modelManagementRequest(t, UpdateOption, http.MethodPut, "/api/option/", OptionUpdateRequest{Key: key, Value: tc.value}, &response)
				assert.False(t, response.Success, "%s=%q", key, tc.value)
				var saved model.Option
				require.NoError(t, db.Where(&model.Option{Key: key}).First(&saved).Error)
				assert.Equal(t, stored[key], saved.Value, "invalid saves must preserve the previous value")
			}
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode":    `{"policy-model":"tiered_expr"}`,
				"billing_setting.billing_expr":    `{"policy-model":"tier(\"base\", p * 3 + c * 15)"}`,
				"group_ratio_setting.group_ratio": `{"default":1}`,
			}))
			cases := []struct {
				name                  string
				threshold, multiplier float64
				wallet, token         int
				force, unlimited      bool
				wantHeld              int
			}{
				{"above threshold", 10, 1, 5500000, 5500000, false, false, 0},
				{"custom threshold", 20, 1, 5500000, 5500000, false, false, 1500},
				{"zero disables bypass", 0, 1, 5500000, 5500000, false, false, 1500},
				{"wallet equals threshold", 10, 1, 5000000, 5500000, false, false, 1500},
				{"token equals threshold", 10, 1, 5500000, 5000000, false, false, 1500},
				{"fractional threshold", 10.5, 1, 5250001, 5250001, false, false, 0},
				{"unlimited token", 10, 1, 5500000, 0, false, true, 0},
				{"forced reservation", 10, 1, 5500000, 5500000, true, false, 1500},
				{"half input cost", 0, 0.5, 100000, 100000, false, false, 750},
				{"fractional multiple refunds excess", 0, 2.5, 100000, 100000, false, false, 3750},
			}
			for i, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					require.NoError(t, model.UpdateOption("quota_setting.trust_quota_usd", strconv.FormatFloat(tc.threshold, 'f', -1, 64)))
					require.NoError(t, model.UpdateOption("quota_setting.pre_consume_multiplier", strconv.FormatFloat(tc.multiplier, 'f', -1, 64)))
					user := model.User{Username: fmt.Sprintf("policy-user-%d", i), Quota: tc.wallet, Group: "default", AffCode: fmt.Sprintf("policy-aff-%d", i)}
					require.NoError(t, db.Create(&user).Error)
					token := model.Token{UserId: user.Id, Key: fmt.Sprintf("policy-token-%d", i), RemainQuota: tc.token, UnlimitedQuota: tc.unlimited}
					require.NoError(t, db.Create(&token).Error)
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					ctx.Set("token_quota", tc.token)
					info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: tc.unlimited, ForcePreConsume: tc.force, OriginModelName: "policy-model", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
					info.UserSetting.BillingPreference = "wallet_only"
					price, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 10000})
					require.NoError(t, err)
					require.Nil(t, service.PreConsumeBilling(ctx, price.QuotaToPreConsume, info))
					assert.Equal(t, tc.wantHeld, info.FinalPreConsumedQuota)
					require.NoError(t, db.First(&user, user.Id).Error)
					assert.Equal(t, tc.wallet-tc.wantHeld, user.Quota)
					if !tc.unlimited {
						require.NoError(t, db.First(&token, token.Id).Error)
						assert.Equal(t, tc.token-tc.wantHeld, token.RemainQuota)
					}
					_, actual, _ := service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100, Len: 1000})
					assert.Equal(t, 2250, actual)
					require.NoError(t, info.Billing.Settle(actual))
					require.NoError(t, info.Billing.Settle(actual))
					require.NoError(t, db.First(&user, user.Id).Error)
					assert.Equal(t, tc.wallet-actual, user.Quota)
					if !tc.unlimited {
						require.NoError(t, db.First(&token, token.Id).Error)
						assert.Equal(t, tc.token-actual, token.RemainQuota)
					}
				})
			}
		})
	}
}

// These fixtures catch cross-domain selection, fallback and request-id replay
// bugs at the real billing entry point, rather than accepting an HTTP success.
func TestXCodeFundingPolicyDatabaseMatrix(t *testing.T) {
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	previousBatch := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(previousConfig))
		common.BatchUpdateEnabled = previousBatch
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"quota_setting.trust_quota_usd": "0",
	}))
	generalTarget := model.SubscriptionFundingTarget{Scope: model.SubscriptionUsageScopeGeneral}
	preferences := []string{"wallet_only", "wallet_first", "subscription_only", "subscription_first"}
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(
				&model.User{}, &model.SubscriptionPlan{}, &model.UserSubscription{},
				&model.SubscriptionPreConsumeRecord{}, &model.XCodeAuthorization{},
				&model.Token{}, &model.UserSession{},
			))
			type fundingFixture struct {
				user          model.User
				token         model.Token
				managedToken  model.Token
				subscriptions [4]model.UserSubscription // general, designated, other xcode, unknown
				target        model.SubscriptionFundingTarget
			}
			sequence := 0
			fixture := func(t *testing.T, preference string) fundingFixture {
				t.Helper()
				sequence++
				f := fundingFixture{user: model.User{
					Username: fmt.Sprintf("funding-%d", sequence), AffCode: fmt.Sprintf("funding-%d", sequence),
					Quota: 100, Group: "default", AuthVersion: 1, Status: common.UserStatusEnabled,
				}}
				setting := f.user.GetSetting()
				setting.BillingPreference = preference
				f.user.SetSetting(setting)
				require.NoError(t, db.Create(&f.user).Error)
				plan := model.SubscriptionPlan{Title: "funding policy", TotalAmount: 100, QuotaResetPeriod: model.SubscriptionResetNever}
				require.NoError(t, db.Create(&plan).Error)
				model.InvalidateSubscriptionPlanCache(plan.Id)
				t.Cleanup(func() { model.InvalidateSubscriptionPlanCache(plan.Id) })
				now := model.GetDBTimestamp()
				// Earlier-expiring non-general instances tempt the old unscoped query.
				for i, scope := range []string{model.SubscriptionUsageScopeGeneral, model.SubscriptionUsageScopeXCode, model.SubscriptionUsageScopeXCode, "future"} {
					f.subscriptions[i] = model.UserSubscription{
						UserId: f.user.Id, PlanId: plan.Id, UsageScope: scope, Source: "admin",
						AmountTotal: 100, AmountUsed: 10, Status: "active",
						StartTime: now - 60, EndTime: now + int64(4-i)*3600,
						AllowWalletOverflow:      i == 0,
						QuotaResetPeriodSnapshot: model.SubscriptionResetNever,
					}
					require.NoError(t, db.Create(&f.subscriptions[i]).Error)
				}
				authorization := model.XCodeAuthorization{UserId: f.user.Id, SubscriptionId: f.subscriptions[1].Id, Revision: 7}
				require.NoError(t, db.Create(&authorization).Error)
				f.target = model.SubscriptionFundingTarget{
					Scope: model.SubscriptionUsageScopeXCode, SubscriptionID: f.subscriptions[1].Id, AuthorizationRevision: 7,
				}
				sid := fmt.Sprintf("funding-session-%d", sequence)
				require.NoError(t, db.Create(&model.UserSession{
					SID: sid, UserID: f.user.Id, Version: 1, UserAuthVersion: 1,
					Status: model.UserSessionStatusActive, ClientPurpose: model.ClientPurposeXCode,
					RefreshHash: strings.Repeat("a", 64), LoginMethod: "password",
					LastActiveAt: now, ExpiresAt: now + 86400,
				}).Error)
				f.token = model.Token{
					UserId: f.user.Id, Key: fmt.Sprintf("funding-native-token-%d", sequence),
					Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
					ClientPurpose: model.ClientPurposeGeneral,
				}
				require.NoError(t, db.Create(&f.token).Error)
				f.managedToken = model.Token{
					UserId: f.user.Id, Key: fmt.Sprintf("funding-managed-token-%d", sequence),
					Status: common.TokenStatusEnabled, ExpiredTime: now + 86400, UnlimitedQuota: true,
					ClientPurpose: model.ClientPurposeXCode, ClientSessionSID: sid,
				}
				require.NoError(t, db.Create(&f.managedToken).Error)
				return f
			}
			billing := func(t *testing.T, f fundingFixture, managed bool, requestID string) (*service.BillingSession, *relaycommon.RelayInfo, *types.NewAPIError) {
				t.Helper()
				target, purpose := generalTarget, model.ClientPurposeGeneral
				token := f.token
				if managed {
					target, purpose = f.target, model.ClientPurposeXCode
					token = f.managedToken
				}
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				info := &relaycommon.RelayInfo{
					UserId: f.user.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: true,
					ForcePreConsume: true, RequestId: requestID, OriginModelName: "funding-model",
					UserSetting: f.user.GetSetting(), ClientPurpose: purpose, FundingScope: target.Scope,
					FundingSubscriptionID: target.SubscriptionID, FundingAuthorizationRevision: target.AuthorizationRevision,
				}
				session, apiErr := service.NewBillingSession(ctx, info, 20)
				return session, info, apiErr
			}
			balances := func(t *testing.T, f fundingFixture, wallet int, used [4]int64) {
				t.Helper()
				var user model.User
				require.NoError(t, db.First(&user, f.user.Id).Error)
				assert.Equal(t, wallet, user.Quota, "wallet")
				assert.Equal(t, f.user.Setting, user.Setting, "billing must not rewrite the native preference")
				for i, sub := range f.subscriptions {
					var actual model.UserSubscription
					require.NoError(t, db.First(&actual, sub.Id).Error)
					assert.Equal(t, used[i], actual.AmountUsed, "instance %d (%s)", sub.Id, sub.UsageScope)
				}
			}
			for _, preference := range preferences {
				for _, managed := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/managed=%t", preference, managed), func(t *testing.T) {
						f := fixture(t, preference)
						session, info, apiErr := billing(t, f, managed, fmt.Sprintf("funding-policy-%d", sequence))
						require.Nil(t, apiErr)
						require.NotNil(t, session)
						assert.Equal(t, 20, session.GetPreConsumedQuota())
						wallet, used, picked := 100, [4]int64{10, 10, 10, 10}, 0
						if managed {
							used[1], picked = 30, f.subscriptions[1].Id
						} else if preference == "wallet_only" || preference == "wallet_first" {
							wallet = 80
						} else {
							used[0], picked = 30, f.subscriptions[0].Id
						}
						assert.Equal(t, picked, info.SubscriptionId)
						if picked == 0 {
							assert.Equal(t, service.BillingSourceWallet, info.BillingSource)
						} else {
							assert.Equal(t, service.BillingSourceSubscription, info.BillingSource)
							var record model.SubscriptionPreConsumeRecord
							require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&record).Error)
							assert.Equal(t, f.user.Id, record.UserId)
							assert.Equal(t, picked, record.UserSubscriptionId)
							assert.Equal(t, info.FundingScope, record.FundingScope)
							assert.EqualValues(t, 20, record.PreConsumed)
						}
						balances(t, f, wallet, used)
					})
				}
				t.Run(preference+"/without general", func(t *testing.T) {
					f := fixture(t, preference)
					require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", f.subscriptions[0].Id).Update("status", "cancelled").Error)
					hasGeneral, err := model.HasActiveUserSubscription(f.user.Id)
					require.NoError(t, err)
					assert.False(t, hasGeneral, "xcode and unknown scopes are not native subscriptions")
					allowOverflow, err := model.UserActiveSubscriptionsAllowWalletOverflow(f.user.Id)
					require.NoError(t, err)
					assert.True(t, allowOverflow, "xcode overflow=false cannot block native wallet fallback")
					session, info, apiErr := billing(t, f, false, fmt.Sprintf("funding-no-general-%d", sequence))
					wallet := 80
					if preference == "subscription_only" {
						require.NotNil(t, apiErr)
						require.Nil(t, session)
						wallet = 100
					} else {
						require.Nil(t, apiErr)
						require.NotNil(t, session)
						assert.Equal(t, service.BillingSourceWallet, info.BillingSource)
						assert.Zero(t, info.SubscriptionId)
					}
					balances(t, f, wallet, [4]int64{10, 10, 10, 10})
				})
				t.Run(preference+"/managed depleted", func(t *testing.T) {
					f := fixture(t, preference)
					require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", f.subscriptions[1].Id).Update("amount_used", 90).Error)
					session, _, apiErr := billing(t, f, true, fmt.Sprintf("funding-depleted-%d", sequence))
					require.Nil(t, session)
					require.NotNil(t, apiErr)
					assert.Equal(t, types.ErrorCode("XCODE_QUOTA_EXHAUSTED"), apiErr.GetErrorCode())
					assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
					balances(t, f, 100, [4]int64{10, 90, 10, 10})
				})
			}
			t.Run("managed HTTP protocol errors", func(t *testing.T) {
				f := fixture(t, "wallet_only")
				require.NoError(t, db.Model(&f.subscriptions[1]).Update("amount_used", 100).Error)
				key := fmt.Sprintf("fundinghttp%d", sequence)
				require.NoError(t, db.Model(&f.managedToken).Update("key", key).Error)
				previousConfig := config.GlobalConfig.ExportAllConfigs()
				previousRatios := ratio_setting.ModelRatio2JSONString()
				previousCountToken := constant.CountToken
				constant.CountToken = false
				t.Cleanup(func() {
					constant.CountToken = previousCountToken
					require.NoError(t, config.GlobalConfig.LoadFromDB(previousConfig))
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
				})
				require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
					"group_ratio_setting.group_ratio": `{"default":1}`,
				}))
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"xcode-protocol-claude":1,"xcode-protocol-chat":1,"xcode-protocol-responses":1}`))
				for i, protocol := range []struct {
					path    string
					format  types.RelayFormat
					modelID string
					apiType string
					body    string
				}{
					{"/v1/messages", types.RelayFormatClaude, "xcode-protocol-claude", "anthropic-messages", `{"model":"xcode-protocol-claude","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`},
					{"/v1/chat/completions", types.RelayFormatOpenAI, "xcode-protocol-chat", "openai-chat-completions", `{"model":"xcode-protocol-chat","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`},
					{"/v1/responses", types.RelayFormatOpenAIResponses, "xcode-protocol-responses", "openai-responses", `{"model":"xcode-protocol-responses","max_output_tokens":1,"input":"hello"}`},
				} {
					channel := model.Channel{Id: i + 1, Name: protocol.modelID, Type: 1, Key: "fixture-upstream", Status: common.ChannelStatusEnabled, Group: "default", Models: protocol.modelID}
					require.NoError(t, db.Create(&channel).Error)
					require.NoError(t, db.Create(&model.Ability{Group: "default", Model: protocol.modelID, ChannelId: channel.Id, Enabled: true}).Error)
					require.NoError(t, db.Create(&model.Model{ModelName: protocol.modelID, NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(xcodeModelConfigFixture(protocol.apiType))}).Error)
					router := gin.New()
					router.POST(protocol.path, middleware.TokenAuth(), func(c *gin.Context) {
						common.SetContextKey(c, constant.ContextKeyOriginalModel, protocol.modelID)
						Relay(c, protocol.format)
					})
					request := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(protocol.body))
					request.Header.Set("Authorization", "Bearer sk-"+key)
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					assert.Equal(t, http.StatusTooManyRequests, response.Code, response.Body.String())
					var body struct {
						Type  string `json:"type"`
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
					assert.Equal(t, "XCODE_QUOTA_EXHAUSTED", body.Error.Code, "all supported protocols preserve the canonical funding code")
					if protocol.format == types.RelayFormatClaude {
						assert.Equal(t, "error", body.Type)
					}
					balances(t, f, 100, [4]int64{10, 100, 10, 10})
				}
			})
			for _, preference := range []string{"wallet_first", "subscription_first"} {
				t.Run(preference+"/native fallback", func(t *testing.T) {
					f := fixture(t, preference)
					wallet, used := 100, [4]int64{90, 10, 10, 10}
					if preference == "wallet_first" {
						require.NoError(t, db.Model(&model.User{}).Where("id = ?", f.user.Id).Update("quota", 5).Error)
						wallet, used = 5, [4]int64{30, 10, 10, 10}
					} else {
						require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", f.subscriptions[0].Id).Update("amount_used", 90).Error)
						wallet = 80
					}
					session, info, apiErr := billing(t, f, false, fmt.Sprintf("funding-fallback-%d", sequence))
					require.Nil(t, apiErr)
					require.NotNil(t, session)
					if preference == "wallet_first" {
						assert.Equal(t, f.subscriptions[0].Id, info.SubscriptionId)
					} else {
						assert.Zero(t, info.SubscriptionId)
						assert.Equal(t, service.BillingSourceWallet, info.BillingSource)
					}
					balances(t, f, wallet, used)
				})
			}
			for _, failure := range []string{"unassigned", "revision", "other instance"} {
				t.Run("managed/"+failure, func(t *testing.T) {
					f := fixture(t, "wallet_only")
					code := types.ErrorCode("XCODE_AUTHORIZATION_CONFLICT")
					status := http.StatusConflict
					switch failure {
					case "unassigned":
						require.NoError(t, db.Where("user_id = ?", f.user.Id).Delete(&model.XCodeAuthorization{}).Error)
						code = types.ErrorCode("XCODE_PLAN_UNASSIGNED")
						status = http.StatusForbidden
					case "revision":
						require.NoError(t, db.Model(&model.XCodeAuthorization{}).Where("user_id = ?", f.user.Id).Update("revision", 8).Error)
					case "other instance":
						f.target.SubscriptionID = f.subscriptions[2].Id
					}
					session, _, apiErr := billing(t, f, true, fmt.Sprintf("funding-auth-%d", sequence))
					require.Nil(t, session)
					require.NotNil(t, apiErr)
					assert.Equal(t, code, apiErr.GetErrorCode())
					assert.Equal(t, status, apiErr.StatusCode)
					balances(t, f, 100, [4]int64{10, 10, 10, 10})
				})
			}
			t.Run("model rejects unspecified funding scope", func(t *testing.T) {
				f := fixture(t, "subscription_only")
				for i, target := range []model.SubscriptionFundingTarget{{}, {Scope: "future"}} {
					result, err := model.PreConsumeUserSubscription(fmt.Sprintf("funding-invalid-%d-%d", sequence, i), f.user.Id, "funding-model", 0, 20, target)
					require.ErrorIs(t, err, model.ErrSubscriptionFundingTargetInvalid)
					assert.Nil(t, result)
				}
				balances(t, f, 100, [4]int64{10, 10, 10, 10})
			})
			for _, firstManaged := range []bool{false, true} {
				t.Run(fmt.Sprintf("replay/first managed=%t", firstManaged), func(t *testing.T) {
					f := fixture(t, "subscription_first")
					requestID := fmt.Sprintf("funding-replay-%d", sequence)
					firstTarget, picked := generalTarget, f.subscriptions[0].Id
					used := [4]int64{30, 10, 10, 10}
					if firstManaged {
						firstTarget, picked = f.target, f.subscriptions[1].Id
						used = [4]int64{10, 30, 10, 10}
					}
					for range 2 {
						result, err := model.PreConsumeUserSubscription(requestID, f.user.Id, "funding-model", 0, 20, firstTarget)
						require.NoError(t, err)
						require.NotNil(t, result)
						assert.Equal(t, picked, result.UserSubscriptionId)
						assert.EqualValues(t, 20, result.PreConsumed)
					}
					balances(t, f, 100, used)
					otherTarget := f.target
					if firstManaged {
						otherTarget = generalTarget
					}
					result, err := model.PreConsumeUserSubscription(requestID, f.user.Id, "funding-model", 0, 20, otherTarget)
					require.ErrorIs(t, err, model.ErrSubscriptionRequestConflict)
					assert.Nil(t, result)
					// Native subscription_first must not turn a replay conflict into wallet fallback.
					session, _, apiErr := billing(t, f, !firstManaged, requestID)
					require.Nil(t, session)
					require.NotNil(t, apiErr)
					balances(t, f, 100, used)
					var count int64
					require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Where("request_id = ?", requestID).Count(&count).Error)
					assert.EqualValues(t, 1, count)
					if firstManaged {
						require.NoError(t, db.Model(&model.XCodeAuthorization{}).Where("user_id = ?", f.user.Id).Updates(map[string]any{
							"subscription_id": f.subscriptions[2].Id, "revision": 8,
						}).Error)
						newTarget := model.SubscriptionFundingTarget{Scope: model.SubscriptionUsageScopeXCode, SubscriptionID: f.subscriptions[2].Id, AuthorizationRevision: 8}
						result, err = model.PreConsumeUserSubscription(requestID, f.user.Id, "funding-model", 0, 20, newTarget)
						require.ErrorIs(t, err, model.ErrSubscriptionRequestConflict)
						assert.Nil(t, result)
						balances(t, f, 100, used)
					}
					other := fixture(t, "subscription_only")
					result, err = model.PreConsumeUserSubscription(requestID, other.user.Id, "funding-model", 0, 20, generalTarget)
					require.ErrorIs(t, err, model.ErrSubscriptionRequestConflict)
					assert.Nil(t, result)
					balances(t, other, 100, [4]int64{10, 10, 10, 10})
				})
			}
		})
	}
}

func TestXCodeRequestLifecycleDatabaseMatrix(t *testing.T) {
	previousBatch := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	t.Cleanup(func() { common.BatchUpdateEnabled = previousBatch })
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.SubscriptionPlan{},
				&model.UserSubscription{}, &model.XCodeAuthorization{}, &model.SubscriptionPreConsumeRecord{}))
			sequence := 0
			newRequest := func(t *testing.T) (*service.BillingSession, *relaycommon.RelayInfo, model.UserSubscription) {
				t.Helper()
				sequence++
				user := model.User{Username: fmt.Sprintf("lifecycle%d", sequence), AffCode: fmt.Sprintf("lifecycle%d", sequence),
					Quota: 900, Status: common.UserStatusEnabled, Group: "default"}
				setting := user.GetSetting()
				setting.BillingPreference = "wallet_only"
				user.SetSetting(setting)
				require.NoError(t, db.Create(&user).Error)
				token := model.Token{UserId: user.Id, Key: fmt.Sprintf("lifecyclekey%d", sequence),
					Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000}
				require.NoError(t, db.Create(&token).Error)
				plan := model.SubscriptionPlan{Title: "request ledger", TotalAmount: 1000, QuotaResetPeriod: model.SubscriptionResetNever}
				require.NoError(t, db.Create(&plan).Error)
				model.InvalidateSubscriptionPlanCache(plan.Id)
				t.Cleanup(func() { model.InvalidateSubscriptionPlanCache(plan.Id) })
				now := model.GetDBTimestamp()
				sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, Source: "admin",
					UsageScope: model.SubscriptionUsageScopeXCode, Status: "active", AmountTotal: 1000,
					StartTime: now - 60, EndTime: now + 3600, QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
				require.NoError(t, db.Create(&sub).Error)
				require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				info := &relaycommon.RelayInfo{UserId: user.Id, UserSetting: setting, TokenId: token.Id, TokenKey: token.Key,
					RequestId: fmt.Sprintf("lifecycle%d", sequence), OriginModelName: "ledger-model", ForcePreConsume: true,
					ClientPurpose: model.ClientPurposeXCode, FundingScope: model.SubscriptionUsageScopeXCode,
					FundingSubscriptionID: sub.Id, FundingAuthorizationRevision: 1}
				session, apiErr := service.NewBillingSession(ctx, info, 100)
				require.Nil(t, apiErr)
				info.Billing = session
				return session, info, sub
			}
			assertState := func(t *testing.T, info *relaycommon.RelayInfo, used, reserved, final int64, status string) model.SubscriptionPreConsumeRecord {
				t.Helper()
				var sub model.UserSubscription
				var record model.SubscriptionPreConsumeRecord
				var user model.User
				require.NoError(t, db.First(&sub, info.SubscriptionId).Error)
				require.NoError(t, db.Where("request_id = ?", info.RequestId).First(&record).Error)
				require.NoError(t, db.First(&user, info.UserId).Error)
				assert.EqualValues(t, 900, user.Quota, "managed lifecycle never uses wallet")
				assert.Equal(t, used, sub.AmountUsed)
				assert.Equal(t, reserved, record.CycleReservedQuota)
				assert.Equal(t, final, record.FinalQuota)
				assert.Equal(t, status, record.Status)
				assert.Equal(t, sub.QuotaResetGeneration, record.ResetGeneration)
				return record
			}
			reset := func(t *testing.T, sub model.UserSubscription) {
				t.Helper()
				_, err := model.AdminResetUserSubscriptionsByPlan(sub.UserId, sub.PlanId, false)
				require.NoError(t, err)
				var actual model.UserSubscription
				require.NoError(t, db.First(&actual, sub.Id).Error)
				assert.Equal(t, sub.QuotaResetGeneration+1, actual.QuotaResetGeneration)
				assert.Zero(t, actual.AmountUsed)
			}
			t.Run("free request creates a zero funding fact without occupying quota", func(t *testing.T) {
				initial, info, _ := newRequest(t)
				require.NoError(t, initial.Settle(0))
				info.RequestId += "-zero"
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				session, apiErr := service.NewBillingSession(ctx, info, 0)
				require.Nil(t, apiErr)
				info.Billing = session
				assertState(t, info, 0, 0, 0, "consumed")
				var token model.Token
				require.NoError(t, db.First(&token, info.TokenId).Error)
				assert.Equal(t, 1000, token.RemainQuota)
				assert.True(t, session.NeedsRefund(), "failed free requests must finalize their pending funding fact")
				require.NoError(t, session.Settle(0))
				assertState(t, info, 0, 0, 0, "settled")
				assert.False(t, session.NeedsRefund())
				require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", info.SubscriptionId).Updates(map[string]any{"amount_total": 1, "amount_used": 1}).Error)
				info.RequestId += "-exhausted"
				exhausted, apiErr := service.NewBillingSession(ctx, info, 0)
				assert.Nil(t, exhausted)
				require.NotNil(t, apiErr)
				assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
				assert.Equal(t, types.ErrorCode("XCODE_QUOTA_EXHAUSTED"), apiErr.GetErrorCode())
			})
			for _, actual := range []int{100, 150} {
				t.Run(fmt.Sprintf("actual%d across reset", actual), func(t *testing.T) {
					session, info, sub := newRequest(t)
					reset(t, sub)
					require.NoError(t, session.Settle(actual))
					require.NoError(t, session.Settle(actual))
					require.NoError(t, model.SettleXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id, int64(actual)))
					require.NoError(t, model.RefundXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id))
					assertState(t, info, int64(actual), int64(actual), int64(actual), "settled")
					require.ErrorIs(t, model.SettleXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id, int64(actual+1)), model.ErrSubscriptionRequestConflict)
					var token model.Token
					require.NoError(t, db.First(&token, info.TokenId).Error)
					assert.Equal(t, 1000-actual, token.RemainQuota)
					assert.Equal(t, actual, token.UsedQuota)
				})
			}
			t.Run("refund old occupancy preserves new request", func(t *testing.T) {
				_, info, sub := newRequest(t)
				reset(t, sub)
				target := model.SubscriptionFundingTarget{Scope: model.SubscriptionUsageScopeXCode, SubscriptionID: sub.Id, AuthorizationRevision: 1}
				_, err := model.PreConsumeUserSubscription(info.RequestId+"other", info.UserId, "ledger-model", 0, 40, target)
				require.NoError(t, err)
				require.NoError(t, model.RefundXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id))
				require.NoError(t, model.RefundXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id))
				assertState(t, info, 40, 0, 0, "refunded")
				require.ErrorIs(t, model.SettleXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id, 100), model.ErrSubscriptionRequestConflict)
			})
			t.Run("smaller current cycle reserve retains token reservation", func(t *testing.T) {
				session, info, sub := newRequest(t)
				reset(t, sub)
				require.NoError(t, session.Reserve(60))
				require.NoError(t, session.Reserve(60))
				assertState(t, info, 60, 60, 0, "consumed")
				assert.Equal(t, 100, session.GetPreConsumedQuota())
				var token model.Token
				require.NoError(t, db.First(&token, info.TokenId).Error)
				assert.Equal(t, 900, token.RemainQuota)
				require.NoError(t, session.Settle(80))
				assertState(t, info, 80, 80, 80, "settled")
				require.NoError(t, db.First(&token, info.TokenId).Error)
				assert.Equal(t, 920, token.RemainQuota)
			})
			for _, crossReset := range []bool{false, true} {
				t.Run(fmt.Sprintf("rollback generation changed=%t", crossReset), func(t *testing.T) {
					_, info, sub := newRequest(t)
					mutation, err := model.ReserveXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id, 150)
					require.NoError(t, err)
					if crossReset {
						reset(t, sub)
						target := model.SubscriptionFundingTarget{Scope: model.SubscriptionUsageScopeXCode, SubscriptionID: sub.Id, AuthorizationRevision: 1}
						_, err = model.PreConsumeUserSubscription(info.RequestId+"other", info.UserId, "ledger-model", 0, 40, target)
						require.NoError(t, err)
					}
					require.NoError(t, model.RollbackXCodeSubscriptionReserve(info.RequestId, info.UserId, sub.Id, mutation))
					if crossReset {
						var actual model.UserSubscription
						require.NoError(t, db.First(&actual, sub.Id).Error)
						assert.EqualValues(t, 40, actual.AmountUsed)
						require.NoError(t, model.RefundXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id))
						assertState(t, info, 40, 0, 0, "refunded")
					} else {
						assertState(t, info, 100, 100, 0, "consumed")
					}
				})
			}
			t.Run("failed token reserve rolls back only this request", func(t *testing.T) {
				session, info, sub := newRequest(t)
				require.NoError(t, db.Model(&model.Token{}).Where("id = ?", info.TokenId).Update("remain_quota", 0).Error)
				require.Error(t, session.Reserve(150))
				assertState(t, info, 100, 100, 0, "consumed")
				assert.Equal(t, 100, session.GetPreConsumedQuota())
				require.NoError(t, model.RefundXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id))
				assertState(t, info, 0, 0, 0, "refunded")
			})
			for _, closed := range []string{"cancelled", "expired"} {
				t.Run("original instance settles after "+closed, func(t *testing.T) {
					session, info, sub := newRequest(t)
					replacement := sub
					replacement.Id, replacement.AmountUsed = 0, 0
					require.NoError(t, db.Create(&replacement).Error)
					require.NoError(t, db.Model(&model.XCodeAuthorization{}).Where("user_id = ?", info.UserId).
						Updates(map[string]any{"subscription_id": replacement.Id, "revision": 2}).Error)
					now := model.GetDBTimestamp()
					require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).Updates(map[string]any{
						"status": closed, "end_time": now - 1, "last_reset_time": now - 120, "next_reset_time": now - 60,
						"quota_reset_period_snapshot": model.SubscriptionResetCustom, "quota_reset_custom_seconds_snapshot": 60,
					}).Error)
					require.NoError(t, session.Settle(150))
					record := assertState(t, info, 150, 150, 150, "settled")
					assert.Equal(t, sub.QuotaResetGeneration, record.ResetGeneration, "closed instance cannot create a new cycle")
					require.NoError(t, db.First(&replacement, replacement.Id).Error)
					assert.Zero(t, replacement.AmountUsed)
				})
			}
			t.Run("streamed actual usage exposes failed funding in consume audit", func(t *testing.T) {
				_, info, sub := newRequest(t)
				require.NoError(t, db.AutoMigrate(&model.Log{}))
				previousLogs := common.LogConsumeEnabled
				common.LogConsumeEnabled = true
				t.Cleanup(func() { common.LogConsumeEnabled = previousLogs })
				channel := model.Channel{Name: "ledger audit", Key: "unused", Status: common.ChannelStatusEnabled}
				require.NoError(t, db.Create(&channel).Error)
				info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channel.Id}
				info.IsStream = true
				info.PriceData.ModelRatio = 1
				info.PriceData.CompletionRatio = 1
				info.PriceData.GroupRatioInfo.GroupRatio = 1
				require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).Update("amount_total", 120).Error)
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				ctx.Set(common.RequestIdKey, info.RequestId)
				service.PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 150, TotalTokens: 150}, nil)
				assertState(t, info, 100, 100, 0, "consumed")
				var log model.Log
				require.NoError(t, model.LOG_DB.Where("request_id = ?", info.RequestId).First(&log).Error)
				assert.Zero(t, log.Quota, "failed funding is not a paid charge")
				assert.True(t, log.IsStream)
				assert.Contains(t, log.Content, "Billing settlement failed")
				var other map[string]any
				require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
				admin, ok := other["admin_info"].(map[string]any)
				require.True(t, ok)
				failure, ok := admin["billing_settlement"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "failed", failure["status"])
				assert.Equal(t, "subscription_quota_insufficient", failure["code"])
				assert.Equal(t, float64(150), failure["actual_quota"])
				assert.Equal(t, false, failure["funding_applied"])
				var user model.User
				require.NoError(t, db.First(&user, info.UserId).Error)
				assert.Zero(t, user.UsedQuota)
				require.NoError(t, db.First(&channel, channel.Id).Error)
				assert.Zero(t, channel.UsedQuota)
			})
			t.Run("insufficient settlement leaves recoverable atomic fact", func(t *testing.T) {
				session, info, sub := newRequest(t)
				require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).Update("amount_total", 120).Error)
				require.Error(t, session.Settle(150))
				assertState(t, info, 100, 100, 0, "consumed")
				require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).Update("amount_total", 200).Error)
				require.NoError(t, session.Settle(150))
				assertState(t, info, 150, 150, 150, "settled")
			})
			t.Run("legacy and malformed mutation cannot bypass ledger", func(t *testing.T) {
				session, info, sub := newRequest(t)
				for _, delta := range []int64{0, 10, -10} {
					require.Error(t, model.PostConsumeUserSubscriptionDelta(sub.Id, delta))
				}
				require.Error(t, model.RefundSubscriptionPreConsume(info.RequestId))
				require.Error(t, service.PostConsumeQuota(info, 0, 100, false))
				info.Billing = nil
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				require.Error(t, service.SettleBilling(ctx, info, 100))
				require.Error(t, session.Settle(-1))
				require.Error(t, session.Reserve(-1))
				require.Error(t, model.SettleXCodeSubscriptionRequest(info.RequestId, info.UserId+1, sub.Id, 10))
				require.Error(t, model.RefundXCodeSubscriptionRequest(info.RequestId, info.UserId, sub.Id+1))
				assertState(t, info, 100, 100, 0, "consumed")
			})
			t.Run("cleanup retains unfinished managed fact", func(t *testing.T) {
				session, info, sub := newRequest(t)
				now := model.GetDBTimestamp()
				require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Where("request_id = ?", info.RequestId).
					UpdateColumn("updated_at", now-10*24*3600).Error)
				_, err := model.CleanupSubscriptionPreConsumeRecords(7 * 24 * 3600)
				require.NoError(t, err)
				assertState(t, info, 100, 100, 0, "consumed")
				require.NoError(t, session.Settle(100))
				require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Where("request_id = ?", info.RequestId).
					UpdateColumn("updated_at", now-10*24*3600).Error)
				_, err = model.CleanupSubscriptionPreConsumeRecords(7 * 24 * 3600)
				require.NoError(t, err)
				var record model.SubscriptionPreConsumeRecord
				query := db.Where("request_id = ?", info.RequestId).Find(&record)
				require.NoError(t, query.Error)
				assert.Zero(t, query.RowsAffected)
				require.NoError(t, db.First(&sub, sub.Id).Error)
				assert.EqualValues(t, 100, sub.AmountUsed)
			})
		})
	}
}

func TestXCodeAdministratorAuthorizationDatabaseMatrix(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(
				&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.XCodeAuthorization{},
				&model.SubscriptionPreConsumeRecord{}, &model.UserSession{}, &model.UserAccessToken{},
				&model.SubscriptionOrder{}, &model.Task{},
			))
			connection, err := db.DB()
			require.NoError(t, err)
			connection.SetMaxOpenConns(1) // Creation must obtain its clock inside this transaction.
			admin := model.User{Username: "xcode-admin", AffCode: "xcode-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default"}
			user := model.User{Username: "xcode-target", AffCode: "xcode-target", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Group: "default", Quota: 1000}
			require.NoError(t, db.Create(&admin).Error)
			require.NoError(t, db.Create(&user).Error)
			_, adminJWT := createAccessTokenTestSession(t, admin.Id, "xcode-admin-session")
			_, userJWT := createAccessTokenTestSession(t, user.Id, "xcode-target-session")
			readPAT, _ := createScopedAccessToken(t, admin.Id, model.GetDBTimestamp()+3600, "billing:read")
			writePAT, _ := createScopedAccessToken(t, admin.Id, model.GetDBTimestamp()+3600, "billing:write")
			router := gin.New()
			router.Use(middleware.RequestId())
			group := router.Group("/api/xcode/v1/admin", middleware.AdminAuth(), middleware.DisableCache())
			group.GET("/users/:id/authorization", AdminGetXCodeAuthorization)
			group.POST("/users/:id/authorization", AdminMutateXCodeAuthorization)
			router.POST("/api/subscription/admin/user_subscriptions/:id/invalidate", middleware.AdminAuth(), AdminInvalidateUserSubscription)
			router.DELETE("/api/subscription/admin/user_subscriptions/:id", middleware.AdminAuth(), AdminDeleteUserSubscription)
			path := fmt.Sprintf("/api/xcode/v1/admin/users/%d/authorization", user.Id)
			type response struct {
				Success bool                                 `json:"success"`
				Code    string                               `json:"code"`
				Data    *xcodeDTO.XCodeAuthorizationSnapshot `json:"data"`
			}
			request := func(method, target, credential string, input any) (*httptest.ResponseRecorder, response) {
				t.Helper()
				body, err := common.Marshal(input)
				require.NoError(t, err)
				recorder := accessTokenRequest(router, method, target, credential, "", string(body))
				var output response
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output), recorder.Body.String())
				return recorder, output
			}
			mutate := func(action, revision string, planID int, advance *bool) (*httptest.ResponseRecorder, response) {
				t.Helper()
				input := xcodeDTO.XCodeAuthorizationMutation{Action: action, ExpectedRevision: revision, AdvanceResetTime: advance}
				if planID > 0 {
					input.PlanID = strconv.Itoa(planID)
				}
				return request(http.MethodPost, path, adminJWT, input)
			}
			read := func() *xcodeDTO.XCodeAuthorizationSnapshot {
				t.Helper()
				recorder, output := request(http.MethodGet, path, adminJWT, nil)
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				require.True(t, output.Success)
				require.NotNil(t, output.Data)
				return output.Data
			}
			planA := model.SubscriptionPlan{Title: "XCode A", Enabled: true, DurationUnit: model.SubscriptionDurationCustom,
				CustomSeconds: 7200, TotalAmount: 100, QuotaResetPeriod: model.SubscriptionResetCustom, QuotaResetCustomSeconds: 3600}
			planB := model.SubscriptionPlan{Title: "XCode B", Enabled: true, DurationUnit: model.SubscriptionDurationCustom,
				CustomSeconds: 7200, TotalAmount: 300, QuotaResetPeriod: model.SubscriptionResetNever}
			require.NoError(t, db.Create(&planA).Error)
			require.NoError(t, db.Create(&planB).Error)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				_, err := model.CreateUserSubscriptionFromPlanTx(tx, user.Id, &planA, "order", model.SubscriptionUsageScopeGeneral)
				return err
			}))
			var general model.UserSubscription
			require.NoError(t, db.Where("user_id = ? AND usage_scope = ?", user.Id, model.SubscriptionUsageScopeGeneral).First(&general).Error)
			require.NoError(t, db.Model(&general).Update("amount_used", 25).Error)
			assert.Equal(t, "unassigned", read().Status)
			assert.Equal(t, "0", read().AuthorizationRevision)
			recorder, _ := request(http.MethodGet, path, userJWT, nil)
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			recorder, _ = request(http.MethodPost, path, readPAT, xcodeDTO.XCodeAuthorizationMutation{Action: "grant", ExpectedRevision: "0", PlanID: strconv.Itoa(planA.Id)})
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			recorder, _ = request(http.MethodGet, path, writePAT, nil)
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			recorder, output := request(http.MethodGet, path, readPAT, nil)
			require.Equal(t, http.StatusOK, recorder.Code)
			var samePlanGeneral *model.UserSubscription
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var err error
				samePlanGeneral, err = model.CreateUserSubscriptionFromPlanTx(tx, user.Id, &planB, "order", model.SubscriptionUsageScopeGeneral)
				return err
			}))
			require.NoError(t, db.Model(samePlanGeneral).Update("amount_used", 35).Error)
			assert.Equal(t, "unassigned", output.Data.Status)
			operation_setting.GetPaymentSetting().ComplianceConfirmed = false
			recorder, output = mutate("grant", "0", planA.Id, nil)
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.False(t, output.Success)
			operation_setting.GetPaymentSetting().ComplianceConfirmed = true
			require.NoError(t, db.Model(&planA).Update("max_purchase_per_user", 1).Error)
			recorder, output = mutate("grant", "0", planA.Id, nil)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Equal(t, "XCODE_PLAN_CONFIG_INVALID", output.Code)
			require.NoError(t, db.Model(&planA).Update("max_purchase_per_user", 0).Error)
			recorder, output = mutate("grant", "0", planA.Id, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.True(t, output.Success)
			assert.Equal(t, "1", output.Data.AuthorizationRevision)
			assert.Equal(t, "active", output.Data.Status)
			assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
			var first model.UserSubscription
			firstID, err := strconv.Atoi(output.Data.Subscription.ID)
			require.NoError(t, err)
			require.NoError(t, db.First(&first, firstID).Error)
			assert.Equal(t, "admin", first.Source)
			assert.False(t, first.AllowWalletOverflow)
			assert.Equal(t, int64(1), first.QuotaResetGeneration)
			assert.Equal(t, first.StartTime*1000, *output.Data.Subscription.StartTime)
			assert.Equal(t, first.NextResetTime*1000, *output.Data.Quota.NextResetTime)
			target := model.SubscriptionFundingTarget{Scope: model.SubscriptionUsageScopeXCode, SubscriptionID: first.Id, AuthorizationRevision: 1}
			_, err = model.PreConsumeUserSubscription("xcode-admin-inflight", user.Id, "model", 0, 20, target)
			require.NoError(t, err)
			require.NoError(t, db.Model(&planA).Updates(map[string]any{"total_amount": 999, "quota_reset_period": model.SubscriptionResetNever}).Error)
			recorder, output = mutate("renew", "1", planA.Id, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, strconv.Itoa(first.Id), output.Data.Subscription.ID)
			assert.Equal(t, "100", *output.Data.Quota.Total)
			assert.Equal(t, "20", output.Data.Quota.Used)
			assert.Equal(t, model.SubscriptionResetCustom, output.Data.Quota.ResetPeriod)
			assert.Equal(t, (first.EndTime+planA.CustomSeconds)*1000, *output.Data.Subscription.EndTime)
			assert.Equal(t, first.LastResetTime*1000, *output.Data.Quota.LastResetTime)
			assert.Equal(t, first.NextResetTime*1000, *output.Data.Quota.NextResetTime)
			recorder, output = mutate("renew", "1", planA.Id, nil)
			assert.Equal(t, http.StatusConflict, recorder.Code)
			assert.Equal(t, "XCODE_AUTHORIZATION_CONFLICT", output.Code)
			recorder, output = mutate("replace", "2", planB.Id, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			secondID, err := strconv.Atoi(output.Data.Subscription.ID)
			require.NoError(t, err)
			assert.NotEqual(t, first.Id, secondID)
			assert.Equal(t, "0", output.Data.Quota.Used)
			assert.Equal(t, "300", *output.Data.Quota.Total)
			require.NoError(t, model.SettleXCodeSubscriptionRequest("xcode-admin-inflight", user.Id, first.Id, 30))
			require.NoError(t, db.First(&first, first.Id).Error)
			assert.Equal(t, "cancelled", first.Status)
			assert.Equal(t, int64(30), first.AmountUsed)
			require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", secondID).Update("amount_used", 44).Error)
			advance := false
			recorder, output = mutate("reset", "3", 0, &advance)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "0", output.Data.Quota.Used)
			assert.Equal(t, "4", output.Data.AuthorizationRevision)
			require.NoError(t, db.First(&general, general.Id).Error)
			assert.Equal(t, int64(25), general.AmountUsed, "designated reset cannot reset a same-template general instance")
			require.NoError(t, db.First(samePlanGeneral, samePlanGeneral.Id).Error)
			assert.Equal(t, int64(35), samePlanGeneral.AmountUsed, "designated reset cannot reset a same-template general instance")
			recorder, _ = request(http.MethodDelete, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d", first.Id), adminJWT, nil)
			assert.Equal(t, http.StatusConflict, recorder.Code, "historical settled facts prohibit deleting the original instance")
			recorder, _ = request(http.MethodDelete, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d", secondID), adminJWT, nil)
			assert.Equal(t, http.StatusConflict, recorder.Code, "designation prohibits deleting the current instance")
			recorder, output = mutate("revoke", "4", 0, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "revoked", output.Data.Status)
			recorder, _ = mutate("renew", "5", planB.Id, nil)
			assert.Equal(t, http.StatusConflict, recorder.Code, "renew cannot revive cancellation")
			recorder, output = mutate("grant", "5", planB.Id, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			thirdID, err := strconv.Atoi(output.Data.Subscription.ID)
			require.NoError(t, err)
			assert.NotEqual(t, secondID, thirdID)
			require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", thirdID).Updates(map[string]any{"end_time": model.GetDBTimestamp() - 1, "amount_used": 77}).Error)
			recorder, output = mutate("renew", "6", planB.Id, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			fourthID, err := strconv.Atoi(output.Data.Subscription.ID)
			require.NoError(t, err)
			assert.NotEqual(t, thirdID, fourthID)
			assert.Equal(t, "0", output.Data.Quota.Used)
			connection.SetMaxOpenConns(4)
			start := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			var concurrent sync.WaitGroup
			for range 2 {
				concurrent.Go(func() {
					<-start
					results <- accessTokenRequest(router, http.MethodPost, path, adminJWT, "", fmt.Sprintf(`{"action":"renew","expected_revision":"7","plan_id":"%d"}`, planB.Id))
				})
			}
			close(start)
			concurrent.Wait()
			close(results)
			statuses := map[int]int{}
			for result := range results {
				statuses[result.Code]++
			}
			assert.Equal(t, map[int]int{http.StatusOK: 1, http.StatusConflict: 1}, statuses)
			var fourth model.UserSubscription
			require.NoError(t, db.First(&fourth, fourthID).Error)
			assert.Equal(t, "8", read().AuthorizationRevision)
			beforeCancel := fourth.EndTime
			_, err = model.AdminInvalidateUserSubscription(first.Id)
			require.NoError(t, err)
			assert.Equal(t, "8", read().AuthorizationRevision, "cancelling history cannot revise the current designation")
			_, err = model.AdminInvalidateUserSubscription(fourth.Id)
			require.NoError(t, err)
			assert.Equal(t, "9", read().AuthorizationRevision)
			assert.Equal(t, "revoked", read().Status)
			assert.Greater(t, beforeCancel, model.GetDBTimestamp())
			require.NoError(t, db.Model(&planB).Update("total_amount", int64(9007199254740993)).Error)
			recorder, output = mutate("grant", "9", planB.Id, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "9007199254740993", *output.Data.Quota.Total, "quota facts retain precision above JavaScript's safe integer")
			recorder, _ = mutate("revoke", "10", 0, nil)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.NoError(t, db.Model(&planB).Update("total_amount", 0).Error)
			recorder, output = mutate("grant", "11", planB.Id, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.True(t, output.Data.Quota.Unlimited)
			assert.Nil(t, output.Data.Quota.Total)
			assert.Nil(t, output.Data.Quota.Remaining)
			assert.Nil(t, output.Data.Quota.UsedPercentage)
			recorder, output = request(http.MethodPost, path, writePAT, xcodeDTO.XCodeAuthorizationMutation{Action: "revoke", ExpectedRevision: "12"})
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "13", output.Data.AuthorizationRevision)
			var latestUser model.User
			require.NoError(t, db.First(&latestUser, user.Id).Error)
			assert.Equal(t, 1000, latestUser.Quota)
			var orders int64
			require.NoError(t, db.Model(&model.SubscriptionOrder{}).Where("user_id = ?", user.Id).Count(&orders).Error)
			assert.Zero(t, orders, "manual authorization never fabricates a payment order")
			boundaryPlan := model.SubscriptionPlan{Title: "Cancellation boundary", Enabled: true,
				DurationUnit: model.SubscriptionDurationCustom, CustomSeconds: 7200, TotalAmount: 100,
				QuotaResetPeriod: model.SubscriptionResetCustom, QuotaResetCustomSeconds: 60}
			require.NoError(t, db.Create(&boundaryPlan).Error)
			for _, action := range []string{"revoke", "replace", "native"} {
				t.Run("due-reset-before-"+action, func(t *testing.T) {
					boundaryUser := model.User{Username: "boundary-" + action, AffCode: "boundary-" + action, Group: "default", AuthVersion: 1}
					require.NoError(t, db.Create(&boundaryUser).Error)
					targetPath := fmt.Sprintf("/api/xcode/v1/admin/users/%d/authorization", boundaryUser.Id)
					recorder, granted := request(http.MethodPost, targetPath, adminJWT,
						xcodeDTO.XCodeAuthorizationMutation{Action: "grant", ExpectedRevision: "0", PlanID: strconv.Itoa(boundaryPlan.Id)})
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					id, err := strconv.Atoi(granted.Data.Subscription.ID)
					require.NoError(t, err)
					requestID := "boundary-request-" + action
					_, err = model.PreConsumeUserSubscription(requestID, boundaryUser.Id, "model", 0, 20,
						model.SubscriptionFundingTarget{Scope: model.SubscriptionUsageScopeXCode, SubscriptionID: id, AuthorizationRevision: 1})
					require.NoError(t, err)
					now := model.GetDBTimestamp()
					require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", id).Updates(map[string]any{
						"start_time": now - 120, "last_reset_time": now - 120, "next_reset_time": now - 60,
						"end_time": now + 3600, "amount_used": 98,
					}).Error)
					if action == "native" {
						recorder, _ = request(http.MethodPost, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d/invalidate", id), adminJWT, nil)
					} else {
						input := xcodeDTO.XCodeAuthorizationMutation{Action: action, ExpectedRevision: "1"}
						if action == "replace" {
							input.PlanID = strconv.Itoa(planB.Id)
						}
						recorder, _ = request(http.MethodPost, targetPath, adminJWT, input)
					}
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					var closed model.UserSubscription
					require.NoError(t, db.First(&closed, id).Error)
					assert.Equal(t, "cancelled", closed.Status)
					assert.Equal(t, int64(2), closed.QuotaResetGeneration, "persist the due active cycle before closing it")
					assert.Zero(t, closed.AmountUsed)
					require.NoError(t, model.SettleXCodeSubscriptionRequest(requestID, boundaryUser.Id, id, 30))
					require.NoError(t, db.First(&closed, id).Error)
					assert.Equal(t, int64(30), closed.AmountUsed)
					assert.Equal(t, int64(2), closed.QuotaResetGeneration, "closed-instance settlement cannot create another period")
				})
			}
			var orphan *model.UserSubscription
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var err error
				orphan, err = model.CreateUserSubscriptionFromPlanTx(tx, user.Id, &boundaryPlan, "admin", model.SubscriptionUsageScopeXCode)
				return err
			}))
			task := model.Task{TaskID: "xcode-reference", UserId: user.Id, Status: model.TaskStatusInProgress,
				PrivateData: model.TaskPrivateData{SubscriptionId: orphan.Id}, Data: []byte(`{}`)}
			require.NoError(t, db.Create(&task).Error)
			orphanPath := fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d", orphan.Id)
			recorder, _ = request(http.MethodDelete, orphanPath, adminJWT, nil)
			assert.Equal(t, http.StatusConflict, recorder.Code, "unfinished task's private-data reference protects an undesignated instance")
			require.NoError(t, db.Model(&task).Update("status", model.TaskStatusSuccess).Error)
			recorder, _ = request(http.MethodDelete, orphanPath, adminJWT, nil)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var orphanCount int64
			require.NoError(t, db.Model(&model.UserSubscription{}).Where("id = ?", orphan.Id).Count(&orphanCount).Error)
			assert.Zero(t, orphanCount)
			for _, action := range []string{"native-cancel", "native-delete", "dedicated-revoke"} {
				t.Run("committed-cache-failure-"+action, func(t *testing.T) {
					cacheUser := model.User{Username: "cache-" + action, AffCode: "cache-" + action, Group: "default", AuthVersion: 1}
					require.NoError(t, db.Create(&cacheUser).Error)
					groupPlan := boundaryPlan
					groupPlan.Id, groupPlan.Title, groupPlan.UpgradeGroup = 0, "Group "+action, "pro"
					require.NoError(t, db.Create(&groupPlan).Error)
					var id int
					targetPath := fmt.Sprintf("/api/xcode/v1/admin/users/%d/authorization", cacheUser.Id)
					if action == "native-delete" {
						require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
							sub, err := model.CreateUserSubscriptionFromPlanTx(tx, cacheUser.Id, &groupPlan, "admin", model.SubscriptionUsageScopeXCode)
							if err == nil {
								id = sub.Id
							}
							return err
						}))
					} else {
						recorder, granted := request(http.MethodPost, targetPath, adminJWT,
							xcodeDTO.XCodeAuthorizationMutation{Action: "grant", ExpectedRevision: "0", PlanID: strconv.Itoa(groupPlan.Id)})
						require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
						id, err = strconv.Atoi(granted.Data.Subscription.ID)
						require.NoError(t, err)
					}
					previousRedis, previousClient := common.RedisEnabled, common.RDB
					client := redis.NewClient(&redis.Options{
						Dialer: func(context.Context, string, string) (net.Conn, error) {
							return nil, errors.New("forced postcommit cache failure")
						},
						MaxRetries: -1,
					})
					common.RedisEnabled, common.RDB = true, client
					t.Cleanup(func() {
						common.RedisEnabled, common.RDB = previousRedis, previousClient
						require.NoError(t, client.Close())
					})
					auditAction := "subscription.invalidate"
					recorder := httptest.NewRecorder()
					switch action {
					case "native-cancel":
						recorder, _ = request(http.MethodPost, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d/invalidate", id), adminJWT, nil)
					case "native-delete":
						auditAction = "subscription.delete"
						recorder, _ = request(http.MethodDelete, fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d", id), adminJWT, nil)
					case "dedicated-revoke":
						auditAction = "xcode.authorization.revoke"
						recorder, _ = request(http.MethodPost, targetPath, adminJWT, xcodeDTO.XCodeAuthorizationMutation{Action: "revoke", ExpectedRevision: "1"})
					}
					require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
					var changedUser model.User
					require.NoError(t, db.First(&changedUser, cacheUser.Id).Error)
					assert.Equal(t, "default", changedUser.Group, "cache failure cannot roll back the committed downgrade")
					if action != "native-delete" {
						var changed model.UserSubscription
						require.NoError(t, db.First(&changed, id).Error)
						assert.Equal(t, "cancelled", changed.Status)
						var designation model.XCodeAuthorization
						require.NoError(t, db.Where("user_id = ?", cacheUser.Id).First(&designation).Error)
						assert.Equal(t, int64(2), designation.Revision)
					}
					var audit model.AuditLog
					require.NoError(t, db.Where("action = ? AND request_id = ?", auditAction, recorder.Header().Get(common.RequestIdKey)).First(&audit).Error)
					assert.True(t, audit.Success, "the audit must describe the committed transition despite the transport error")
					assert.Equal(t, admin.Id, audit.UserId)
				})
			}
		})
	}
}

func TestPreConsumeMultiplierRejectsInvalidRuntimeAndOverflow(t *testing.T) {
	previous := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(previous)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"policy-overflow":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"policy-overflow":"tier(\"base\", p * 3)"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	for _, multiplier := range []float64{0, -0.5, math.NaN(), math.Inf(1), math.MaxFloat64} {
		operation_setting.GetQuotaSetting().PreConsumeMultiplier = multiplier
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		info := &relaycommon.RelayInfo{OriginModelName: "policy-overflow", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
		_, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.Error(t, err)
		assert.Nil(t, info.Billing)
	}
}

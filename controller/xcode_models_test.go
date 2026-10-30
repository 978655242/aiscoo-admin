package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	xcodeDTO "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	relayDTO "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Explicit capabilities describe only this fixture, not provider-name defaults.
func xcodeModelConfigFixture(apiType string) string {
	outputMap := "{\"max_tokens\": maxOutputTokens}"
	if apiType == "openai-responses" {
		outputMap = "{\"max_output_tokens\": maxOutputTokens}"
	}
	return fmt.Sprintf(`{"schema_version":1,"api_type":%q,"model_config":{"enabled":true,"properties":{"requiresMfjsToolSchema":false,"contextWindow":8192,"inputFormat":{"supportsText":true,"supportsImage":false,"supportsVideo":false,"supportsAudio":false,"supportsPdf":false},"outputFormat":{"supportsText":true},"supportsToolCall":true,"supportsJsonSchemaOutput":false,"supportsNativeWebSearch":false,"supportsMidConversationSystem":false},"optionSpecs":{"reasoningLevel":{"values":["off"],"map":"{}"},"maxOutputTokens":{"max":128,"map":%q}}}}`, apiType, outputMap)
}

func xcodeCatalogFixture(t *testing.T, db *gorm.DB) model.User {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.XCodeAuthorization{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}, &model.UserSession{}, &model.Token{}))
	user := model.User{Username: "catalog-owner", AffCode: "catalog-owner", Group: "catalog", Status: common.UserStatusEnabled, Quota: 777}
	require.NoError(t, db.Create(&user).Error)
	for i, name := range []string{"chat", "claude", "responses", "missing", "invalid", "disabled", "config-disabled", "prefix-real"} {
		channel := model.Channel{Id: i + 1, Name: name, Type: 1, Key: "upstream-secret", BaseURL: common.GetPointer("https://secret-upstream.invalid"), Status: common.ChannelStatusEnabled, Group: "catalog", Models: name}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "catalog", Model: name, ChannelId: channel.Id, Enabled: true}).Error)
		if name == "missing" || name == "prefix-real" {
			continue
		}
		apiType := "openai-chat-completions"
		if name == "claude" {
			apiType = "anthropic-messages"
		}
		if name == "responses" {
			apiType = "openai-responses"
		}
		raw := xcodeModelConfigFixture(apiType)
		if name == "invalid" {
			raw = `{"schema_version":1}`
		}
		if name == "config-disabled" {
			raw = strings.Replace(raw, `"enabled":true`, `"enabled":false`, 1)
		}
		metadata := model.Model{ModelName: name, Description: "administrator-secret-description", Status: 1, NameRule: model.NameRuleExact, XCodeConfig: model.LongText(raw)}
		// 图标：chat 用模型自身 icon，claude 回落供应商 icon，其余为空。
		switch name {
		case "chat":
			metadata.Icon = "OpenAI"
		case "claude":
			vendor := model.Vendor{Name: "Anthropic", Icon: "Claude.Color"}
			require.NoError(t, db.Create(&vendor).Error)
			metadata.VendorID = vendor.Id
		}
		require.NoError(t, db.Create(&metadata).Error)
		if name == "disabled" {
			require.NoError(t, db.Model(&metadata).Update("status", 0).Error)
		}
	}
	require.NoError(t, db.Create(&model.Model{ModelName: "prefix-", NameRule: model.NameRulePrefix, Status: 1, XCodeConfig: model.LongText(xcodeModelConfigFixture("openai-chat-completions"))}).Error)
	// Stale abilities must not invent executable models from missing/disabled channels.
	require.NoError(t, db.Create(&model.Channel{Id: 99, Name: "off", Type: 1, Key: "secret", Status: common.ChannelStatusManuallyDisabled}).Error)
	for _, ability := range []model.Ability{{Group: "catalog", Model: "orphan", ChannelId: 999, Enabled: true}, {Group: "catalog", Model: "channel-off", ChannelId: 99, Enabled: true}, {Group: "catalog", Model: "ability-off", ChannelId: 1, Enabled: false}, {Group: "other", Model: "other-group", ChannelId: 1, Enabled: true}} {
		require.NoError(t, db.Create(&ability).Error)
	}
	return user
}

func TestXCodeCatalogDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			user := xcodeCatalogFixture(t, db)
			get := func() *xcodeDTO.XCodeCatalogSnapshot {
				t.Helper()
				value, err := service.GetXCodeModelCatalog(user.Id)
				require.NoError(t, err)
				return value
			}
			snapshot := get()
			assert.Equal(t, 1, snapshot.SchemaVersion)
			assert.Positive(t, snapshot.ServerTime)
			assert.Equal(t, "0", snapshot.AuthorizationRevision)
			var names []string
			for _, entry := range snapshot.Models {
				names = append(names, entry.ModelID)
			}
			assert.Equal(t, []string{"chat", "claude", "config-disabled", "disabled", "invalid", "missing", "prefix-real", "responses"}, names)
			expectedReasons := map[string]string{"missing": "model_metadata_missing", "prefix-real": "model_metadata_missing", "invalid": "model_metadata_invalid", "disabled": "model_disabled", "config-disabled": "model_config_disabled"}
			for _, entry := range snapshot.Models {
				assert.Equal(t, entry.ModelID, entry.DisplayName)
				expectedIcon := map[string]string{"chat": "OpenAI", "claude": "Claude.Color"}[entry.ModelID]
				if expectedIcon == "" {
					assert.Nil(t, entry.Icon, entry.ModelID)
				} else if assert.NotNil(t, entry.Icon, entry.ModelID) {
					assert.Equal(t, expectedIcon, *entry.Icon)
				}
				if reason, disabled := expectedReasons[entry.ModelID]; disabled {
					assert.False(t, entry.Enabled)
					require.NotNil(t, entry.DisabledReason)
					assert.Equal(t, reason, *entry.DisabledReason)
					validatedGroup, err := service.ValidateXCodeModelRequest(user.Id, entry.ModelID, "openai-chat-completions", common.GetPointer(uint(1)))
					assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
					assert.Empty(t, validatedGroup)
				} else {
					assert.True(t, entry.Enabled)
					assert.Nil(t, entry.DisabledReason)
					require.NotNil(t, entry.APIType)
					validatedGroup, err := service.ValidateXCodeModelRequest(user.Id, entry.ModelID, *entry.APIType, common.GetPointer(uint(128)))
					require.NoError(t, err)
					assert.Equal(t, "catalog", validatedGroup)
				}
				if entry.ModelID == "missing" || entry.ModelID == "prefix-real" || entry.ModelID == "invalid" {
					assert.Nil(t, entry.APIType)
					assert.Nil(t, entry.ModelConfig)
				}
			}
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/xcode/v1/models", nil)
			common.SetContextKey(ctx, constant.ContextKeyUserId, user.Id)
			GetXCodeModels(ctx)
			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			var wire map[string]any
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &wire))
			data := wire["data"].(map[string]any)
			assert.Len(t, data, 5)
			for _, raw := range data["models"].([]any) {
				assert.Len(t, raw.(map[string]any), 7)
			}
			for _, secret := range []string{"upstream-secret", "secret-upstream", "administrator-secret", "channel_id", "base_url", "created_time"} {
				assert.NotContains(t, response.Body.String(), secret)
			}
			assert.Equal(t, snapshot.CatalogRevision, get().CatalogRevision)
			require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "chat").Updates(map[string]any{"updated_time": 999, "description": "changed prose", "x_code_config": "  " + xcodeModelConfigFixture("openai-chat-completions") + "\n"}).Error)
			assert.Equal(t, snapshot.CatalogRevision, get().CatalogRevision, "incidental timestamps/prose and JSON formatting are not capability changes")
			t.Run("reasoning configuration changes do not reauthorize or alter other models", func(t *testing.T) {
				before := get()
				var metadata model.Model
				require.NoError(t, db.Where("model_name = ?", "chat").First(&metadata).Error)
				original := string(metadata.XCodeConfig)
				defer func() { require.NoError(t, metadata.Update(&original)) }()
				optionMap := `reasoningLevel == "disabled" ? {"thinking":{"type":"disabled"},"reasoning_effort":null} : {"thinking":{"type":"enabled"},"reasoning_effort":reasoningLevel}`
				updated := strings.Replace(original, `"values":["off"],"map":"{}"`, fmt.Sprintf(`"values":["disabled","low","high","max"],"map":%q`, optionMap), 1)
				metadata.XCodeConfig = model.LongText(updated)
				var saved struct {
					Success bool
					Data    model.Model
				}
				modelManagementRequest(t, UpdateModelMeta, http.MethodPut, "/api/models/", metadata, &saved)
				require.True(t, saved.Success)
				assert.Equal(t, updated, string(saved.Data.XCodeConfig))
				after := get()
				assert.NotEqual(t, before.CatalogRevision, after.CatalogRevision)
				assert.Equal(t, before.AuthorizationRevision, after.AuthorizationRevision)
				for i, entry := range after.Models {
					if entry.ModelID != "chat" {
						assert.Equal(t, before.Models[i], entry)
						continue
					}
					require.NotNil(t, entry.ModelConfig)
					assert.Equal(t, []string{"disabled", "low", "high", "max"}, entry.ModelConfig.OptionSpecs.ReasoningLevel.Values)
					assert.Equal(t, optionMap, entry.ModelConfig.OptionSpecs.ReasoningLevel.Map)
				}
			})
			previous := snapshot.CatalogRevision
			for _, update := range []func(){
				func() {
					require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "chat").Update("x_code_config", strings.Replace(xcodeModelConfigFixture("openai-chat-completions"), `"max":128`, `"max":64`, 1)).Error)
				},
				func() {
					require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "chat").Update("x_code_config", xcodeModelConfigFixture("openai-responses")).Error)
				},
				func() { require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update("type", 14).Error) },
				func() {
					require.NoError(t, db.Model(&model.Ability{}).Where("model = ?", "chat").Update("weight", 2).Error)
				},
				func() {
					require.NoError(t, db.Model(&model.Ability{}).Where("model = ?", "claude").Update("enabled", false).Error)
				},
				func() {
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update("status", common.ChannelStatusManuallyDisabled).Error)
				},
				func() { require.NoError(t, db.Model(&user).Update("group", "other").Error) },
			} {
				update()
				revision := get().CatalogRevision
				assert.NotEqual(t, previous, revision)
				previous = revision
			}
			require.NoError(t, db.Model(&user).Update("group", "catalog").Error)
			validatedGroup, err := service.ValidateXCodeModelRequest(user.Id, "other-group", "openai-chat-completions", common.GetPointer(uint(1)))
			assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
			assert.Empty(t, validatedGroup)
			beforeGrant := get().CatalogRevision
			now := model.GetDBTimestamp()
			sub := model.UserSubscription{UserId: user.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 3600, AmountTotal: 100, QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
			require.NoError(t, db.Create(&sub).Error)
			require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)
			assigned := get()
			assert.Equal(t, "1", assigned.AuthorizationRevision)
			assert.NotEqual(t, beforeGrant, assigned.CatalogRevision)
			require.NoError(t, db.Model(&model.XCodeAuthorization{}).Where("user_id = ?", user.Id).Update("revision", 2).Error)
			assert.NotEqual(t, assigned.CatalogRevision, get().CatalogRevision)
			assigned = get()
			require.NoError(t, db.Model(&sub).Update("status", "cancelled").Error)
			assert.NotEqual(t, assigned.CatalogRevision, get().CatalogRevision)
			t.Run("exact authority survives collation variants", func(t *testing.T) {
				if dialect.kind == "mysql" {
					require.NoError(t, db.Exec("ALTER TABLE abilities MODIFY `group` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL, MODIFY model varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL").Error)
				}
				for i, identity := range []struct{ group, name string }{
					{"Catalog", "authority-exact"}, {"catalog ", "authority-exact"},
					{"catalog", "AUTHORITY-EXACT"}, {"catalog", "authority-exact "},
					{"catalog", "authority-exact"},
				} {
					priority := int64(100 - i)
					if i == 4 {
						priority = 0
					}
					channel := model.Channel{Id: 201 + i, Name: "authority", Type: constant.ChannelTypeOpenAI, Key: "secret", Status: common.ChannelStatusEnabled, Group: identity.group, Models: identity.name, Priority: &priority}
					require.NoError(t, db.Create(&channel).Error)
					require.NoError(t, db.Create(&model.Ability{Group: identity.group, Model: identity.name, ChannelId: channel.Id, Enabled: true, Priority: &priority}).Error)
				}
				require.NoError(t, db.Create(&model.Model{ModelName: "authority-exact", NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(xcodeModelConfigFixture("openai-chat-completions"))}).Error)
				validatedGroup, err := service.ValidateXCodeModelRequest(user.Id, "authority-exact", "openai-chat-completions", common.GetPointer(uint(1)))
				require.NoError(t, err)
				assert.Equal(t, "catalog", validatedGroup)
				found := false
				for _, entry := range get().Models {
					if entry.ModelID == "authority-exact" {
						found = true
						assert.True(t, entry.Enabled)
					}
				}
				assert.True(t, found, "the exact executable identity is advertised")
				for _, cached := range []bool{false, true} {
					t.Run(fmt.Sprintf("selection_cache_%t", cached), func(t *testing.T) {
						previousMemory := common.MemoryCacheEnabled
						common.MemoryCacheEnabled = cached
						t.Cleanup(func() { common.MemoryCacheEnabled = previousMemory })
						if cached {
							model.InitChannelCache()
						}
						ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
						ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"authority-exact","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`))
						ctx.Request.Header.Set("Content-Type", "application/json")
						common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
						common.SetContextKey(ctx, constant.ContextKeyUserId, user.Id)
						_, err := helper.GetAndValidateRequest(ctx, types.RelayFormatOpenAI)
						require.NoError(t, err)
						param := &service.RetryParam{Ctx: ctx, TokenGroup: "catalog", ModelName: "authority-exact", RequestPath: "/v1/chat/completions"}
						selected, group, selectionError := service.SelectChannelForRequest(ctx, "authority-exact", param)
						require.Nil(t, selectionError)
						require.NotNil(t, selected)
						assert.Equal(t, 205, selected.Id, "a higher-priority collation equivalent is not managed authority")
						assert.Equal(t, "catalog", group)
						constraints := service.GetChannelConstraints(ctx)
						constraints.AddPin(xcodeDTO.ChannelPin{ChannelId: 201, Source: xcodeDTO.PinSourceToken})
						pinned, _, pinError := service.SelectChannelForRequest(ctx, "authority-exact", param)
						require.NotNil(t, pinError, "a pin cannot bypass exact ability admission")
						assert.Nil(t, pinned)
						constraints.Pins = []xcodeDTO.ChannelPin{{ChannelId: 205, Source: xcodeDTO.PinSourceToken}}
						pinned, _, pinError = service.SelectChannelForRequest(ctx, "authority-exact", param)
						require.Nil(t, pinError)
						require.NotNil(t, pinned)
						assert.Equal(t, 205, pinned.Id)
						if cached {
							require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 205).Updates(map[string]any{"type": constant.ChannelTypeAdvancedCustom, "settings": `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/responses","upstream_path":"/provider/responses"}]}}`}).Error)
							pinned, _, pinError = service.SelectChannelForRequest(ctx, "authority-exact", param)
							require.NotNil(t, pinError, "a warm pin must use live routing facts, not the cached OpenAI channel")
							assert.Nil(t, pinned)
							require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 205).Update("settings", `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/provider/chat"}]}}`).Error)
							pinned, _, pinError = service.SelectChannelForRequest(ctx, "authority-exact", param)
							require.Nil(t, pinError)
							require.NotNil(t, pinned)
							assert.Equal(t, constant.ChannelTypeAdvancedCustom, pinned.Type, "dispatch receives the live channel, not its cached projection")
							require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 205).Updates(map[string]any{"type": constant.ChannelTypeOpenAI, "settings": ""}).Error)
						}
						constraints.Pins = nil
						for _, attempt := range []int{1, 4} {
							param.SetRetry(attempt)
							param.TokenGroup = "Catalog"
							selected, group, err = service.CacheGetRandomSatisfiedChannel(param)
							require.NoError(t, err)
							require.NotNil(t, selected)
							assert.Equal(t, 205, selected.Id, "retry stays on the exact group and model")
							assert.Equal(t, "catalog", group)
						}
						if cached {
							replacement := model.Channel{Id: 206, Name: "replacement", Type: constant.ChannelTypeOpenAI, Key: "secret", Status: common.ChannelStatusEnabled, Group: "catalog", Models: "authority-exact"}
							require.NoError(t, db.Create(&replacement).Error)
							require.NoError(t, db.Create(&model.Ability{Group: "catalog", Model: "authority-exact", ChannelId: 206, Enabled: true}).Error)
							require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 205).Update("enabled", false).Error)
							param.SetRetry(0)
							selected, _, selectionError = service.SelectChannelForRequest(ctx, "authority-exact", param)
							require.Nil(t, selectionError)
							require.NotNil(t, selected)
							assert.Equal(t, 206, selected.Id, "a warm native cache cannot retain retired managed authority")
							param.SetRetry(1)
							selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
							require.NoError(t, err)
							require.NotNil(t, selected)
							assert.Equal(t, 206, selected.Id)
							require.NoError(t, db.Model(&replacement).Update("status", common.ChannelStatusManuallyDisabled).Error)
							selected, _, err = service.CacheGetRandomSatisfiedChannel(param)
							require.NoError(t, err)
							assert.Nil(t, selected, "a warm cache cannot dispatch when only collation equivalents remain")
							require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 205).Update("enabled", true).Error)
						}
					})
				}
				if dialect.kind == "mysql" {
					native, err := model.GetChannel("catalog", "authority-exact", 0, nil)
					require.NoError(t, err)
					require.NotNil(t, native)
					assert.Equal(t, 201, native.Id, "native SQL collation routing is intentionally unchanged")
				}
				require.NoError(t, db.Create(&model.Ability{Group: "live-catalog", Model: "authority-exact", ChannelId: 205, Enabled: true}).Error)
				require.NoError(t, db.Model(&user).Update("group", "live-catalog").Error)
				validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "authority-exact", "openai-chat-completions", common.GetPointer(uint(1)))
				require.NoError(t, err)
				assert.Equal(t, "live-catalog", validatedGroup, "successful admission supplies the live authority group for distribution")
				require.NoError(t, db.Model(&user).Update("group", "catalog").Error)
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 205).Update("status", common.ChannelStatusManuallyDisabled).Error)
				validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "authority-exact", "openai-chat-completions", common.GetPointer(uint(1)))
				assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
				assert.Empty(t, validatedGroup)
				for _, entry := range get().Models {
					assert.NotEqual(t, "authority-exact", entry.ModelID, "case and trailing-space variants cannot authorize the exact identity")
				}
			})
			t.Run("targeted admission chooses exact metadata rather than SQL first", func(t *testing.T) {
				if dialect.kind == "mysql" {
					require.NoError(t, db.Exec("ALTER TABLE models MODIFY model_name varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL").Error)
				}
				for _, name := range []string{"METADATA-FIRST", "metadata-first "} {
					require.NoError(t, db.Create(&model.Model{ModelName: name, NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(xcodeModelConfigFixture("openai-responses"))}).Error)
				}
				exact := model.Model{ModelName: "metadata-first", NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(strings.Replace(xcodeModelConfigFixture("openai-chat-completions"), `"max":128`, `"max":64`, 1))}
				require.NoError(t, db.Create(&exact).Error)
				channel := model.Channel{Id: 207, Name: "metadata-first", Type: constant.ChannelTypeOpenAI, Key: "secret", Status: common.ChannelStatusEnabled}
				require.NoError(t, db.Create(&channel).Error)
				require.NoError(t, db.Create(&model.Ability{Group: "catalog", Model: exact.ModelName, ChannelId: channel.Id, Enabled: true}).Error)
				entry := func() xcodeDTO.XCodeCatalogModel {
					t.Helper()
					for _, value := range get().Models {
						if value.ModelID == "metadata-first" {
							return value
						}
					}
					t.Fatal("exact metadata model disappeared")
					return xcodeDTO.XCodeCatalogModel{}
				}
				require.True(t, entry().Enabled)
				validatedGroup, err := service.ValidateXCodeModelRequest(user.Id, "metadata-first", "openai-chat-completions", common.GetPointer(uint(64)))
				require.NoError(t, err)
				assert.Equal(t, "catalog", validatedGroup)
				_, err = service.ValidateXCodeModelRequest(user.Id, "metadata-first", "openai-chat-completions", common.GetPointer(uint(65)))
				var apiError *types.NewAPIError
				require.ErrorAs(t, err, &apiError)
				assert.Equal(t, http.StatusBadRequest, apiError.StatusCode, "the exact row's output boundary wins")
				_, err = service.ValidateXCodeModelRequest(user.Id, "metadata-first", "openai-responses", common.GetPointer(uint(1)))
				assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
				require.NoError(t, db.Delete(&exact).Error)
				assert.False(t, entry().Enabled, "collation-equivalent metadata cannot replace missing exact authority")
				_, err = service.ValidateXCodeModelRequest(user.Id, "metadata-first", "openai-chat-completions", common.GetPointer(uint(1)))
				assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
			})
			t.Run("effective route revisions and availability", func(t *testing.T) {
				channel := model.Channel{Id: 301, Name: "route", Type: constant.ChannelTypeAdvancedCustom, Key: "route-credential", Status: common.ChannelStatusEnabled}
				route := relayDTO.AdvancedCustomRoute{IncomingPath: "/v1/responses", UpstreamPath: "/provider/responses", Models: []string{"route-model"}, Auth: &relayDTO.AdvancedCustomRouteAuth{Type: "header", Name: "X-API-Key", Value: "route-auth-secret"}}
				settings := relayDTO.ChannelOtherSettings{AdvancedCustom: &relayDTO.AdvancedCustomConfig{Routes: []relayDTO.AdvancedCustomRoute{route}}}
				channel.SetOtherSettings(settings)
				channel.SetSetting(relayDTO.ChannelSettings{Proxy: "http://proxy-user:proxy-password@proxy.invalid:8080"})
				require.NoError(t, db.Create(&channel).Error)
				require.NoError(t, db.Create(&model.Ability{Group: "catalog", Model: "route-model", ChannelId: channel.Id, Enabled: true}).Error)
				require.NoError(t, db.Create(&model.Model{ModelName: "route-model", NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(xcodeModelConfigFixture("openai-responses"))}).Error)
				routeEntry := func() xcodeDTO.XCodeCatalogModel {
					t.Helper()
					for _, entry := range get().Models {
						if entry.ModelID == "route-model" {
							return entry
						}
					}
					t.Fatal("configured route model disappeared")
					return xcodeDTO.XCodeCatalogModel{}
				}
				require.True(t, routeEntry().Enabled)
				validatedGroup, err := service.ValidateXCodeModelRequest(user.Id, "route-model", "openai-responses", common.GetPointer(uint(1)))
				require.NoError(t, err)
				assert.Equal(t, "catalog", validatedGroup)
				initial := get().CatalogRevision
				save := func() {
					t.Helper()
					channel.SetOtherSettings(settings)
					require.NoError(t, db.Model(&channel).Updates(map[string]any{"settings": channel.OtherSettings, "setting": channel.Setting}).Error)
				}
				settings.UpstreamModelUpdateLastCheckTime = 999
				settings.AdvancedCustom.Routes[0].Auth.Value = "rotated-auth-secret"
				channel.SetSetting(relayDTO.ChannelSettings{Proxy: "http://other:rotated-password@proxy.invalid:8080"})
				save()
				assert.Equal(t, initial, get().CatalogRevision, "credentials and incidental discovery timestamps are excluded")
				require.NoError(t, db.Model(&channel).Update("settings", " \n"+channel.OtherSettings).Error)
				assert.Equal(t, initial, get().CatalogRevision, "route JSON formatting is not a configuration change")
				settings.AdvancedCustom.Routes[0].UpstreamPath = "/provider/responses-v2"
				save()
				rerouted := get().CatalogRevision
				assert.NotEqual(t, initial, rerouted, "effective upstream route changes invalidate the catalog")
				settings.AdvancedCustom.Routes[0].IncomingPath = "/v1/chat/completions"
				save()
				disabled := routeEntry()
				assert.False(t, disabled.Enabled)
				require.NotNil(t, disabled.DisabledReason)
				assert.Equal(t, "model_route_unavailable", *disabled.DisabledReason)
				assert.NotEqual(t, rerouted, get().CatalogRevision)
				validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "route-model", "openai-responses", common.GetPointer(uint(1)))
				assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
				assert.Empty(t, validatedGroup)
				settings.AdvancedCustom.Routes[0].IncomingPath = "/v1/responses"
				settings.AdvancedCustom.Routes[0].Models = []string{"another-model"}
				save()
				assert.False(t, routeEntry().Enabled, "model-specific route restrictions are enforced")
				validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "route-model", "openai-responses", common.GetPointer(uint(1)))
				assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
				assert.Empty(t, validatedGroup)
				for _, protocol := range []struct{ apiType, path string }{
					{"openai-chat-completions", "/v1/chat/completions"},
					{"anthropic-messages", "/v1/messages"},
					{"openai-responses", "/v1/responses"},
				} {
					settings.AdvancedCustom.Routes[0].IncomingPath = protocol.path
					settings.AdvancedCustom.Routes[0].Models = []string{"route-model"}
					save()
					require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "route-model").Update("x_code_config", xcodeModelConfigFixture(protocol.apiType)).Error)
					assert.True(t, routeEntry().Enabled, protocol.apiType)
					validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "route-model", protocol.apiType, common.GetPointer(uint(1)))
					require.NoError(t, err)
					assert.Equal(t, "catalog", validatedGroup)
					settings.AdvancedCustom.Routes[0].Models = []string{"another-model"}
					save()
					assert.False(t, routeEntry().Enabled, protocol.apiType)
					validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "route-model", protocol.apiType, common.GetPointer(uint(1)))
					assert.ErrorIs(t, err, service.ErrXCodeModelUnavailable)
					assert.Empty(t, validatedGroup)
				}
				native := model.Channel{Id: 302, Name: "eligible", Type: constant.ChannelTypeOpenAI, Key: "secret", Status: common.ChannelStatusEnabled}
				require.NoError(t, db.Create(&native).Error)
				require.NoError(t, db.Create(&model.Ability{Group: "catalog", Model: "route-model", ChannelId: native.Id, Enabled: true}).Error)
				assert.True(t, routeEntry().Enabled, "a selectable candidate restores route availability")
				validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "route-model", "openai-responses", common.GetPointer(uint(1)))
				require.NoError(t, err)
				assert.Equal(t, "catalog", validatedGroup)
				wire, err := common.Marshal(get())
				require.NoError(t, err)
				for _, secret := range []string{"route-credential", "rotated-auth-secret", "rotated-password", "proxy.invalid", "/provider/responses-v2", "channel_id", "advanced_routes"} {
					assert.NotContains(t, string(wire), secret)
				}
				require.NoError(t, db.Model(&channel).Update("type", constant.ChannelTypeVLLM).Error)
				presetRevision := get().CatalogRevision
				settings.AdvancedCustom.Routes[0].IncomingPath = "/ignored-edit"
				save()
				assert.Equal(t, presetRevision, get().CatalogRevision, "named channels use normalized presets, not editable route overrides")
				require.NoError(t, db.Model(&channel).Updates(map[string]any{"settings": "{", "setting": "{"}).Error)
				validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "route-model", "openai-responses", common.GetPointer(uint(1)))
				require.NoError(t, err)
				assert.Equal(t, "catalog", validatedGroup)
				assert.True(t, routeEntry().Enabled, "named preset remains effective for malformed stored overrides")
				var stored model.Channel
				require.NoError(t, db.First(&stored, channel.Id).Error)
				assert.Equal(t, "{", stored.OtherSettings, "catalog reads cannot repair-and-save projected channel data")
				require.NotNil(t, stored.Setting)
				assert.Equal(t, "{", *stored.Setting)
				assert.Equal(t, "route-credential", stored.Key)
			})
		})
	}
}

func TestXCodeManagedRequestValidation(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	user := xcodeCatalogFixture(t, db)
	now := model.GetDBTimestamp()
	require.NoError(t, db.Model(&user).Update("auth_version", 1).Error)
	sub := model.UserSubscription{UserId: user.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 3600, AmountTotal: 100, AmountUsed: 7, QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
	require.NoError(t, db.Create(&sub).Error)
	require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)
	require.NoError(t, db.Create(&model.UserSession{SID: "catalog-managed-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, ClientPurpose: model.ClientPurposeXCode, RefreshHash: strings.Repeat("a", 64), LoginMethod: "password", ExpiresAt: now + 3600}).Error)
	require.NoError(t, db.Create(&model.Token{UserId: user.Id, Key: "catalogmanagedkey", Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: now + 3600, ClientPurpose: model.ClientPurposeXCode, ClientSessionSID: "catalog-managed-session"}).Error)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(upstream.Close)
	require.NoError(t, db.Model(&model.Channel{}).Where("id IN ?", []int{1, 2, 3}).Update("base_url", upstream.URL).Error)
	protocols := []struct {
		path, modelID, apiType, field string
		format                        types.RelayFormat
	}{
		{"/v1/chat/completions", "chat", "openai-chat-completions", "max_tokens", types.RelayFormatOpenAI},
		{"/v1/messages", "claude", "anthropic-messages", "max_tokens", types.RelayFormatClaude},
		{"/v1/responses", "responses", "openai-responses", "max_output_tokens", types.RelayFormatOpenAIResponses},
	}
	for _, protocol := range protocols {
		for _, tc := range []struct {
			name, modelID, limits string
			status                int
			code                  string
		}{
			{"at metadata maximum", protocol.modelID, fmt.Sprintf(`,%q:128`, protocol.field), 0, ""},
			{"explicit zero", protocol.modelID, fmt.Sprintf(`,%q:0`, protocol.field), 0, ""},
			{"absent output bound", protocol.modelID, "", 400, "invalid_request"},
			{"above metadata maximum", protocol.modelID, fmt.Sprintf(`,%q:129`, protocol.field), 400, "invalid_request"},
			{"native safety maximum", protocol.modelID, fmt.Sprintf(`,%q:%d`, protocol.field, uint64(math.MaxInt32)), 400, ""},
			{"missing metadata", "missing", fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"invalid metadata", "invalid", fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"disabled metadata", "disabled", fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"disabled configuration", "config-disabled", fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"prefix metadata only", "prefix-real", fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"unavailable real model", "orphan", fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"case is not alias", strings.ToUpper(protocol.modelID), fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"whitespace is not alias", " " + protocol.modelID, fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
			{"wrong protocol", map[string]string{"chat": "claude", "claude": "responses", "responses": "chat"}[protocol.modelID], fmt.Sprintf(`,%q:1`, protocol.field), 403, "XCODE_MODEL_UNAVAILABLE"},
		} {
			t.Run(protocol.apiType+"/"+tc.name, func(t *testing.T) {
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"input":"hello"%s}`, tc.modelID, tc.limits)
				response := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(response)
				ctx.Request = httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(body))
				ctx.Request.Header.Set("Content-Type", "application/json")
				common.SetContextKey(ctx, constant.ContextKeyUserId, user.Id)
				common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
				request, err := helper.GetAndValidateRequest(ctx, protocol.format)
				if tc.status == 0 {
					require.NoError(t, err)
					require.NotNil(t, request)
					if tc.name == "explicit zero" {
						encoded, marshalErr := common.Marshal(request)
						require.NoError(t, marshalErr)
						assert.Contains(t, string(encoded), fmt.Sprintf(`%q:0`, protocol.field))
					}
					return
				}
				require.Error(t, err)
				if tc.name != "native safety maximum" {
					var apiError *types.NewAPIError
					require.ErrorAs(t, err, &apiError)
					assert.Equal(t, tc.status, apiError.StatusCode)
					assert.Equal(t, types.ErrorCode(tc.code), apiError.GetErrorCode())
					assert.True(t, types.IsSkipRetryError(apiError))
				}
				// The actual Relay envelope must preserve the refusal and never reach billing.
				response = httptest.NewRecorder()
				router := gin.New()
				router.POST(protocol.path, middleware.TokenAuth(), func(c *gin.Context) { Relay(c, protocol.format) })
				relayRequest := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(body))
				relayRequest.Header.Set("Authorization", "Bearer sk-catalogmanagedkey")
				relayRequest.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(response, relayRequest)
				assert.Equal(t, tc.status, response.Code, response.Body.String())
				if tc.code != "" && (protocol.format != types.RelayFormatClaude || strings.HasPrefix(tc.code, "XCODE_")) {
					assert.Contains(t, response.Body.String(), fmt.Sprintf(`"code":%q`, tc.code))
				}
				if protocol.format == types.RelayFormatClaude {
					assert.Contains(t, response.Body.String(), `"type":"error"`)
					if tc.status == http.StatusBadRequest {
						assert.Contains(t, response.Body.String(), `"type":"invalid_request_error"`)
					}
				}
			})
		}
	}
	// Both OpenAI fields are independently bounded, including the ignored one.
	for _, limits := range []string{`"max_tokens":129,"max_completion_tokens":1`, `"max_tokens":1,"max_completion_tokens":129`} {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"hello"}],`+limits+`}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(ctx, constant.ContextKeyUserId, user.Id)
		common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
		_, err := helper.GetAndValidateRequest(ctx, types.RelayFormatOpenAI)
		var apiError *types.NewAPIError
		require.ErrorAs(t, err, &apiError)
		assert.Equal(t, 400, apiError.StatusCode)
	}
	for _, tc := range []struct {
		path, modelID, limits string
		format                types.RelayFormat
		accepted              bool
	}{
		{"/v1/chat/completions", "chat", `"max_completion_tokens":128`, types.RelayFormatOpenAI, true},
		{"/v1/messages", "claude", `"max_tokens":1,"max_tokens_to_sample":129`, types.RelayFormatClaude, false},
		{"/v1/messages", "claude", `"max_tokens_to_sample":1`, types.RelayFormatClaude, false},
	} {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],%s}`, tc.modelID, tc.limits)))
		ctx.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(ctx, constant.ContextKeyUserId, user.Id)
		common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
		_, err := helper.GetAndValidateRequest(ctx, tc.format)
		if tc.accepted {
			require.NoError(t, err)
		} else {
			var apiError *types.NewAPIError
			require.ErrorAs(t, err, &apiError)
			assert.Equal(t, 400, apiError.StatusCode)
		}
	}
	// Native credentials do not acquire metadata bounds or need a current catalog.
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"no-native-metadata","max_tokens":129,"messages":[{"role":"user","content":"hello"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	request, err := helper.GetAndValidateRequest(ctx, types.RelayFormatOpenAI)
	require.NoError(t, err)
	assert.IsType(t, &relayDTO.GeneralOpenAIRequest{}, request)
	ctx, _ = gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"no-native-metadata","messages":[{"role":"user","content":"hello"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	request, err = helper.GetAndValidateRequest(ctx, types.RelayFormatOpenAI)
	require.NoError(t, err)
	assert.Nil(t, request.(*relayDTO.GeneralOpenAIRequest).MaxTokens)
	assert.Nil(t, request.(*relayDTO.GeneralOpenAIRequest).MaxCompletionTokens)
	require.NoError(t, db.First(&sub, sub.Id).Error)
	assert.Equal(t, int64(7), sub.AmountUsed)
	var persisted model.User
	require.NoError(t, db.First(&persisted, user.Id).Error)
	assert.Equal(t, 777, persisted.Quota)
	var records int64
	require.NoError(t, db.Model(&model.SubscriptionPreConsumeRecord{}).Count(&records).Error)
	assert.Zero(t, records)
	assert.Zero(t, calls.Load())
}

func TestXCodeCatalogReadFailuresAreNotEmptySuccess(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, table := range []string{"users", "abilities", "channels", "models", "x_code_authorizations", "user_subscriptions"} {
		t.Run(table, func(t *testing.T) {
			db := modelManagementDB(t, "sqlite", "")
			user := xcodeCatalogFixture(t, db)
			now := model.GetDBTimestamp()
			sub := model.UserSubscription{UserId: user.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 3600, QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
			require.NoError(t, db.Create(&sub).Error)
			require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)
			if table == "x_code_authorizations" {
				require.NoError(t, db.Migrator().DropTable(&model.XCodeAuthorization{}))
			} else {
				require.NoError(t, db.Migrator().DropTable(table))
			}
			snapshot, err := service.GetXCodeModelCatalog(user.Id)
			require.Error(t, err)
			assert.Nil(t, snapshot)
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/xcode/v1/models", nil)
			common.SetContextKey(ctx, constant.ContextKeyUserId, user.Id)
			GetXCodeModels(ctx)
			assert.Equal(t, 503, response.Code)
			assert.Contains(t, response.Body.String(), "XCODE_BACKEND_UNAVAILABLE")
			if table == "users" || table == "abilities" || table == "channels" || table == "models" {
				var validatedGroup string
				validatedGroup, err = service.ValidateXCodeModelRequest(user.Id, "chat", "openai-chat-completions", common.GetPointer(uint(1)))
				assert.Empty(t, validatedGroup)
				var apiError *types.NewAPIError
				require.ErrorAs(t, err, &apiError)
				assert.Equal(t, 503, apiError.StatusCode)
				assert.Equal(t, types.ErrorCode("XCODE_BACKEND_UNAVAILABLE"), apiError.GetErrorCode())
				for _, protocol := range []struct {
					path   string
					format types.RelayFormat
				}{{"/v1/chat/completions", types.RelayFormatOpenAI}, {"/v1/messages", types.RelayFormatClaude}, {"/v1/responses", types.RelayFormatOpenAIResponses}} {
					response = httptest.NewRecorder()
					ctx, _ = gin.CreateTestContext(response)
					ctx.Request = httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(`{"model":"chat","max_tokens":1,"max_output_tokens":1,"messages":[{"role":"user","content":"hello"}],"input":"hello"}`))
					ctx.Request.Header.Set("Content-Type", "application/json")
					common.SetContextKey(ctx, constant.ContextKeyUserId, user.Id)
					common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
					Relay(ctx, protocol.format)
					assert.Equal(t, 503, response.Code, response.Body.String())
					assert.Contains(t, response.Body.String(), `"code":"XCODE_BACKEND_UNAVAILABLE"`)
					assert.Contains(t, response.Body.String(), "aiscoo client service is temporarily unavailable")
				}
			}
		})
	}
}

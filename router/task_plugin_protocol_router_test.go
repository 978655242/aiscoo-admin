package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func productionXCodeConfig(apiType string) string {
	field := "max_tokens"
	if apiType == "openai-responses" {
		field = "max_output_tokens"
	}
	return fmt.Sprintf(`{"schema_version":1,"api_type":%q,"model_config":{"enabled":true,"properties":{"requiresMfjsToolSchema":false,"contextWindow":8192,"inputFormat":{"supportsText":true,"supportsImage":false,"supportsVideo":false,"supportsAudio":false,"supportsPdf":false},"outputFormat":{"supportsText":true},"supportsToolCall":true,"supportsJsonSchemaOutput":false,"supportsNativeWebSearch":false,"supportsMidConversationSystem":false},"optionSpecs":{"reasoningLevel":{"values":["off"],"map":"{}"},"maxOutputTokens":{"max":128,"map":%q}}}}`, apiType, fmt.Sprintf(`{"%s": maxOutputTokens}`, field))
}

func setupXCodeProductionRouter(t *testing.T) (*gin.Engine, model.UserSubscription) {
	t.Helper()
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Model{}, &model.UserSession{}, &model.XCodeAuthorization{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.Task{}, &model.Log{}))
	previousMemoryCache, previousRateLimit := common.MemoryCacheEnabled, setting.ModelRequestRateLimitEnabled
	common.MemoryCacheEnabled, setting.ModelRequestRateLimitEnabled = false, false
	t.Cleanup(func() {
		common.MemoryCacheEnabled, setting.ModelRequestRateLimitEnabled = previousMemoryCache, previousRateLimit
	})
	user := model.User{Username: "production-xcode-user", AffCode: "production-xcode-user", Group: "default", Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 777}
	require.NoError(t, model.DB.Create(&user).Error)
	now := model.GetDBTimestamp()
	sub := model.UserSubscription{UserId: user.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 3600, AmountTotal: 100, AmountUsed: 7, QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
	require.NoError(t, model.DB.Create(&sub).Error)
	require.NoError(t, model.DB.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{SID: "production-xcode-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, ClientPurpose: model.ClientPurposeXCode, RefreshHash: strings.Repeat("a", 64), LoginMethod: "password", ExpiresAt: now + 3600}).Error)
	for _, token := range []model.Token{
		{UserId: user.Id, Key: "productionmanagedkey", Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: now + 3600, ClientPurpose: model.ClientPurposeXCode, ClientSessionSID: "production-xcode-session"},
		{UserId: user.Id, Key: "productionnativekey", Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1},
	} {
		require.NoError(t, model.DB.Create(&token).Error)
	}
	engine := gin.New()
	var requestSequence atomic.Int64
	engine.Use(func(c *gin.Context) {
		c.Set(common.RequestIdKey, fmt.Sprintf("production-admission-request-%d", requestSequence.Add(1)))
		c.Next()
		if c.GetHeader("X-Expect-No-Dispatch") == "true" {
			assert.Zero(t, common.GetContextKeyInt(c, constant.ContextKeyChannelId), "managed refusal must precede channel selection")
			_, pinned := c.Get(pluginruntime.ContextKeyPinnedEndpoint)
			assert.False(t, pinned, "managed refusal must precede plugin pinning")
		}
	})
	SetRelayRouter(engine)
	SetTaskPluginProtocolRouter(engine)
	return engine, sub
}

func productionRelayRequest(engine *gin.Engine, path, key, body string, noDispatch bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	if noDispatch {
		request.Header.Set("X-Expect-No-Dispatch", "true")
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

func TestManagedProductionRoutesRejectUnavailableModelsBeforeDistribution(t *testing.T) {
	engine, sub := setupXCodeProductionRouter(t)
	for _, protocol := range []struct{ path, apiType, field string }{
		{"/v1/chat/completions", "openai-chat-completions", "max_tokens"},
		{"/v1/messages", "anthropic-messages", "max_tokens"},
		{"/v1/responses", "openai-responses", "max_output_tokens"},
	} {
		for _, reason := range []string{"nonexistent", "orphan", "channel-off", "ability-off", "missing", "invalid", "metadata-disabled", "config-disabled", "wrong-protocol"} {
			t.Run(protocol.apiType+"/"+reason, func(t *testing.T) {
				name := protocol.apiType + "-" + reason
				if reason != "nonexistent" {
					channel := model.Channel{Name: name, Type: constant.ChannelTypeOpenAI, Key: "upstream-secret", Status: common.ChannelStatusEnabled, Group: "default", Models: name}
					if reason == "channel-off" {
						channel.Status = common.ChannelStatusManuallyDisabled
					}
					require.NoError(t, model.DB.Create(&channel).Error)
					channelID := channel.Id
					if reason == "orphan" {
						channelID = 99999
					}
					require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: name, ChannelId: channelID, Enabled: reason != "ability-off"}).Error)
					if reason != "missing" {
						raw := productionXCodeConfig(protocol.apiType)
						switch reason {
						case "invalid":
							raw = `{"schema_version":1}`
						case "config-disabled":
							raw = strings.Replace(raw, `"enabled":true`, `"enabled":false`, 1)
						case "wrong-protocol":
							wrong := "openai-responses"
							if protocol.apiType == wrong {
								wrong = "openai-chat-completions"
							}
							raw = productionXCodeConfig(wrong)
						}
						require.NoError(t, model.DB.Create(&model.Model{ModelName: name, NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(raw)}).Error)
						if reason == "metadata-disabled" {
							require.NoError(t, model.DB.Model(&model.Model{}).Where("model_name = ?", name).Update("status", 0).Error)
						}
					}
				}
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"input":"hello",%q:1}`, name, protocol.field)
				response := productionRelayRequest(engine, protocol.path, "productionmanagedkey", body, true)
				require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
				assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				var payload map[string]any
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
				errorBody, ok := payload["error"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "XCODE_MODEL_UNAVAILABLE", errorBody["code"])
				assert.Contains(t, errorBody["message"], "production-admission-request")
				if protocol.path == "/v1/messages" {
					assert.Equal(t, "error", payload["type"])
					assert.Equal(t, "permission_error", errorBody["type"])
				} else {
					assert.NotContains(t, payload, "type")
				}
			})
		}
	}
	var reservations, tasks int64
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).Count(&reservations).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Count(&tasks).Error)
	assert.Zero(t, reservations)
	assert.Zero(t, tasks)
	var after model.UserSubscription
	require.NoError(t, model.DB.First(&after, sub.Id).Error)
	assert.Equal(t, sub.AmountUsed, after.AmountUsed)

	// Native unavailable-model selection retains its existing distributor status.
	response := productionRelayRequest(engine, "/v1/chat/completions", "productionnativekey", `{"model":"native-absent","messages":[{"role":"user","content":"hello"}]}`, false)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.NotContains(t, response.Body.String(), "XCODE_")
}

func TestManagedClaimedResponsesAdmissionPrecedesPluginHooksAndReservation(t *testing.T) {
	engine, sub := setupXCodeProductionRouter(t)
	const source = `
export const meta = {apiVersion:1,key:"production-admission",name:"Admission",version:"1.0.0",author:{name:"Test"},models:["claimed-admission-model"],fetchMode:"per_task",protocols:[{name:"openai_responses",supports:["sync"]}]};
export function buildSubmitRequest(ctx) { return {url:ctx.baseUrl+"/submit",method:"POST",body:ctx.requestBody}; }
export function parseSubmitResponse(ctx, response) { return {taskId:"vendor-task",taskData:response.body,immediate:{status:"SUCCESS"}}; }
export function buildQueryRequest(ctx) { return {url:ctx.baseUrl+"/query"}; }
export function parseTaskResult() { return {status:"SUCCESS"}; }
export function listArtifacts() { return []; }
export function buildContentRequest() { throw new Error("artifact_not_found"); }
export const protocols = {openai_responses: {
  decodeRequest: function(ctx) {
    if (ctx.body.value.input === "reject") throw new Error("Fixture input rejected by registered plugin");
    return {kind:"submit",model:ctx.model,requestBody:ctx.body.value};
  },
  renderFinal: function() { return {output:[]}; }
}};
`
	_, err := pluginruntime.DefaultRegistry.Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pluginruntime.DefaultRegistry.Unregister("production-admission")) })
	channel := model.Channel{Name: "claimed-admission", Type: constant.ChannelTypeTaskPlugin, Key: "upstream-secret", Status: common.ChannelStatusEnabled, Group: "default", Models: "claimed-admission-model", Setting: common.GetPointer(`{"task_plugin_key":"production-admission"}`)}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "claimed-admission-model", ChannelId: channel.Id, Enabled: true}).Error)
	metadata := model.Model{ModelName: "claimed-admission-model", NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(productionXCodeConfig("openai-responses"))}
	require.NoError(t, model.DB.Create(&metadata).Error)
	for _, tc := range []struct {
		name, config, limit, key string
		status                   int
		code                     string
		pluginReached            bool
	}{
		{"missing metadata", "", `,"max_output_tokens":1`, "productionmanagedkey", 403, "XCODE_MODEL_UNAVAILABLE", false},
		{"disabled metadata", strings.Replace(productionXCodeConfig("openai-responses"), `"enabled":true`, `"enabled":false`, 1), `,"max_output_tokens":1`, "productionmanagedkey", 403, "XCODE_MODEL_UNAVAILABLE", false},
		{"wrong protocol", productionXCodeConfig("openai-chat-completions"), `,"max_output_tokens":1`, "productionmanagedkey", 403, "XCODE_MODEL_UNAVAILABLE", false},
		{"absent output bound", productionXCodeConfig("openai-responses"), "", "productionmanagedkey", 400, "invalid_request", false},
		{"excessive output bound", productionXCodeConfig("openai-responses"), `,"max_output_tokens":129`, "productionmanagedkey", 400, "invalid_request", false},
		{"native safety bound", productionXCodeConfig("openai-responses"), `,"max_output_tokens":1073741824`, "productionmanagedkey", 400, "invalid_request", false},
		{"valid declared managed model", productionXCodeConfig("openai-responses"), `,"max_output_tokens":128`, "productionmanagedkey", 400, "", true},
		{"explicit zero retained", productionXCodeConfig("openai-responses"), `,"max_output_tokens":0`, "productionmanagedkey", 400, "", true},
		{"native without metadata or output bound", "", "", "productionnativekey", 400, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, model.DB.Model(&metadata).Update("x_code_config", tc.config).Error)
			body := `{"model":"claimed-admission-model","input":"reject"` + tc.limit + `}`
			response := productionRelayRequest(engine, "/v1/responses", tc.key, body, !tc.pluginReached)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.pluginReached {
				assert.Contains(t, response.Body.String(), "Fixture input rejected by registered plugin")
			} else {
				assert.NotContains(t, response.Body.String(), "Fixture input rejected by registered plugin")
				var payload map[string]any
				require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
				errorBody, ok := payload["error"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, tc.code, errorBody["code"])
				assert.Contains(t, errorBody["message"], "production-admission-request")
				assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			}
			var reservations, tasks int64
			require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).Count(&reservations).Error)
			require.NoError(t, model.DB.Model(&model.Task{}).Count(&tasks).Error)
			assert.Zero(t, reservations)
			assert.Zero(t, tasks)
			var after model.UserSubscription
			require.NoError(t, model.DB.First(&after, sub.Id).Error)
			assert.Equal(t, sub.AmountUsed, after.AmountUsed)
		})
	}
	service.InitHttpClient()
	previousPrices := ratio_setting.ModelPrice2JSONString()
	previousFreePreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	previousBatch, previousLog := common.BatchUpdateEnabled, common.LogConsumeEnabled
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"claimed-admission-model":0}`))
	t.Cleanup(func() {
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previousFreePreConsume
		common.BatchUpdateEnabled, common.LogConsumeEnabled = previousBatch, previousLog
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
	})
	var submissions atomic.Int32
	var repricedSubmissions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/submit", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		submissions.Add(1)
		var payload map[string]any
		if !assert.NoError(t, common.DecodeJson(r.Body, &payload)) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if payload["input"] == "paid-retry" && repricedSubmissions.Add(1) == 1 {
			if !assert.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"claimed-admission-model":0.001}`)) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"retry with updated estimate"}}`)
			return
		}
		var actual model.UserSubscription
		if assert.NoError(t, model.DB.First(&actual, sub.Id).Error) {
			assert.Equal(t, int64(7), actual.AmountUsed, "a free task must not occupy designated quota while upstream is running")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":"fixture completed"}`)
	}))
	t.Cleanup(upstream.Close)
	require.NoError(t, model.DB.Model(&channel).Update("base_url", upstream.URL).Error)
	for _, managed := range []bool{true, false} {
		t.Run(fmt.Sprintf("successful plugin dispatch/managed=%t", managed), func(t *testing.T) {
			key, limit, raw := "productionnativekey", "", ""
			if managed {
				key, limit, raw = "productionmanagedkey", `,"max_output_tokens":1`, productionXCodeConfig("openai-responses")
			}
			require.NoError(t, model.DB.Model(&metadata).Update("x_code_config", raw).Error)
			before := submissions.Load()
			response := productionRelayRequest(engine, "/v1/responses", key, `{"model":"claimed-admission-model","input":"hello"`+limit+`}`, false)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Equal(t, before+1, submissions.Load())
			var payload map[string]any
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.Equal(t, "completed", payload["status"])
			assert.Equal(t, "claimed-admission-model", payload["model"])
			var persisted model.Task
			require.NoError(t, model.DB.Order("id DESC").First(&persisted).Error)
			assert.Equal(t, constant.TaskPlatform("production-admission"), persisted.Platform)
			assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), persisted.Status)
			if managed {
				var reservation model.SubscriptionPreConsumeRecord
				require.NoError(t, model.DB.Where("user_id = ? AND funding_scope = ?", sub.UserId, model.SubscriptionUsageScopeXCode).First(&reservation).Error)
				assert.Equal(t, sub.Id, reservation.UserSubscriptionId)
				assert.Equal(t, "settled", reservation.Status)
				assert.Zero(t, reservation.FinalQuota)
				require.NoError(t, model.DB.First(&sub, sub.Id).Error)
				assert.Equal(t, int64(7), sub.AmountUsed, "a free managed task still completes its designated funding fact without charge")
			}
		})
	}
	t.Run("paid retry is reserved before another upstream submission", func(t *testing.T) {
		previousRetries := common.RetryTimes
		common.RetryTimes = 1
		t.Cleanup(func() { common.RetryTimes = previousRetries })
		require.NoError(t, model.DB.Model(&metadata).Update("x_code_config", productionXCodeConfig("openai-responses")).Error)
		response := productionRelayRequest(engine, "/v1/responses", "productionmanagedkey", `{"model":"claimed-admission-model","input":"paid-retry","max_output_tokens":1}`, false)
		assert.Equal(t, http.StatusTooManyRequests, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "XCODE_QUOTA_EXHAUSTED")
		assert.Equal(t, int32(1), repricedSubmissions.Load(), "insufficient paid retry must never be submitted upstream")
		var tasks int64
		require.NoError(t, model.DB.Model(&model.Task{}).Count(&tasks).Error)
		assert.EqualValues(t, 2, tasks, "only the two preceding completed tasks are persisted")
	})
}

func TestManagedProductionAdmissionPreservesBodySizePrecedence(t *testing.T) {
	engine, _ := setupXCodeProductionRouter(t)
	previousLimit := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	t.Cleanup(func() { constant.MaxRequestBodyMB = previousLimit })
	body := `{"model":"unavailable","input":"hello","max_output_tokens":1,"max_tokens":1,"messages":[{"role":"user","content":"hello"}]}` + strings.Repeat(" ", 1<<20)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			response := productionRelayRequest(engine, path, "productionmanagedkey", body, true)
			require.Equal(t, http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
			assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			assert.NotContains(t, response.Body.String(), "XCODE_MODEL_UNAVAILABLE")
			var payload map[string]any
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			errorBody, ok := payload["error"].(map[string]any)
			require.True(t, ok)
			assert.Contains(t, errorBody["message"], "production-admission-request")
			if path == "/v1/messages" {
				assert.Equal(t, "error", payload["type"])
				assert.Equal(t, "invalid_request_error", errorBody["type"])
			} else {
				assert.NotContains(t, payload, "type")
			}
		})
	}
}

func TestManagedProductionRelayPreservesOutputLimitsAndLiveGroupRouting(t *testing.T) {
	engine, sub := setupXCodeProductionRouter(t)
	service.InitHttpClient()
	previousRatios := ratio_setting.ModelRatio2JSONString()
	previousCountToken, previousBatch, previousLog := constant.CountToken, common.BatchUpdateEnabled, common.LogConsumeEnabled
	previousFreePreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	constant.CountToken, common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false, false
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"admission-reuse-chat":0,"admission-reuse-responses":0}`))
	t.Cleanup(func() {
		constant.CountToken, common.BatchUpdateEnabled, common.LogConsumeEnabled = previousCountToken, previousBatch, previousLog
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previousFreePreConsume
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
	})
	var calls atomic.Int32
	var expectLimit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var actual model.UserSubscription
		if assert.NoError(t, model.DB.First(&actual, sub.Id).Error) {
			assert.Equal(t, int64(7), actual.AmountUsed, "a free relay must not occupy designated quota while upstream is running")
		}
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if !assert.NoError(t, common.Unmarshal(body, &payload)) {
			http.Error(w, "invalid JSON", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses" {
			_, present := payload["max_output_tokens"]
			assert.Equal(t, expectLimit.Load(), present, "explicit zero and native omission retain their distinct forwarding semantics")
			if limit, present := payload["max_output_tokens"]; present {
				assert.Equal(t, float64(0), limit)
			}
			assert.Equal(t, false, payload["store"])
			_, _ = io.WriteString(w, `{"id":"resp_fixture","object":"response","status":"completed","model":"admission-reuse-responses","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`)
		} else {
			_, present := payload["max_tokens"]
			assert.Equal(t, expectLimit.Load(), present, "explicit zero and native omission retain their distinct forwarding semantics")
			if limit, present := payload["max_tokens"]; present {
				assert.Equal(t, float64(0), limit)
			}
			_, _ = io.WriteString(w, `{"id":"chat_fixture","object":"chat.completion","created":1,"model":"admission-reuse-chat","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	for _, protocol := range []struct{ path, name, apiType, field string }{
		{"/v1/chat/completions", "admission-reuse-chat", "openai-chat-completions", "max_tokens"},
		{"/v1/responses", "admission-reuse-responses", "openai-responses", "max_output_tokens"},
	} {
		channel := model.Channel{Name: protocol.name, Type: constant.ChannelTypeOpenAI, Key: "upstream-secret", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Group: "default", Models: protocol.name}
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: protocol.name, ChannelId: channel.Id, Enabled: true}).Error)
		require.NoError(t, model.DB.Create(&model.Model{ModelName: protocol.name, NameRule: model.NameRuleExact, Status: 1, XCodeConfig: model.LongText(productionXCodeConfig(protocol.apiType))}).Error)
		for _, managed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/managed=%t", protocol.apiType, managed), func(t *testing.T) {
				key, limit := "productionnativekey", ""
				if managed {
					key, limit = "productionmanagedkey", fmt.Sprintf(`,%q:0`, protocol.field)
				}
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"input":"hello","store":false%s}`, protocol.name, limit)
				expectLimit.Store(managed)
				before := calls.Load()
				response := productionRelayRequest(engine, protocol.path, key, body, false)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				assert.Equal(t, before+1, calls.Load())
			})
		}
	}
	t.Run("warm user cache cannot route managed requests or retries to the old group", func(t *testing.T) {
		redisServer := miniredis.RunT(t)
		redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
		previousRedis, previousClient, previousRetries := common.RedisEnabled, common.RDB, common.RetryTimes
		common.RedisEnabled, common.RDB, common.RetryTimes = true, redisClient, 1
		t.Cleanup(func() {
			common.RedisEnabled, common.RDB, common.RetryTimes = previousRedis, previousClient, previousRetries
			require.NoError(t, redisClient.Close())
		})
		var user model.User
		require.NoError(t, model.DB.Where("username = ?", "production-xcode-user").First(&user).Error)
		cached, err := model.GetUserCache(user.Id)
		require.NoError(t, err)
		require.Equal(t, "default", cached.Group)
		require.NoError(t, model.DB.Model(&user).Update("group", "admission-live").Error)
		cached, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		require.Equal(t, "default", cached.Group, "the fixture must retain a genuinely warm stale authentication cache")
		var newGroupCalls atomic.Int32
		newUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if newGroupCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":{"message":"retry fixture","type":"api_error","code":"provider_busy"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"chat_live_group","object":"chat.completion","created":1,"model":"admission-reuse-chat","choices":[{"index":0,"message":{"role":"assistant","content":"live group"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
		}))
		t.Cleanup(newUpstream.Close)
		channel := model.Channel{Name: "live-group-channel", Type: constant.ChannelTypeOpenAI, Key: "upstream-secret", BaseURL: &newUpstream.URL, Status: common.ChannelStatusEnabled, Group: "admission-live", Models: "admission-reuse-chat"}
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{Group: "admission-live", Model: "admission-reuse-chat", ChannelId: channel.Id, Enabled: true}).Error)
		body := `{"model":"admission-reuse-chat","messages":[{"role":"user","content":"hello"}],"max_tokens":0}`
		expectLimit.Store(true)
		oldGroupCalls := calls.Load()
		response := productionRelayRequest(engine, "/v1/chat/completions", "productionmanagedkey", body, false)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "live group")
		assert.Equal(t, int32(2), newGroupCalls.Load(), "the initial selection and retry both stay in the live DB group")
		assert.Equal(t, oldGroupCalls, calls.Load(), "neither selection may use the cached old group")
		cached, err = model.GetUserCache(user.Id)
		require.NoError(t, err)
		assert.Equal(t, "default", cached.Group, "managed admission changes request-local routing, not shared native cache state")
		var managedToken model.Token
		require.NoError(t, model.DB.Where("key = ?", "productionmanagedkey").First(&managedToken).Error)
		assert.Empty(t, managedToken.Group, "request admission must not rewrite stored key representation")

		// Native requests continue using the existing cached-group routing policy.
		response = productionRelayRequest(engine, "/v1/chat/completions", "productionnativekey", body, false)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Equal(t, oldGroupCalls+1, calls.Load())
		assert.Equal(t, int32(2), newGroupCalls.Load())
	})
	var reservations []model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("user_id = ? AND funding_scope = ?", sub.UserId, model.SubscriptionUsageScopeXCode).Find(&reservations).Error)
	require.Len(t, reservations, 3, "each successful managed HTTP request, including the retry, has one designated funding fact")
	for _, reservation := range reservations {
		assert.Equal(t, sub.Id, reservation.UserSubscriptionId)
		assert.Equal(t, "settled", reservation.Status)
		assert.Zero(t, reservation.FinalQuota)
	}
	require.NoError(t, model.DB.First(&sub, sub.Id).Error)
	assert.Equal(t, int64(7), sub.AmountUsed)
	var user model.User
	require.NoError(t, model.DB.First(&user, sub.UserId).Error)
	assert.Equal(t, 777, user.Quota, "free managed relay must not touch the native wallet")
}

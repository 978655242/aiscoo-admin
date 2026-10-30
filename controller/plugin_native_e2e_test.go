package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type nativeRouteBilling struct {
	events      []string
	preConsumed int
	userID      int
	settled     bool
}

func (b *nativeRouteBilling) Settle(int) error {
	b.events = append(b.events, "settle")
	b.settled = true
	return nil
}

func (b *nativeRouteBilling) Refund(*gin.Context) {
	b.events = append(b.events, "refund")
	if !b.settled && b.preConsumed > 0 {
		_ = model.IncreaseUserQuota(b.userID, b.preConsumed, true)
		b.preConsumed = 0
	}
}

func (b *nativeRouteBilling) NeedsRefund() bool {
	return !b.settled && b.preConsumed > 0
}

func (b *nativeRouteBilling) GetPreConsumedQuota() int {
	return b.preConsumed
}

func (b *nativeRouteBilling) Reserve(quota int) error {
	b.events = append(b.events, "reserve")
	if err := model.DecreaseUserQuota(b.userID, quota, true); err != nil {
		return err
	}
	b.preConsumed = quota
	return nil
}

// ReserveBeforeSubmit holds nothing: this fixture charges in Reserve once the
// upstream has accepted.
func (*nativeRouteBilling) ReserveBeforeSubmit(int) error { return nil }

func TestKlingNativeRouteSubmitPollSettleAndQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousBatchUpdate := common.BatchUpdateEnabled
	previousLogConsume := common.LogConsumeEnabled
	previousRedisEnabled := common.RedisEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Channel{}, &model.Task{}, &model.Log{}))
	model.DB = database
	model.LOG_DB = database
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = false
	common.RedisEnabled = false
	previousModelRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"kling-v1":1}`))
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.BatchUpdateEnabled = previousBatchUpdate
		common.LogConsumeEnabled = previousLogConsume
		common.RedisEnabled = previousRedisEnabled
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModelRatios))
	})
	require.NoError(t, database.Create(&model.User{
		Id:       7,
		Username: "native-route-user",
		Group:    "default",
		Quota:    1_000_000,
	}).Error)

	var submitCalls atomic.Int32
	var queryCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/kling/v1/videos/text2video":
			submitCalls.Add(1)
			body, readErr := io.ReadAll(r.Body)
			if !assert.NoError(t, readErr) {
				http.Error(w, "read request", http.StatusInternalServerError)
				return
			}
			assert.Contains(t, string(body), `"model_name":"kling-v1"`)
			_, _ = io.WriteString(w, `{"code":0,"message":"","data":{"task_id":"kling-private-1","task_status":"submitted"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/kling/v1/videos/text2video/kling-private-1":
			queryCalls.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"message":"","data":{"task_id":"kling-private-1","task_status":"succeed","task_status_msg":"","task_result":{"videos":[{"id":"video-private","url":"https://cdn.example/video.mp4","duration":"5"}]},"final_unit_deduction":"1"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	channel := model.Channel{
		Type:    constant.ChannelTypeKling,
		Name:    "kling-native-e2e",
		Key:     "sk-test",
		BaseURL: &upstream.URL,
		Status:  common.ChannelStatusEnabled,
		Models:  "kling-v1",
		Group:   "default",
	}
	require.NoError(t, database.Create(&channel).Error)

	generation := pluginruntime.DefaultRegistry.Generation()
	require.NotNil(t, generation)
	submitBinding, found := generation.LookupDeclaredRoute(http.MethodPost, "/kling/v1/videos/text2video")
	require.True(t, found)
	require.Equal(t, "kling", submitBinding.Plugin.Meta.Key)

	submitRecorder := httptest.NewRecorder()
	submitContext, _ := gin.CreateTestContext(submitRecorder)
	submitContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/kling/v1/videos/text2video",
		bytes.NewBufferString(`{"model_name":"kling-v1","prompt":"a lighthouse"}`),
	)
	submitContext.Request.Header.Set("Content-Type", "application/json")
	submitContext.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{
		Generation: generation,
		Plugin:     submitBinding.Plugin,
		Route:      submitBinding.Route,
	})
	common.SetContextKey(submitContext, constant.ContextKeyUserId, 7)
	common.SetContextKey(submitContext, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(submitContext, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(submitContext, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(submitContext, constant.ContextKeyUserQuota, 1_000_000)

	middleware.PrepareTaskPluginRoute()(submitContext)
	require.False(t, submitContext.IsAborted(), submitRecorder.Body.String())
	require.Equal(t, "kling-v1", submitContext.GetString("resolved_task_model"))
	require.Equal(t, "text_to_video", submitContext.GetString("task_action"))
	require.Nil(t, middleware.SetupContextForSelectedChannel(submitContext, &channel, "kling-v1"))

	billing := &nativeRouteBilling{userID: 7}
	relayInfo := &relaycommon.RelayInfo{
		UserId:          7,
		UserGroup:       "default",
		UsingGroup:      "default",
		UserQuota:       1_000_000,
		TokenGroup:      "default",
		OriginModelName: "kling-v1",
		Billing:         billing,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			Action:        submitContext.GetString("task_action"),
			PublicTaskID:  "task_kling_public",
			LockedChannel: &channel,
		},
	}

	outcome, taskErr := executeTaskSubmissionWith(submitContext, relayInfo, relay.RelayTaskSubmit)
	require.Nil(t, taskErr)
	require.NotNil(t, outcome)
	require.Equal(t, []string{"reserve", "settle"}, billing.events)
	require.False(t, submitContext.Writer.Written())

	presentTaskSubmission(submitContext, outcome)
	require.Equal(t, http.StatusOK, submitRecorder.Code)
	assert.Contains(t, submitRecorder.Body.String(), `"task_id":"task_kling_public"`)
	assert.NotContains(t, submitRecorder.Body.String(), "kling-private-1")
	assert.Equal(t, int32(1), submitCalls.Load())

	var persisted model.Task
	require.NoError(t, database.Where("task_id = ?", "task_kling_public").First(&persisted).Error)
	assert.Equal(t, constant.TaskPlatform("kling"), persisted.Platform)
	assert.Equal(t, "kling-private-1", persisted.PrivateData.UpstreamTaskID)
	assert.Equal(t, model.TaskStatus(model.TaskStatusNotStart), persisted.Status)

	previousAdaptorFactory := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
		return relay.GetTaskAdaptor(platform)
	}
	t.Cleanup(func() { service.GetTaskAdaptorFunc = previousAdaptorFactory })
	service.DispatchPlatformUpdate(
		context.Background(),
		persisted.Platform,
		map[int][]string{channel.Id: {"kling-private-1"}},
		map[string]*model.Task{"kling-private-1": &persisted},
	)

	require.NoError(t, database.Where("task_id = ?", "task_kling_public").First(&persisted).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), persisted.Status)
	assert.Equal(t, "100%", persisted.Progress)
	assert.Equal(t, 1, persisted.Quota)
	assert.Equal(t, int32(1), queryCalls.Load())
	var settledUser model.User
	require.NoError(t, database.First(&settledUser, 7).Error)
	assert.Equal(t, 999_999, settledUser.Quota)

	queryBinding, found := generation.LookupDeclaredRoute(http.MethodGet, "/kling/v1/videos/text2video/:task_id")
	require.True(t, found)
	queryRecorder := httptest.NewRecorder()
	queryContext, _ := gin.CreateTestContext(queryRecorder)
	queryContext.Request = httptest.NewRequest(http.MethodGet, "/kling/v1/videos/text2video/task_kling_public", nil)
	queryContext.Params = gin.Params{{Key: "task_id", Value: "task_kling_public"}}
	queryContext.Set(pluginruntime.ContextKeyPinnedRoute, pluginruntime.PinnedRoute{
		Generation: generation,
		Plugin:     queryBinding.Plugin,
		Route:      queryBinding.Route,
	})
	common.SetContextKey(queryContext, constant.ContextKeyUserId, 7)

	middleware.PrepareTaskPluginRoute()(queryContext)

	require.True(t, queryContext.IsAborted())
	require.Equal(t, http.StatusOK, queryRecorder.Code)
	assert.Contains(t, queryRecorder.Body.String(), `"task_id":"task_kling_public"`)
	assert.Contains(t, queryRecorder.Body.String(), `"task_status":"succeed"`)
	assert.NotContains(t, queryRecorder.Body.String(), "kling-private-1")
	assert.NotContains(t, queryRecorder.Body.String(), upstream.URL)
}

// A Responses plugin's origin task pins its channel across retries. The
// managed authority must still be checked against the live ability each time.
func TestXCodeOriginTaskRetryRechecksChannelAuthority(t *testing.T) {
	service.InitHttpClient()
	db := modelManagementDB(t, "sqlite", "")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.Log{}, &model.XCodeAuthorization{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	oldRetries, oldErrorLog, oldConsume, oldExport, oldBatch := common.RetryTimes, constant.ErrorLogEnabled, common.LogConsumeEnabled, common.DataExportEnabled, common.BatchUpdateEnabled
	oldGroupRatios := ratio_setting.GroupRatio2JSONString()
	common.RetryTimes, constant.ErrorLogEnabled = 1, false
	common.LogConsumeEnabled, common.DataExportEnabled, common.BatchUpdateEnabled = false, false, false
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"origin-retry-model":0.001}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	t.Cleanup(func() {
		common.RetryTimes, constant.ErrorLogEnabled = oldRetries, oldErrorLog
		common.LogConsumeEnabled, common.DataExportEnabled, common.BatchUpdateEnabled = oldConsume, oldExport, oldBatch
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldGroupRatios))
	})

	registry := pluginruntime.NewRegistry()
	_, err = registry.Register(`
export const meta = {apiVersion:1,key:"origin-retry",name:"Origin retry",version:"1.0.0",author:{name:"Test"},models:["origin-retry-model"],fetchMode:"per_task",protocols:[{name:"openai_responses",supports:["sync"]}]};
export const protocols = {openai_responses:{
  decodeRequest(ctx){return {kind:"submit",model:ctx.model,originTaskIds:["origin-public"],requestBody:ctx.body.value};},
  renderFinal(){return {output:[]};}
}};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/jobs",method:"POST",body:{origin:ctx.originTasks[0].upstreamTaskId,channel:ctx.apiKey}};}
export function parseSubmitResponse(ctx,response){return {taskId:"new-private",taskData:response.body,immediate:{status:"SUCCESS",progress:"100%"}};}
export function buildQueryRequest(){throw new Error("unexpected query");}
export function parseTaskResult(){throw new Error("unexpected poll");}
`, pluginruntime.Options{})
	require.NoError(t, err)
	generation := registry.Generation()
	candidates := generation.LookupEndpointCandidates(http.MethodPost, "/v1/responses", "origin-retry-model")
	require.Len(t, candidates, 1)
	pinned := pluginruntime.PinnedEndpoint{Generation: generation, Plugin: candidates[0].Plugin, Protocol: candidates[0].Protocol, Operation: candidates[0].Operation, Model: "origin-retry-model", Candidates: candidates}

	var calls atomic.Int32
	var revoke, failAfterSubmit bool
	var failChannelLookup atomic.Bool
	var revokeErr error
	var submittedKeys []string
	var channelID int
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:origin-channel-failure", func(tx *gorm.DB) {
		if failChannelLookup.Load() && tx.Statement.Table == "channels" {
			tx.AddError(errors.New("injected channel lookup failure"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove("test:origin-channel-failure")) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if !assert.NoError(t, readErr) {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		var submitted map[string]any
		if !assert.NoError(t, common.Unmarshal(body, &submitted)) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		assert.Equal(t, "origin-private", submitted["origin"])
		key, _ := submitted["channel"].(string)
		submittedKeys = append(submittedKeys, key)
		if calls.Add(1) == 1 {
			if revoke {
				revokeErr = db.Model(&model.Ability{}).Where("channel_id = ?", channelID).Update("enabled", false).Error
			}
			if failAfterSubmit {
				failChannelLookup.Store(true)
			}
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"temporary"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":"ok"}`)
	}))
	t.Cleanup(upstream.Close)

	channel := model.Channel{Type: constant.ChannelTypeTaskPlugin, Name: "origin-retry", Key: "first-key\nsecond-key", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Models: pinned.Model, Group: "default", AutoBan: common.GetPointer(0)}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channel.SetSetting(kitdto.ChannelSettings{TaskPluginKey: "origin-retry"})
	require.NoError(t, db.Create(&channel).Error)
	channelID = channel.Id
	require.NoError(t, channel.AddAbilities(db))
	user := model.User{Username: "origin-retry-user", AffCode: "origin-retry-user", Group: "default", Status: common.UserStatusEnabled, Quota: 10000}
	require.NoError(t, db.Create(&user).Error)
	origin := model.Task{TaskID: "origin-public", UserId: user.Id, ChannelId: channel.Id, Platform: "origin-retry", Status: model.TaskStatusSuccess, PrivateData: model.TaskPrivateData{UpstreamTaskID: "origin-private"}}
	require.NoError(t, db.Create(&origin).Error)
	now := model.GetDBTimestamp()
	subscription := model.UserSubscription{UserId: user.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 3600, AmountTotal: 10000, QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
	require.NoError(t, db.Create(&subscription).Error)
	require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: subscription.Id, Revision: 1}).Error)

	for _, tc := range []struct {
		name       string
		managed    bool
		revoke     bool
		failLookup bool
		status     int
		calls      int32
	}{
		{name: "managed still authorized", managed: true, status: http.StatusOK, calls: 2},
		{name: "ordinary retry unchanged", revoke: true, status: http.StatusOK, calls: 2},
		{name: "managed lookup failure", managed: true, failLookup: true, status: http.StatusServiceUnavailable, calls: 1},
		{name: "managed revoked", managed: true, revoke: true, status: http.StatusForbidden, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls.Store(0)
			submittedKeys = nil
			revoke, failAfterSubmit, revokeErr = tc.revoke, tc.failLookup, nil
			failChannelLookup.Store(false)
			require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", channel.Id).Update("enabled", true).Error)
			// Reset the polling cursor, then warm the native cache before revocation.
			common.MemoryCacheEnabled = false
			cached, err := model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			cached.ChannelInfo.MultiKeyPollingIndex = 0
			require.NoError(t, cached.SaveChannelInfo())
			common.MemoryCacheEnabled = true
			model.InitChannelCache()
			cached, err = model.CacheGetChannel(channel.Id)
			require.NoError(t, err)
			cached.ChannelInfo.MultiKeyPollingIndex = 0
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"origin-retry-model","input":"hello"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, common.RequestIdKey, "origin-retry-"+tc.name)
			common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
			common.SetContextKey(c, constant.ContextKeyTokenId, 81)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserSetting, kitdto.UserSetting{BillingPreference: "wallet_only"})
			if tc.managed {
				common.SetContextKey(c, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
				common.SetContextKey(c, constant.ContextKeyFundingScope, model.SubscriptionUsageScopeXCode)
				common.SetContextKey(c, constant.ContextKeyFundingSubscriptionID, subscription.Id)
				common.SetContextKey(c, constant.ContextKeyFundingAuthorizationRevision, int64(1))
				service.GetChannelConstraints(c).AddFilter(dto.ChannelFilter{Kind: dto.FilterXCodeAuthority, Group: "default", Model: pinned.Model, RequestPath: "/v1/responses"})
			}
			c.Set(pluginruntime.ContextKeyPinnedEndpoint, pinned)
			c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: generation, Plugin: pinned.Plugin})
			middleware.PrepareTaskPluginEndpoint()(c)
			require.False(t, c.IsAborted(), recorder.Body.String())
			selected, _, selectErr := service.SelectChannelForRequest(c, pinned.Model, &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: pinned.Model})
			require.Nil(t, selectErr)
			require.Equal(t, channel.Id, selected.Id)
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, selected, pinned.Model))
			deps := pluginProtocolTestDeps()
			deps.submit = func(c *gin.Context, info *relaycommon.RelayInfo) (*taskSubmissionOutcome, *dto.TaskError) {
				info.IsPlayground = true
				return executeTaskSubmission(c, info)
			}
			serveTaskPluginProtocol(c, pinned, deps)
			failChannelLookup.Store(false)
			require.NoError(t, revokeErr)
			assert.Equal(t, tc.calls, calls.Load(), "only authorized attempts may reach the pinned upstream")
			if assert.NotEmpty(t, submittedKeys) {
				assert.Equal(t, "first-key", submittedKeys[0], "the first attempt uses the key already selected by distribution")
			}
			assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			if tc.managed && tc.status != http.StatusOK {
				if tc.revoke {
					assert.Contains(t, recorder.Body.String(), "XCODE_MODEL_UNAVAILABLE")
				} else {
					assert.Contains(t, recorder.Body.String(), "XCODE_BACKEND_UNAVAILABLE")
				}
				require.Eventually(t, func() bool {
					var record model.SubscriptionPreConsumeRecord
					return db.Where("request_id = ?", common.GetContextKeyString(c, common.RequestIdKey)).First(&record).Error == nil && record.Status == "refunded"
				}, 3*time.Second, 10*time.Millisecond, "managed reservation must finish refunding before database cleanup")
			}
		})
	}
}

package relay

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	kittypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRelayChannelDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousCache := common.MemoryCacheEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.Channel{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled = previousCache
		require.NoError(t, sqlDB.Close())
	})
	return database
}

func TestRealtimeFetchAppliesChannelHeaderOverrides(t *testing.T) {
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Task{}))
	service.InitHttpClient()
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"name":"operations/task-1","done":false}`))
	}))
	t.Cleanup(server.Close)
	headerOverride := `{"X-API-Key":"{api_key}"}`
	channel := &model.Channel{
		Name: "realtime-header-override", Type: constant.ChannelTypeGemini,
		Key: "channel-key", BaseURL: &server.URL, HeaderOverride: &headerOverride,
	}
	require.NoError(t, database.Create(channel).Error)
	task := &model.Task{
		TaskID: "public-task", ChannelId: channel.Id,
		Status: model.TaskStatusInProgress, Progress: "50%",
		PrivateData: model.TaskPrivateData{
			Key: "saved-key", UpstreamTaskID: base64.RawURLEncoding.EncodeToString([]byte("operations/task-1")),
		},
	}
	require.NoError(t, database.Create(task).Error)

	tryRealtimeFetch(task, true)

	require.Len(t, requests, 1)
	request := <-requests
	assert.Equal(t, "/v1beta/operations/task-1", request.URL.Path)
	assert.Equal(t, "saved-key", request.Header.Get("X-API-Key"))
	assert.Equal(t, "saved-key", request.Header.Get("X-Goog-Api-Key"))
}

func TestRealtimeFetchRefundsTaskItFindsFailed(t *testing.T) {
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Task{}, &model.User{}, &model.Log{}, &model.Ability{}))
	// Simulate another instance creating the channel after this instance's cache sync.
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	previousLogDB := model.LOG_DB
	previousFactory := service.GetTaskAdaptorFunc
	previousRedis := common.RedisEnabled
	model.LOG_DB = database
	common.RedisEnabled = false
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
		if adaptor := GetTaskAdaptor(platform); adaptor != nil {
			return adaptor
		}
		return nil
	}
	t.Cleanup(func() {
		model.LOG_DB = previousLogDB
		service.GetTaskAdaptorFunc = previousFactory
		common.RedisEnabled = previousRedis
	})
	service.InitHttpClient()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"name":"operations/task-1","done":true,"error":{"code":3,"message":"blocked by safety"}}`))
	}))
	t.Cleanup(server.Close)
	channel := &model.Channel{Name: "realtime-refund", Type: constant.ChannelTypeGemini, Key: "channel-key", BaseURL: &server.URL}
	require.NoError(t, database.Create(channel).Error)
	require.NoError(t, database.Create(&model.User{Id: 7, Username: "realtime-owner", Quota: 100}).Error)
	task := &model.Task{
		TaskID: "public-task", Platform: constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeGemini)),
		UserId: 7, ChannelId: channel.Id, Quota: 1000,
		Status: model.TaskStatusInProgress, Progress: "50%",
		PrivateData: model.TaskPrivateData{
			Key: "channel-key", UpstreamTaskID: base64.RawURLEncoding.EncodeToString([]byte("operations/task-1")),
		},
	}
	require.NoError(t, database.Create(task).Error)

	tryRealtimeFetch(task, true)

	var stored model.Task
	require.NoError(t, database.First(&stored, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusFailure), stored.Status)
	assert.Equal(t, "blocked by safety", stored.FailReason)
	assert.Zero(t, stored.Quota, "the failed task is refunded")
	assert.EqualValues(t, 1, calls.Load(), "the observed terminal result must not be fetched again")
	var user model.User
	require.NoError(t, database.First(&user, 7).Error)
	assert.Equal(t, 1100, user.Quota)
	var refunds int64
	require.NoError(t, database.Model(&model.Log{}).Where("type = ?", model.LogTypeRefund).Count(&refunds).Error)
	assert.EqualValues(t, 1, refunds)
}

func TestRealtimeFetchUsesFirstTerminalResponseAcrossPollers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		status    model.TaskStatus
		wantQuota int
		wantFunds int
	}{
		{name: "success", body: `{"name":"operations/task-2","done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"https://example.test/video"}}]}}}`, status: model.TaskStatusSuccess, wantQuota: 1000, wantFunds: 100},
		{name: "failure", body: `{"name":"operations/task-2","done":true,"error":{"code":3,"message":"upstream failed"}}`, status: model.TaskStatusFailure, wantQuota: 0, wantFunds: 1100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupRelayChannelDB(t)
			require.NoError(t, db.AutoMigrate(&model.Task{}, &model.User{}, &model.Log{}))
			previousLogDB, previousFactory, previousRedis := model.LOG_DB, service.GetTaskAdaptorFunc, common.RedisEnabled
			model.LOG_DB, common.RedisEnabled = db, false
			service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor {
				return GetTaskAdaptor(platform)
			}
			t.Cleanup(func() {
				model.LOG_DB, service.GetTaskAdaptorFunc, common.RedisEnabled = previousLogDB, previousFactory, previousRedis
			})
			service.InitHttpClient()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			ch := &model.Channel{Name: "realtime-first", Type: constant.ChannelTypeGemini, Key: "channel-key", BaseURL: &server.URL}
			require.NoError(t, db.Create(ch).Error)
			require.NoError(t, db.Create(&model.User{Id: 8, Username: "realtime-first", Quota: 100}).Error)
			task := &model.Task{TaskID: "public-first", Platform: constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeGemini)), UserId: 8, ChannelId: ch.Id, Quota: 1000, Status: model.TaskStatusInProgress, Progress: "50%", PrivateData: model.TaskPrivateData{UpstreamTaskID: base64.RawURLEncoding.EncodeToString([]byte("operations/task-2"))}}
			require.NoError(t, db.Create(task).Error)
			var stale model.Task
			require.NoError(t, db.First(&stale, task.ID).Error)

			response := tryRealtimeFetch(task, false)
			require.NotEmpty(t, response)
			var envelope dto.TaskResponse[map[string]any]
			require.NoError(t, common.Unmarshal(response, &envelope))
			if tc.status == model.TaskStatusSuccess {
				assert.Equal(t, "succeeded", envelope.Data["status"])
			} else {
				assert.Equal(t, "failed", envelope.Data["status"])
			}
			assert.EqualValues(t, 1, calls.Load(), "a second provider request is not required")
			var persisted model.Task
			require.NoError(t, db.First(&persisted, task.ID).Error)
			assert.Equal(t, tc.status, persisted.Status)
			assert.Equal(t, tc.wantQuota, persisted.Quota)
			var owner model.User
			require.NoError(t, db.First(&owner, 8).Error)
			assert.Equal(t, tc.wantFunds, owner.Quota)
			assert.JSONEq(t, tc.body, string(persisted.Data))

			// Another instance loaded the unfinished row before this transition.
			tryRealtimeFetch(&stale, true)
			require.NoError(t, db.First(&persisted, task.ID).Error)
			require.NoError(t, db.First(&owner, 8).Error)
			assert.Equal(t, tc.status, persisted.Status)
			assert.Equal(t, tc.wantQuota, persisted.Quota)
			assert.Equal(t, tc.wantFunds, owner.Quota)
			assert.Equal(t, persisted.Status, stale.Status)
			var refunds int64
			require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeRefund).Count(&refunds).Error)
			if tc.status == model.TaskStatusFailure {
				assert.EqualValues(t, 1, refunds)
			} else {
				assert.Zero(t, refunds)
			}
		})
	}
}

type terminalTestTransport func(*http.Request) (*http.Response, error)

func (f terminalTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTerminalVideoFetchHTTPPreservesProviderFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	credentials, err := common.Marshal(map[string]any{
		"project_id": "test-project", "client_email": "test@example.test",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})),
	})
	require.NoError(t, err)
	const vertexOperation = "projects/test-project/locations/us-central1/publishers/google/models/veo-3.0-generate-001/operations/task-1"
	for _, tc := range []struct {
		name       string
		channel    int
		platform   constant.TaskPlatform
		status     model.TaskStatus
		firstPoll  bool
		disabled   bool
		deleted    bool
		openai     bool
		body       string
		resultURL  string
		failReason string
		wantStatus string
		wantURL    string
		wantFormat string
		wantCalls  int32
		wantRefund int64
	}{
		{name: "Gemini live failure then stored", channel: constant.ChannelTypeGemini, platform: "google", status: model.TaskStatusFailure, firstPoll: true, body: `{"name":"operations/task-1","done":true,"error":{"code":3,"message":"blocked by safety"}}`, wantStatus: "failed", wantURL: "blocked by safety", wantFormat: "mp4", wantCalls: 1, wantRefund: 1},
		{name: "Gemini live success then stored", channel: constant.ChannelTypeGemini, platform: "24", status: model.TaskStatusSuccess, firstPoll: true, body: `{"name":"operations/task-1","done":true,"response":{"videos":[{"mimeType":"video/webm"}]}}`, wantStatus: "succeeded", wantURL: "http://localhost:3000/v1/videos/public-task/content", wantFormat: "video/webm", wantCalls: 1},
		{name: "Gemini background success", channel: constant.ChannelTypeGemini, platform: "google", status: model.TaskStatusSuccess, body: `{"response":{"videos":[{"mimeType":"video/webm"}]}}`, resultURL: "https://example.test/gemini", wantStatus: "succeeded", wantURL: "https://example.test/gemini", wantFormat: "video/webm"},
		{name: "Gemini background failure", channel: constant.ChannelTypeGemini, platform: "24", status: model.TaskStatusFailure, body: `{"error":{"message":"legacy failure"}}`, failReason: "legacy failure", wantStatus: "failed", wantURL: "legacy failure", wantFormat: "mp4"},
		{name: "Gemini empty historical data", channel: constant.ChannelTypeGemini, platform: "24", status: model.TaskStatusSuccess, resultURL: "https://example.test/empty", wantStatus: "succeeded", wantURL: "https://example.test/empty", wantFormat: "mp4"},
		{name: "Vertex live success then stored", channel: constant.ChannelTypeVertexAi, platform: "vertex-ai", status: model.TaskStatusSuccess, firstPoll: true, body: `{"name":"` + vertexOperation + `","done":true,"response":{"videos":[{"mimeType":"video/webm","bytesBase64Encoded":"aGVsbG8="}]}}`, wantStatus: "succeeded", wantURL: "http://localhost:3000/v1/videos/public-task/content", wantFormat: "video/webm", wantCalls: 1},
		{name: "Vertex live failure then stored", channel: constant.ChannelTypeVertexAi, platform: "41", status: model.TaskStatusFailure, firstPoll: true, body: `{"name":"` + vertexOperation + `","done":true,"error":{"code":3,"message":"blocked by safety"}}`, wantStatus: "failed", wantURL: "blocked by safety", wantFormat: "mp4", wantCalls: 1, wantRefund: 1},
		{name: "Vertex background success", channel: constant.ChannelTypeVertexAi, platform: "vertex-ai", status: model.TaskStatusSuccess, body: `{"response":{"videos":[{"mimeType":"video/webm"}]}}`, resultURL: "https://example.test/vertex", wantStatus: "succeeded", wantURL: "https://example.test/vertex", wantFormat: "video/webm"},
		{name: "Vertex background failure", channel: constant.ChannelTypeVertexAi, platform: "41", status: model.TaskStatusFailure, body: `{"response":{"videos":[{"mimeType":"video/quicktime"}]}}`, failReason: "vendor failure", wantStatus: "failed", wantURL: "vendor failure", wantFormat: "video/quicktime"},
		{name: "disabled legacy channel", channel: constant.ChannelTypeVertexAi, platform: "41", status: model.TaskStatusSuccess, disabled: true, body: `{"response":{"videos":[{"mimeType":"video/webm"}]}}`, failReason: "https://example.test/legacy", wantStatus: "succeeded", wantURL: "https://example.test/legacy", wantFormat: "video/webm"},
		{name: "deleted Gemini channel falls back", channel: constant.ChannelTypeGemini, platform: "24", status: model.TaskStatusSuccess, deleted: true, body: `{"response":{"videos":[{"mimeType":"video/webm"}]}}`, resultURL: "https://example.test/deleted", wantStatus: "SUCCESS", wantURL: "https://example.test/deleted"},
		{name: "same plugin on New API remains generic", channel: constant.ChannelTypeNewAPI, platform: "google", status: model.TaskStatusSuccess, body: `{"response":{"videos":[{"mimeType":"video/webm"}]}}`, resultURL: "https://example.test/gateway", wantStatus: "SUCCESS", wantURL: "https://example.test/gateway"},
		{name: "other provider remains generic", channel: constant.ChannelTypeKling, platform: "kling", status: model.TaskStatusFailure, body: `{}`, failReason: "failed", wantStatus: "FAILURE", wantURL: "failed"},
		{name: "OpenAI video uses adaptor", channel: constant.ChannelTypeGemini, platform: "google", status: model.TaskStatusSuccess, openai: true, body: `{}`, resultURL: "https://example.test/video", wantStatus: "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupRelayChannelDB(t)
			require.NoError(t, db.AutoMigrate(&model.Task{}, &model.User{}, &model.Log{}))
			previousLogDB, previousRedis, previousBatch := model.LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled
			model.LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = db, false, false
			t.Cleanup(func() {
				model.LOG_DB, common.RedisEnabled, common.BatchUpdateEnabled = previousLogDB, previousRedis, previousBatch
			})
			service.InitHttpClient()
			var calls, authCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth2/v4/token" {
					authCalls.Add(1)
					_, _ = w.Write([]byte(`{"access_token":"local-token"}`))
					return
				}
				calls.Add(1)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(upstream.Close)
			localURL, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			client := service.GetHttpClient()
			originalTransport := client.Transport
			client.Transport = terminalTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Scheme == "https" && req.URL.Host == "www.googleapis.com" && req.URL.Path == "/oauth2/v4/token" {
					req = req.Clone(req.Context())
					req.URL.Scheme, req.URL.Host = localURL.Scheme, localURL.Host
				} else if req.URL.Scheme != "http" || req.URL.Host != localURL.Host {
					return nil, fmt.Errorf("test blocked nonlocal request to %s", req.URL.Host)
				}
				return originalTransport.RoundTrip(req)
			})
			t.Cleanup(func() { client.Transport = originalTransport })
			ch := &model.Channel{Name: "terminal-http", Type: tc.channel, Key: "channel-key", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled}
			operation := "operations/task-1"
			if tc.channel == constant.ChannelTypeVertexAi && tc.firstPoll {
				ch.Key = string(credentials)
				operation = vertexOperation
			}
			if tc.disabled {
				ch.Status = common.ChannelStatusManuallyDisabled
			}
			require.NoError(t, db.Create(ch).Error)
			if tc.deleted {
				require.NoError(t, db.Delete(ch).Error)
			}
			require.NoError(t, db.Create(&model.User{Id: 9, Username: "terminal-owner", Quota: 100}).Error)
			status, progress, quota, data := tc.status, "100%", 1000, []byte(tc.body)
			if tc.firstPoll {
				status, progress, data = model.TaskStatusInProgress, "50%", []byte(`{}`)
			} else if tc.status == model.TaskStatusFailure {
				quota = 0
			}
			task := &model.Task{TaskID: "public-task", Platform: tc.platform, UserId: 9, ChannelId: ch.Id, Quota: quota, Status: status, Progress: progress, FailReason: tc.failReason, Data: data, PrivateData: model.TaskPrivateData{ResultURL: tc.resultURL, UpstreamTaskID: base64.RawURLEncoding.EncodeToString([]byte(operation))}}
			require.NoError(t, db.Create(task).Error)
			router := gin.New()
			handler := func(c *gin.Context) {
				c.Set("id", 9)
				if taskErr := RelayTaskFetch(c, relayconstant.RelayModeVideoFetchByID); taskErr != nil {
					c.JSON(taskErr.StatusCode, taskErr)
				}
			}
			router.GET("/v1/video/generations/:task_id", handler)
			router.GET("/v1/videos/:task_id", handler)
			gateway := httptest.NewServer(router)
			t.Cleanup(gateway.Close)
			path := "/v1/video/generations/public-task"
			if tc.openai {
				path = "/v1/videos/public-task"
			}
			for range 2 {
				resp, err := gateway.Client().Get(gateway.URL + path)
				require.NoError(t, err)
				var body map[string]any
				require.NoError(t, common.DecodeJson(resp.Body, &body))
				require.NoError(t, resp.Body.Close())
				require.Equal(t, http.StatusOK, resp.StatusCode)
				if tc.openai {
					assert.Equal(t, tc.wantStatus, body["status"])
					assert.Equal(t, "video", body["object"])
				} else {
					data, ok := body["data"].(map[string]any)
					require.True(t, ok)
					assert.Equal(t, tc.wantStatus, data["status"])
					if tc.wantFormat != "" {
						assert.Equal(t, tc.wantFormat, data["format"])
						assert.Equal(t, tc.wantURL, data["url"])
						assert.Equal(t, "public-task", data["task_id"])
					} else {
						assert.Equal(t, tc.wantURL, data["result_url"])
						assert.NotContains(t, data, "format")
					}
				}
				assert.Equal(t, tc.wantCalls, calls.Load())
			}
			assert.LessOrEqual(t, authCalls.Load(), int32(1))
			var stored model.Task
			require.NoError(t, db.First(&stored, task.ID).Error)
			assert.Equal(t, tc.status, stored.Status)
			if tc.channel == constant.ChannelTypeVertexAi && tc.firstPoll && tc.status == model.TaskStatusSuccess {
				assert.Contains(t, string(stored.Data), `"mimeType":"video/webm"`)
				assert.NotContains(t, string(stored.Data), "bytesBase64Encoded")
			}
			var owner model.User
			require.NoError(t, db.First(&owner, 9).Error)
			assert.Equal(t, 100+int(tc.wantRefund)*1000, owner.Quota)
			var refunds int64
			require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeRefund).Count(&refunds).Error)
			assert.Equal(t, tc.wantRefund, refunds)
		})
	}
}

func TestApplyChannelPinPreservesOriginTasksAndRetryMode(t *testing.T) {
	database := setupRelayChannelDB(t)
	channel := &model.Channel{Name: "origin-channel", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeDoubaoVideo}
	require.NoError(t, database.Create(channel).Error)
	originTask := &model.Task{
		TaskID: "task-lock", ChannelId: channel.Id, Action: "text_to_video", Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-task-lock"},
		Data:        []byte(`{"id":"upstream-task-lock"}`),
	}

	for _, tc := range []struct {
		name     string
		tokenPin bool
		apply    func(*gin.Context, *relaycommon.RelayInfo) *dto.TaskError
	}{
		{name: "origin affinity", apply: ApplyOriginTaskAffinity},
		{name: "same channel retry", apply: ApplyChannelPin},
		{name: "token pin suppresses channel lock", tokenPin: true, apply: ApplyChannelPin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyOriginTasks, []*model.Task{originTask})
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel})
			if tc.tokenPin {
				constraints.AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
			}
			info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			require.Nil(t, tc.apply(c, info))
			if tc.tokenPin {
				assert.Nil(t, info.LockedChannel)
			} else {
				locked, ok := info.LockedChannel.(*model.Channel)
				require.True(t, ok)
				require.NotNil(t, locked)
				assert.Equal(t, channel.Id, locked.Id)
			}
			require.Len(t, info.OriginTasks, 1)
			assert.Equal(t, "task-lock", info.OriginTasks[0].TaskID)
			assert.Equal(t, "upstream-task-lock", info.OriginTasks[0].UpstreamTaskID)
			assert.Equal(t, "text_to_video", info.OriginTasks[0].Action)
			assert.Equal(t, string(model.TaskStatusSuccess), info.OriginTasks[0].Status)
			assert.Equal(t, []byte(originTask.Data), info.OriginTasks[0].Data)
		})
	}
}

func TestTaskModel2DtoNormalizesLegacyAction(t *testing.T) {
	task := &model.Task{Action: "firstTailGenerate"}

	dtoTask := TaskModel2Dto(task)

	assert.Equal(t, constant.TaskActionFirstTailToVideo, dtoTask.Action)
	assert.Equal(t, "firstTailGenerate", task.Action)
}

const mappingOrderSubmitPlugin = `
export const meta = {apiVersion:1,key:"maporder",name:"Map Order",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl+"/submit", method:"POST", body:{upstreamModel: ctx.upstreamModel, model: ctx.model}, action:"text_to_video"};
}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`

const mappingOrderRewritePlugin = `
export const meta = {apiVersion:1,key:"maporder-rw",name:"Map Order RW",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl+"/submit", method:"POST", body:{upstreamModel: ctx.upstreamModel}, rewriteModel:"rewritten"};
}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`

func pinMappingOrderPlugin(t *testing.T, c *gin.Context, source string) {
	t.Helper()
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: plugin})
}

func newTaskSubmitContext(t *testing.T, originalModel, mapping string) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	common.SetContextKey(c, constant.ContextKeyOriginalModel, originalModel)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://provider.example")
	if mapping != "" {
		c.Set("model_mapping", mapping)
	}
	c.Set("task_request", map[string]any{"prompt": "p"})
	return c, &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
}

func TestRelayTaskSubmitMapsBeforeValidateWhenOriginSet(t *testing.T) {
	const mapping = `{"alias-model":"mid-model","mid-model":"declared-model"}`

	c, info := newTaskSubmitContext(t, "alias-model", mapping)
	pinMappingOrderPlugin(t, c, mappingOrderSubmitPlugin)
	info.OriginModelName = "alias-model"

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, "alias-model", info.OriginModelName)
	assert.Equal(t, "declared-model", info.UpstreamModelName)
	assert.True(t, info.IsModelMapped)
}

func TestRelayTaskSubmitDeclaredNameWithoutMappingIsUnchanged(t *testing.T) {
	c, info := newTaskSubmitContext(t, "declared-model", "")
	pinMappingOrderPlugin(t, c, mappingOrderSubmitPlugin)
	info.OriginModelName = "declared-model"

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, "declared-model", info.OriginModelName)
	assert.Equal(t, "declared-model", info.UpstreamModelName)
	assert.False(t, info.IsModelMapped)
}

func TestRelayTaskSubmitDoesNotApplyMappingTwice(t *testing.T) {
	c, info := newTaskSubmitContext(t, "alias-model", `{"alias-model":"declared-model"}`)
	pinMappingOrderPlugin(t, c, mappingOrderRewritePlugin)
	info.OriginModelName = "alias-model"

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, "rewritten", info.UpstreamModelName, "late mapping would overwrite rewriteModel with the chain tail")
	assert.Equal(t, "alias-model", info.OriginModelName)
}

func TestRelayTaskSubmitEmptyOriginKeepsLateMapping(t *testing.T) {
	plugin, err := pluginruntime.NewRegistry().Register(mappingOrderSubmitPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	synthesized := service.CoverTaskActionToModelName(constant.TaskPlatform(plugin.Meta.Key), "text_to_video")
	c, info := newTaskSubmitContext(t, "pre-validate-upstream",
		`{"pre-validate-upstream":"should-not-apply-early","`+synthesized+`":"legacy-tail"}`)
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: plugin})
	info.OriginModelName = ""

	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	assert.Equal(t, "model_price_error", taskErr.Code)
	assert.Equal(t, synthesized, info.OriginModelName)
	assert.Equal(t, "legacy-tail", info.UpstreamModelName)
	assert.True(t, info.IsModelMapped)
}

const billingFallbackPlugin = `
export const meta = {apiVersion:1,key:"bill-fallback",name:"Bill Fallback",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl+"/submit", method:"POST", body:{upstreamModel: ctx.upstreamModel, model: ctx.model}, action:"text_to_video"};
}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`

func saveBillingConfig(t *testing.T) {
	t.Helper()
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
}

func TestRelayTaskSubmitAliasBillingIdentityAndExprFallback(t *testing.T) {
	const mapping = `{"alias-model":"declared-model"}`
	const aliasExpr = `tier("alias", 2)`
	const tailExpr = `tier("tail", 3)`
	const legacyExpr = `tier("legacy", u("old_units") * 2)`

	tests := []struct {
		name       string
		modes      map[string]string
		exprs      map[string]string
		wantTiered bool
		wantExpr   string
		source     string
	}{
		{
			name:       "alias own tiered wins",
			modes:      map[string]string{"alias-model": "tiered_expr", "declared-model": "tiered_expr"},
			exprs:      map[string]string{"alias-model": aliasExpr, "declared-model": tailExpr},
			wantTiered: true,
			wantExpr:   aliasExpr,
		},
		{
			name:       "fallback uses tail expr",
			modes:      map[string]string{"declared-model": "tiered_expr"},
			exprs:      map[string]string{"declared-model": tailExpr},
			wantTiered: true,
			wantExpr:   tailExpr,
		},
		{
			name:       "stored expression keeps running after schema narrows",
			modes:      map[string]string{"declared-model": "tiered_expr"},
			exprs:      map[string]string{"declared-model": legacyExpr},
			wantTiered: true,
			wantExpr:   legacyExpr,
			source: strings.Replace(billingFallbackPlugin, `fetchMode:"per_task"`, `fetchMode:"per_task", usageSchema:{old_units:{type:"number",unit:"count"}}, usageProfiles:[{models:["declared-model"],schema:{seconds:{type:"number",unit:"second"}}}]`, 1) + `
export function extractUsage(){return {old_units:2};}
`,
		},
		{
			name:       "neither tiered uses ordinary pricing",
			wantTiered: false,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			saveBillingConfig(t)
			if len(testCase.modes) > 0 {
				modeJSON, marshalErr := common.Marshal(testCase.modes)
				require.NoError(t, marshalErr)
				exprJSON, marshalErr := common.Marshal(testCase.exprs)
				require.NoError(t, marshalErr)
				require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
					"billing_setting.billing_mode": string(modeJSON),
					"billing_setting.billing_expr": string(exprJSON),
				}))
				if testCase.wantExpr == aliasExpr {
					require.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("alias-model"))
				} else {
					require.Equal(t, billing_setting.BillingModeRatio, billing_setting.GetBillingMode("alias-model"))
					require.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("declared-model"))
				}
			}

			c, info := newTaskSubmitContext(t, "alias-model", mapping)
			c.Set("group", "default")
			info.UserGroup = "default"
			info.UsingGroup = "default"
			source := testCase.source
			if source == "" {
				source = billingFallbackPlugin
			}
			pinMappingOrderPlugin(t, c, source)
			info.OriginModelName = "alias-model"

			_, taskErr := RelayTaskSubmit(c, info)
			require.NotNil(t, taskErr)
			assert.Equal(t, "alias-model", info.OriginModelName)
			assert.Equal(t, "declared-model", info.UpstreamModelName)
			assert.True(t, info.IsModelMapped)

			task := model.InitTask(constant.TaskPlatform("bill-fallback"), info)
			assert.Equal(t, "alias-model", task.Properties.OriginModelName)
			assert.Equal(t, "declared-model", task.Properties.UpstreamModelName)

			if testCase.wantTiered {
				require.NotNil(t, info.TieredBillingSnapshot, "submission error: %+v", taskErr)
				assert.Equal(t, "alias-model", info.TieredBillingSnapshot.ModelName)
				assert.Equal(t, testCase.wantExpr, info.TieredBillingSnapshot.ExprString)
				assert.Equal(t, billingexpr.ExprHashString(testCase.wantExpr), info.TieredBillingSnapshot.ExprHash)
				assert.NotEqual(t, "model_price_error", taskErr.Code)
				if testCase.wantExpr == legacyExpr {
					assert.Equal(t, 4*common.QuotaPerUnit, info.TieredBillingSnapshot.EstimatedQuotaBeforeGroup)
				}
			} else {
				assert.Nil(t, info.TieredBillingSnapshot)
				assert.Equal(t, "model_price_error", taskErr.Code)
			}
		})
	}
}

func TestSharedTaskBillingExpressionSelectionAndFrozenSettlement(t *testing.T) {
	const baseExpr = `tier("base", u("seconds") * 2)`
	const alphaExpr = `tier("alpha", u("seconds") * 3)`
	const betaExpr = `tier("beta", u("credits") * 5)`
	const aliasExpr = `tier("alias", u("credits") * 7)`
	for _, tc := range []struct {
		name, plugin, model, mapping, modelExpr, mode, wantExpr string
		variants                                                map[string]string
		wantPriceError, profiled                                bool
	}{
		{name: "executing plugin override", plugin: "billing-beta", model: "declared-model", modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-alpha::declared-model": alphaExpr, "billing-beta::declared-model": betaExpr}, wantExpr: betaExpr},
		{name: "override ignores model mode", plugin: "billing-beta", model: "declared-model", modelExpr: baseExpr, mode: "ratio", variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr},
		{name: "model expression fallback", plugin: "billing-alpha", model: "declared-model", modelExpr: baseExpr, mode: "tiered_expr", wantExpr: baseExpr},
		{name: "alias override precedes mapped override", plugin: "billing-beta", model: "alias-model", mapping: `{"alias-model":"declared-model"}`, modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-beta::declared-model": betaExpr, "billing-beta::alias-model": aliasExpr}, wantExpr: aliasExpr},
		{name: "mapped override precedes model fallback", plugin: "billing-beta", model: "alias-model", mapping: `{"alias-model":"declared-model"}`, modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr},
		{name: "unconfigured plugin cannot use another schema", plugin: "billing-beta", model: "declared-model", modelExpr: baseExpr, mode: "tiered_expr", wantPriceError: true},
		{name: "missing usage in skipped branch remains incompatible", plugin: "billing-beta", model: "declared-model", modelExpr: `true ? tier("free", 0) : tier("missing", u("seconds"))`, mode: "tiered_expr", wantPriceError: true},
		{name: "fixed pricing is still rejected", plugin: "billing-beta", model: "declared-model", variants: map[string]string{"billing-beta::declared-model": `tier("fixed", fixed(1))`}, wantPriceError: true},
		{name: "endpoint mapping keeps the declared profile", plugin: "billing-beta", model: "declared-model", mapping: `{"declared-model":"ep-endpoint"}`, modelExpr: baseExpr, mode: "tiered_expr", variants: map[string]string{"billing-beta::declared-model": betaExpr}, wantExpr: betaExpr, profiled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveBillingConfig(t)
			registry := pluginruntime.NewRegistry()
			for _, spec := range []struct{ key, field, unit string }{{"billing-alpha", "seconds", "second"}, {"billing-beta", "credits", "credit"}} {
				source := strings.ReplaceAll(billingFallbackPlugin, "bill-fallback", spec.key)
				schema := `usageSchema:{` + spec.field + `:{type:"number",unit:"` + spec.unit + `"}}`
				if tc.profiled && spec.key == "billing-beta" {
					// The endpoint ID is undeclared, so only the declared model's profile carries this schema.
					schema = `usageSchema:{seconds:{type:"number",unit:"second"}},usageProfiles:[{models:["declared-model"],schema:{` + spec.field + `:{type:"number",unit:"` + spec.unit + `"}}}]`
				}
				source = strings.Replace(source, `fetchMode:"per_task"`, `fetchMode:"per_task",`+schema, 1)
				source += `export function extractUsage(){return {` + spec.field + `:2};}`
				_, err := registry.Register(source, pluginruntime.Options{})
				require.NoError(t, err)
			}
			variants := tc.variants
			if variants == nil {
				variants = map[string]string{}
			}
			rawVariants, err := common.Marshal(variants)
			require.NoError(t, err)
			modes, err := common.Marshal(map[string]string{"declared-model": tc.mode})
			require.NoError(t, err)
			expressions, err := common.Marshal(map[string]string{"declared-model": tc.modelExpr})
			require.NoError(t, err)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				billing_setting.PluginBillingExprOption: string(rawVariants), "billing_setting.billing_mode": string(modes), "billing_setting.billing_expr": string(expressions),
			}))
			c, info := newTaskSubmitContext(t, tc.model, tc.mapping)
			c.Set("group", "default")
			c.Set("task_plugin_key", tc.plugin)
			info.UserGroup = "default"
			info.UsingGroup = "default"
			info.OriginModelName = tc.model
			plugin, ok := registry.Generation().Get(tc.plugin)
			require.True(t, ok)
			c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: registry.Generation(), Plugin: plugin})
			_, taskErr := RelayTaskSubmit(c, info)
			require.NotNil(t, taskErr) // This fixture stops at reservation, before upstream submission.
			if tc.wantPriceError {
				assert.Equal(t, "model_price_error", taskErr.Code)
				assert.Nil(t, info.TieredBillingSnapshot)
				return
			}
			require.NotNil(t, info.TieredBillingSnapshot, "submission error: %+v", taskErr)
			assert.Equal(t, tc.wantExpr, info.TieredBillingSnapshot.ExprString)
			assert.NotEqual(t, "model_price_error", taskErr.Code)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{billing_setting.PluginBillingExprOption: `{}`, "billing_setting.billing_expr": `{}`}))
			field := "seconds"
			if tc.plugin == "billing-beta" {
				field = "credits"
			}
			result, usage, err := service.EvaluateTaskCompletionUsage(info.TieredBillingSnapshot, map[string]any{field: float64(4)})
			require.NoError(t, err)
			assert.Equal(t, float64(4), usage[field])
			assert.Equal(t, float64(2), info.TieredBillingSnapshot.UsageFacts[field])
			assert.Equal(t, 2*info.TieredBillingSnapshot.EstimatedQuotaAfterGroup, result.ActualQuotaAfterGroup)
			assert.Equal(t, tc.wantExpr, info.TieredBillingSnapshot.ExprString)
		})
	}
}

func TestSharedEndpointBillingKeepsOrdinaryAndPluginPricesSeparate(t *testing.T) {
	require.NoError(t, i18n.Init())
	const taskExpr = `tier("images", u("image_count") * 0.05)`
	const ordinaryExpr = `tier("images", fixed(0.04)) * image_count`
	savedPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices)) })
	for _, tc := range []struct {
		name, expression, override          string
		ordinaryQuota, taskQuota, taskFinal int
		wantError                           string
		alias, shared, legacy               bool
	}{
		{name: "task usage price is rejected by ordinary relay", expression: taskExpr, taskQuota: 50000, taskFinal: 75000, wantError: "task usage expression"},
		{name: "enum fallback cannot silently bill zero", expression: `u("output_image_type") == "2k" ? tier("2k", u("image_count") * 0.08) : tier("1k", 0.04)`, taskQuota: 20000, taskFinal: 20000, wantError: "task usage expression"},
		{name: "unselected usage branch is rejected", expression: `true ? tier("flat", 0.04) : tier("images", u("image_count"))`, taskQuota: 20000, taskFinal: 20000, wantError: "task usage expression"},
		{name: "dynamic usage key is rejected", expression: `tier("images", u(u("unit")) * 0.05)`, taskQuota: 50000, taskFinal: 75000, wantError: "task usage expression"},
		{name: "plain task USD price is ambiguous", expression: `tier("flat", 0.04)`, taskQuota: 20000, taskFinal: 20000, wantError: "shares its billing expression"},
		{name: "alias retains task USD price", expression: `tier("flat", 0.04)`, taskQuota: 20000, taskFinal: 20000, wantError: "shares its billing expression", alias: true},
		{name: "per image relay and plugin overrides", expression: ordinaryExpr, override: taskExpr, ordinaryQuota: 40000, taskQuota: 50000, taskFinal: 75000},
		{name: "mapped plugin override", expression: ordinaryExpr, override: taskExpr, ordinaryQuota: 40000, taskQuota: 50000, taskFinal: 75000, alias: true},
		{name: "token relay and plugin overrides", expression: `tier("tokens", p * 2)`, override: taskExpr, ordinaryQuota: 1000, taskQuota: 50000, taskFinal: 75000},
		{name: "ordinary constant has ordinary units with plugin override", expression: `tier("flat", 80000)`, override: taskExpr, ordinaryQuota: 40000, taskQuota: 50000, taskFinal: 75000},
		{name: "another plugin still inherits task USD units", expression: `tier("flat", 0.04)`, override: taskExpr, taskQuota: 50000, taskFinal: 75000, wantError: "shares its billing expression", shared: true},
		{name: "legacy per image pricing stays shared", ordinaryQuota: 40000, taskQuota: 40000, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveBillingConfig(t)
			name, mapping, mappedModel := "declared-model", "", ""
			if tc.alias {
				name, mapping, mappedModel = "alias-model", `{"alias-model":"declared-model"}`, "declared-model"
			}
			mode := billing_setting.BillingModeTieredExpr
			if tc.legacy {
				mode = billing_setting.BillingModeRatio
			}
			variants := map[string]string{}
			if tc.override != "" {
				variants["bill-fallback::declared-model"] = tc.override
			}
			modes, err := common.Marshal(map[string]string{name: mode})
			require.NoError(t, err)
			expressions, err := common.Marshal(map[string]string{name: tc.expression})
			require.NoError(t, err)
			overrides, err := common.Marshal(variants)
			require.NoError(t, err)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode": string(modes), "billing_setting.billing_expr": string(expressions),
				billing_setting.PluginBillingExprOption: string(overrides), "group_ratio_setting.group_ratio": `{"default":1}`,
			}))
			prices, err := common.Marshal(map[string]float64{name: 0.04})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(prices)))
			registry := pluginruntime.NewRegistry()
			source := strings.Replace(billingFallbackPlugin, `fetchMode:"per_task"`, `fetchMode:"per_task",usageSchema:{image_count:{type:"number",unit:"count",description:"Image generation unit price"},output_image_type:{enum:["1k","2k"]},unit:{enum:["image_count"]}}`, 1)
			source += `export function extractUsage(){return {image_count:2,output_image_type:"1k",unit:"image_count"};}`
			plugin, err := registry.Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			candidates := []pluginruntime.ProtocolBinding{{Plugin: plugin}}
			if tc.shared {
				other, err := registry.Register(strings.ReplaceAll(source, "bill-fallback", "other-images"), pluginruntime.Options{})
				require.NoError(t, err)
				candidates = append(candidates, pluginruntime.ProtocolBinding{Plugin: other})
			}
			ordinaryContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			ordinaryContext.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			ordinaryContext.Request.Header.Set("Accept-Language", "en")
			ordinaryContext.Set(pluginruntime.ContextKeyRelayEndpoint, pluginruntime.PinnedEndpoint{Generation: registry.Generation(), Model: name, MappedModel: mappedModel, Candidates: candidates})
			ordinary := &relaycommon.RelayInfo{
				OriginModelName: name, UserGroup: "default", UsingGroup: "default", RelayFormat: kittypes.RelayFormatOpenAIImage,
				Request: &kitdto.ImageRequest{Model: name, N: common.GetPointer(uint(2))}, BillingRequestInput: &billingexpr.RequestInput{},
			}
			ordinaryPrice, err := helper.ModelPriceHelper(ordinaryContext, ordinary, 1000, &kittypes.TokenCountMeta{})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, ordinary.TieredBillingSnapshot)
				assert.Nil(t, ordinary.Billing, "pricing must fail before reservation")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.ordinaryQuota, ordinaryPrice.QuotaToPreConsume)
			}

			pluginContext, task := newTaskSubmitContext(t, name, mapping)
			pluginContext.Set("group", "default")
			pluginContext.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: registry.Generation(), Plugin: plugin})
			task.OriginModelName, task.UserGroup, task.UsingGroup = name, "default", "default"
			_, taskErr := RelayTaskSubmit(pluginContext, task)
			require.NotNil(t, taskErr) // This fixture stops at reservation, before upstream submission.
			assert.NotEqual(t, "model_price_error", taskErr.Code)
			assert.Equal(t, tc.taskQuota, task.PriceData.Quota)
			if tc.legacy {
				assert.Nil(t, task.TieredBillingSnapshot)
				assert.Nil(t, ordinary.TieredBillingSnapshot)
				return
			}
			require.NotNil(t, task.TieredBillingSnapshot)
			assert.True(t, task.TieredBillingSnapshot.TaskUsageBilling)
			// Updating live prices must not change either in-flight meter.
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_expr": `{}`, billing_setting.PluginBillingExprOption: `{}`,
			}))
			settlement, _, err := service.EvaluateTaskCompletionUsage(task.TieredBillingSnapshot, map[string]any{"image_count": float64(3)})
			require.NoError(t, err)
			assert.Equal(t, tc.taskFinal, settlement.ActualQuotaAfterGroup)
			if tc.wantError != "" {
				return
			}
			require.NotNil(t, ordinary.TieredBillingSnapshot)
			assert.False(t, ordinary.TieredBillingSnapshot.TaskUsageBilling)
			settlement, err = billingexpr.ComputeTieredQuotaWithRequest(ordinary.TieredBillingSnapshot, billingexpr.TokenParams{P: 1000, Len: 1000}, *ordinary.BillingRequestInput)
			require.NoError(t, err)
			assert.Equal(t, tc.ordinaryQuota, settlement.ActualQuotaAfterGroup)
		})
	}
}

// Issue #7478: task APIs answer 201 Created or 202 Accepted on submission.
// Every 2xx must reach parseSubmitResponse; only other statuses are upstream
// failures that keep the upstream status and body.
func TestRelayTaskSubmitAcceptsAnySuccessfulUpstreamStatus(t *testing.T) {
	service.InitHttpClient()
	const source = `
export const meta = {apiVersion:1,key:"status-echo",name:"Status Echo",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl+"/submit", method:"POST", body:{model: ctx.model}, action:"text_to_video"}; }
export function parseSubmitResponse(ctx, response) { return {taskId: response.body.id, taskData: {status: response.statusCode}}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl+"/query"}; }
export function parseTaskResult() { return {status:"SUCCESS"}; }
`
	for _, tc := range []struct {
		status   int
		wantCode string
	}{
		{status: http.StatusOK},
		{status: http.StatusCreated},
		{status: http.StatusAccepted},
		{status: http.StatusBadRequest, wantCode: "fail_to_fetch_task"},
		{status: http.StatusBadGateway, wantCode: "fail_to_fetch_task"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			saveBillingConfig(t)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode": `{"declared-model":"tiered_expr"}`,
				"billing_setting.billing_expr": `{"declared-model":"tier(\"flat\", 3)"}`,
			}))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"id":"job-42","message":"upstream body"}`))
			}))
			defer server.Close()

			c, info := newTaskSubmitContext(t, "declared-model", "")
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			c.Set("group", "default")
			info.UserGroup, info.UsingGroup = "default", "default"
			info.OriginModelName = "declared-model"
			// An existing reservation stands in for the wallet so the fixture reaches the upstream call.
			info.Billing = &imageReservation{limit: 1 << 30}
			pinMappingOrderPlugin(t, c, source)

			result, taskErr := RelayTaskSubmit(c, info)
			if tc.wantCode != "" {
				require.NotNil(t, taskErr)
				assert.Equal(t, tc.wantCode, taskErr.Code)
				assert.Equal(t, tc.status, taskErr.StatusCode)
				assert.Contains(t, taskErr.Message, "upstream body")
				assert.Nil(t, result)
				return
			}
			require.Nil(t, taskErr, "submission error: %+v", taskErr)
			require.NotNil(t, result)
			assert.Equal(t, "job-42", result.UpstreamTaskID)
			assert.JSONEq(t, `{"status":`+strconv.Itoa(tc.status)+`}`, string(result.TaskData))
		})
	}
}

// A retry prices the plugin it lands on from scratch and holds that price
// before the upstream sees the request. Plugin A fails upstream on the first
// attempt; the retry runs on plugin B, pinned the way
// SetupContextForSelectedChannel pins another candidate plugin's channel.
func TestRelayTaskSubmitRetryRepricesAndReservesBeforeUpstream(t *testing.T) {
	service.InitHttpClient()
	const source = `
export const meta = {apiVersion:1,key:"KEY",name:"KEY",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task",usageSchema:{seconds:{type:"number",unit:"second",description:"Video generation unit price"}}};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl+"/submit", method:"POST", body:{model: ctx.model}, action:"text_to_video"}; }
export function parseSubmitResponse(ctx, response) { return {taskId: response.body.id}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl+"/query"}; }
export function parseTaskResult() { return {status:"SUCCESS"}; }
export function extractUsage() { return {seconds:2}; }
`
	pricier := map[string]string{"retry-a::declared-model": `tier("a", u("seconds") * 0.05)`, "retry-b::declared-model": `tier("b", u("seconds") * 0.4)`}
	for _, tc := range []struct {
		name, perCallPrice, wantCode string
		expressions                  map[string]string
		wallet, wantQuota            int
		wantSubmitted                []string
		freeFirst                    bool
	}{
		{name: "per-call plugin drops the previous expression", expressions: map[string]string{"retry-a::declared-model": `tier("a", u("seconds") * 0.02)`}, perCallPrice: `{"declared-model":0.04}`, wallet: 1 << 30, wantQuota: 40000, wantSubmitted: []string{"a", "b"}},
		{name: "free plugin retry is held before submission", expressions: map[string]string{"retry-b::declared-model": `tier("b", u("seconds") * 0.4)`}, perCallPrice: `{"declared-model":0}`, wallet: 1 << 30, wantQuota: 400000, wantSubmitted: []string{"a", "b"}, freeFirst: true},
		{name: "paid retry to free plugin retains the reservation", expressions: map[string]string{"retry-a::declared-model": `tier("a", u("seconds") * 0.05)`}, perCallPrice: `{"declared-model":0}`, wallet: 1 << 30, wantSubmitted: []string{"a", "b"}},
		{name: "pricier plugin is held before submission", expressions: pricier, perCallPrice: `{}`, wallet: 1 << 30, wantQuota: 400000, wantSubmitted: []string{"a", "b"}},
		{name: "unaffordable plugin is never submitted", expressions: pricier, perCallPrice: `{}`, wallet: 100000, wantCode: "insufficient_user_quota", wantSubmitted: []string{"a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveBillingConfig(t)
			savedPrices := ratio_setting.ModelPrice2JSONString()
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices)) })
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(tc.perCallPrice))
			expressions, err := common.Marshal(tc.expressions)
			require.NoError(t, err)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				billing_setting.PluginBillingExprOption:       string(expressions),
				"billing_setting.billing_mode":                `{}`,
				"billing_setting.billing_expr":                `{}`,
				"group_ratio_setting.group_ratio":             `{"default":1}`,
				"quota_setting.enable_free_model_pre_consume": "false",
			}))
			registry := pluginruntime.NewRegistry()
			for _, key := range []string{"retry-a", "retry-b"} {
				_, err := registry.Register(strings.ReplaceAll(source, "KEY", key), pluginruntime.Options{})
				require.NoError(t, err)
			}
			// The fake stands in for the session the first attempt created; its
			// limit is the most the wallet can hold.
			billing := &imageReservation{limit: tc.wallet}
			var submitted []string
			var heldAtSubmit []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
				submitted = append(submitted, attempt)
				heldAtSubmit = append(heldAtSubmit, billing.held)
				w.Header().Set("Content-Type", "application/json")
				if attempt == "a" {
					w.WriteHeader(http.StatusInternalServerError)
				}
				_, _ = w.Write([]byte(`{"id":"job-` + attempt + `"}`))
			}))
			defer server.Close()

			c, info := newTaskSubmitContext(t, "declared-model", "")
			c.Set("group", "default")
			info.UserGroup, info.UsingGroup, info.OriginModelName = "default", "default", "declared-model"
			if !tc.freeFirst {
				info.Billing = billing
			}
			var result *TaskSubmitResult
			var taskErr *dto.TaskError
			for _, attempt := range []string{"a", "b"} {
				plugin, ok := registry.Generation().Get("retry-" + attempt)
				require.True(t, ok)
				c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Generation: registry.Generation(), Plugin: plugin})
				c.Set("task_plugin_key", plugin.Meta.Key)
				common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL+"/"+attempt)
				result, taskErr = RelayTaskSubmit(c, info)
				if attempt == "a" {
					require.NotNil(t, taskErr)
					require.Equal(t, http.StatusInternalServerError, taskErr.StatusCode, "the first attempt fails upstream and is retried")
					if tc.freeFirst {
						assert.Nil(t, info.Billing, "a free ordinary request starts without a billing session")
						// A fake wallet-backed session isolates the retry's pre-submit ordering.
						info.Billing = billing
					}
				}
			}

			require.Equal(t, tc.wantSubmitted, submitted)
			if tc.wantCode != "" {
				require.NotNil(t, taskErr)
				assert.Equal(t, tc.wantCode, taskErr.Code)
				assert.Equal(t, http.StatusForbidden, taskErr.StatusCode)
				return
			}
			require.Nil(t, taskErr, "submission error: %+v", taskErr)
			assert.Equal(t, tc.wantQuota, result.Quota)
			assert.GreaterOrEqual(t, heldAtSubmit[1], tc.wantQuota, "plugin B must not see the request before its price is held")
			if tc.wantQuota == 0 {
				assert.Equal(t, heldAtSubmit[0], heldAtSubmit[1], "a cheaper retry keeps the earlier reservation until settlement")
			}
			if _, ok := tc.expressions["retry-b::declared-model"]; !ok {
				assert.Nil(t, info.TieredBillingSnapshot, "a per-call plugin must not settle with the previous plugin's expression")
				return
			}
			require.NotNil(t, info.TieredBillingSnapshot)
			assert.Equal(t, tc.expressions["retry-b::declared-model"], info.TieredBillingSnapshot.ExprString)
		})
	}
}

func TestManagedFreeTaskCreatesFundingFactBeforeSubmission(t *testing.T) {
	db := setupRelayChannelDB(t)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.SubscriptionPlan{},
		&model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.XCodeAuthorization{}))
	saveBillingConfig(t)
	savedPrices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"declared-model":0}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		billing_setting.PluginBillingExprOption:       `{}`,
		"billing_setting.billing_mode":                `{}`,
		"billing_setting.billing_expr":                `{}`,
		"group_ratio_setting.group_ratio":             `{"default":1}`,
		"quota_setting.enable_free_model_pre_consume": "false",
	}))
	user := model.User{Username: "managed-free-task", AffCode: "managed-free-task", Quota: 900, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "managed-free-task-key", RemainQuota: 100, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	plan := model.SubscriptionPlan{Title: "managed-free-task", TotalAmount: 100, QuotaResetPeriod: model.SubscriptionResetNever}
	require.NoError(t, db.Create(&plan).Error)
	now := model.GetDBTimestamp()
	sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, UsageScope: model.SubscriptionUsageScopeXCode,
		Source: "admin", Status: "active", AmountTotal: 100, StartTime: now - 60, EndTime: now + 3600,
		QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
	require.NoError(t, db.Create(&sub).Error)
	require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)

	service.InitHttpClient()
	var factAtSubmit model.SubscriptionPreConsumeRecord
	var queryErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queryErr = db.Where("request_id = ?", "managed-free-task").First(&factAtSubmit).Error
		_, _ = w.Write([]byte(`{"id":"job-free"}`))
	}))
	t.Cleanup(server.Close)
	c, info := newTaskSubmitContext(t, "declared-model", "")
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
	c.Set("group", "default")
	pinMappingOrderPlugin(t, c, billingFallbackPlugin)
	info.UserId, info.TokenId, info.TokenKey = user.Id, token.Id, token.Key
	info.UserGroup, info.UsingGroup, info.OriginModelName = "default", "default", "declared-model"
	info.RequestId = "managed-free-task"
	info.UserSetting = kitdto.UserSetting{BillingPreference: "wallet_only"}
	info.ClientPurpose, info.FundingScope = model.ClientPurposeXCode, model.SubscriptionUsageScopeXCode
	info.FundingSubscriptionID, info.FundingAuthorizationRevision = sub.Id, 1

	result, taskErr := RelayTaskSubmit(c, info)
	require.Nil(t, taskErr, "submission error: %+v", taskErr)
	require.NoError(t, queryErr)
	assert.Zero(t, result.Quota)
	assert.Equal(t, "consumed", factAtSubmit.Status)
	assert.Zero(t, factAtSubmit.CycleReservedQuota)
	assert.Equal(t, sub.Id, factAtSubmit.UserSubscriptionId)
	require.NoError(t, info.Billing.Settle(0))
	require.NoError(t, db.First(&factAtSubmit, factAtSubmit.Id).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, "settled", factAtSubmit.Status)
	assert.Equal(t, 900, user.Quota)
}

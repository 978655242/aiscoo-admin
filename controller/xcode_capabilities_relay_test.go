package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestXCodeThinkingRelayConformance(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 10
	t.Cleanup(func() { constant.StreamingTimeout = previousStreamingTimeout })
	user := xcodeCatalogFixture(t, db)
	now := model.GetDBTimestamp()
	require.NoError(t, db.Model(&user).Update("auth_version", 1).Error)
	sub := model.UserSubscription{UserId: user.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 3600, AmountTotal: 10000000, QuotaResetPeriodSnapshot: model.SubscriptionResetNever}
	require.NoError(t, db.Create(&sub).Error)
	require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 1}).Error)
	require.NoError(t, db.Create(&model.UserSession{SID: "thinking-relay-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, ClientPurpose: model.ClientPurposeXCode, RefreshHash: strings.Repeat("a", 64), LoginMethod: "password", ExpiresAt: now + 3600}).Error)
	key := "thinkingrelayisolated"
	require.NoError(t, db.Create(&model.Token{UserId: user.Id, Key: key, Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: now + 3600, ClientPurpose: model.ClientPurposeXCode, ClientSessionSID: "thinking-relay-session"}).Error)
	previousGroups, previousRatios := setting.UserUsableGroups2JSONString(), ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"catalog":"Catalog"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"catalog":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousRatios))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"chat":1,"fixture-upstream":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"chat":1,"fixture-upstream":1}`))
	service.InitHttpClient()
	var mutex sync.Mutex
	var bodies []map[string]any
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		mutex.Lock()
		bodies = append(bodies, body)
		mutex.Unlock()
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			emit := func(delta any) {
				data, _ := json.Marshal(map[string]any{"id": "fixture-stream", "object": "chat.completion.chunk", "created": 1, "model": "fixture-upstream", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.(http.Flusher).Flush()
			}
			emit(map[string]any{"role": "assistant", "reasoning_content": "fixture reasoning"})
			// The relay buffers its last chunk; a second chunk makes the first observable before cancellation.
			emit(map[string]any{"reasoning_content": " continued"})
			<-r.Context().Done()
			close(canceled)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"fixture-complete","object":"chat.completion","created":1,"model":"fixture-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"fixture complete"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}`)
	}))
	t.Cleanup(upstream.Close)
	configure := func(channelType int, passThrough bool, override string) {
		channel, err := model.GetChannelById(1, true)
		require.NoError(t, err)
		channel.Type = channelType
		channel.BaseURL = common.GetPointer(upstream.URL)
		if channelType == constant.ChannelTypeDeepSeek {
			channel.BaseURL = common.GetPointer(upstream.URL + "/v1")
		}
		channel.ModelMapping = common.GetPointer(`{"chat":"fixture-upstream"}`)
		channel.ParamOverride = common.GetPointer(override)
		channel.SetSetting(dto.ChannelSettings{PassThroughBodyEnabled: passThrough})
		require.NoError(t, db.Save(channel).Error)
	}
	var serial atomic.Int32
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup())
	router.POST("/v1/chat/completions", middleware.RouteTag("relay"), middleware.TokenAuth(), XCodeModelAdmission, middleware.Distribute(), func(c *gin.Context) {
		c.Set(common.RequestIdKey, fmt.Sprintf("isolated-thinking-%d", serial.Add(1)))
		Relay(c, types.RelayFormatOpenAI)
	})
	gateway := httptest.NewServer(router)
	t.Cleanup(gateway.Close)
	for _, tc := range []struct {
		name                                   string
		kind                                   int
		pass                                   bool
		override, expectedModel, expectedLevel string
	}{
		{"deepseek converter and alias", constant.ChannelTypeDeepSeek, false, "{}", "fixture-upstream", "high"},
		{"channel parameter override", constant.ChannelTypeOpenAI, false, `{"reasoning_effort":"max"}`, "fixture-upstream", "max"},
		{"raw pass-through", constant.ChannelTypeOpenAI, true, "{}", "chat", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configure(tc.kind, tc.pass, tc.override)
			request, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"chat","max_tokens":48,"thinking":{"type":"enabled"},"reasoning_effort":"high","messages":[{"role":"user","content":"synthetic fixture"},{"role":"assistant","content":null,"reasoning_content":"fixture reasoning","tool_calls":[{"id":"lookup-1","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"fixture\"}"}}]},{"role":"tool","tool_call_id":"lookup-1","content":"fixture tool result"}]}`))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer sk-"+key)
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode, string(data))
			mutex.Lock()
			body := bodies[len(bodies)-1]
			mutex.Unlock()
			require.Equal(t, tc.expectedModel, body["model"])
			require.Equal(t, tc.expectedLevel, body["reasoning_effort"])
			require.Equal(t, map[string]any{"type": "enabled"}, body["thinking"])
			messages := body["messages"].([]any)
			assistant := messages[1].(map[string]any)
			tool := messages[2].(map[string]any)
			require.Equal(t, "fixture reasoning", assistant["reasoning_content"])
			require.Equal(t, "lookup-1", tool["tool_call_id"])
			require.Equal(t, "fixture tool result", tool["content"])
		})
	}
	t.Run("streamed reasoning and prompt client cancellation", func(t *testing.T) {
		configure(constant.ChannelTypeDeepSeek, false, "{}")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"chat","max_tokens":48,"stream":true,"thinking":{"type":"enabled"},"reasoning_effort":"high","messages":[{"role":"user","content":"cancel fixture"}]}`))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer sk-"+key)
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Contains(t, response.Header.Get("Content-Type"), "text/event-stream")
		reader := bufio.NewReader(response.Body)
		for {
			line, err := reader.ReadString('\n')
			require.NoError(t, err)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var chunk dto.ChatCompletionsStreamResponse
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &chunk))
			require.Len(t, chunk.Choices, 1)
			require.Equal(t, "fixture reasoning", chunk.Choices[0].Delta.GetReasoningContent())
			break
		}
		mutex.Lock()
		body := bodies[len(bodies)-1]
		mutex.Unlock()
		require.Equal(t, "fixture-upstream", body["model"])
		require.Equal(t, "high", body["reasoning_effort"])
		require.Equal(t, map[string]any{"type": "enabled"}, body["thinking"])
		select {
		case <-canceled:
			t.Fatal("upstream ended before client cancellation")
		default:
		}
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		cancel()
		select {
		case <-canceled:
		case <-deadline.C:
			t.Fatal("upstream did not observe client cancellation within 1s (relay idle timeout is 10s)")
		}
	})
}

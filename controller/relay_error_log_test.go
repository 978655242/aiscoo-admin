package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProcessChannelErrorUsesSnapshotWithoutLeakingChannelMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousErrorLogEnabled := constant.ErrorLogEnabled

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB, model.LOG_DB = database, database
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	constant.ErrorLogEnabled = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, database.Create(&model.User{Id: 7, Username: "log-owner", Group: "default"}).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("id", 7)
	ctx.Set("username", "log-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 11)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	ctx.Set("channel_id", 202)
	ctx.Set("channel_name", "mutable-context-channel")
	ctx.Set("channel_type", 9)
	ctx.Set("use_channel", []string{"101"})
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	channelSnapshot := types.ChannelError{
		ChannelId:   101,
		ChannelType: 1,
		ChannelName: "snapshot-channel",
		AutoBan:     false,
	}
	apiErr := types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	processChannelError(ctx, channelSnapshot, apiErr, nil)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, channelSnapshot.ChannelId, stored.ChannelId)
	storedOther, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, float64(http.StatusBadGateway), storedOther["status_code"])
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, storedOther, key)
	}
	adminInfo, ok := storedOther["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"101"}, adminInfo["use_channel"])

	logs, total, err := model.GetUserLogs(7, model.LogTypeError, 0, 0, "", "", 0, 10, "", "", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, channelSnapshot.ChannelId, logs[0].ChannelId)
	assert.Empty(t, logs[0].ChannelName)
	userOther, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	assert.NotContains(t, userOther, "admin_info")
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, userOther, key)
	}
}

func TestWriteRelayErrorPreservesUpstreamClaudeType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%t", managed), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if managed {
				common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
			}
			upstream := types.ClaudeError{Type: "overloaded_error", Message: "upstream overloaded"}
			writeRelayError(ctx, types.RelayFormatClaude, types.WithClaudeError(upstream, http.StatusServiceUnavailable), nil)
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			assert.JSONEq(t, `{"type":"error","error":{"type":"overloaded_error","message":"upstream overloaded"}}`, recorder.Body.String())
			if managed {
				assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
			}
		})
	}
	t.Run("managed non-2xx upstream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
		ctx.Set(common.RequestIdKey, "upstream-type-request")
		response := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"overloaded_error","message":"upstream overloaded"}}`)),
		}
		writeRelayError(ctx, types.RelayFormatClaude, service.RelayErrorHandler(ctx, response, false), nil)
		assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		var body struct {
			Type  string            `json:"type"`
			Error types.ClaudeError `json:"error"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
		assert.Equal(t, "error", body.Type)
		assert.Equal(t, "overloaded_error", body.Error.Type)
		assert.Contains(t, body.Error.Message, "upstream overloaded")
		assert.Contains(t, body.Error.Message, "upstream-type-request")
		assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	})
}

func TestWriteRelayErrorManagedClaudeAdmissionCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		status int
		code   types.ErrorCode
		kind   string
	}{
		{http.StatusUnauthorized, "XCODE_SESSION_INVALID", "authentication_error"},
		{http.StatusForbidden, "XCODE_MODEL_UNAVAILABLE", "permission_error"},
		{http.StatusTooManyRequests, "XCODE_QUOTA_EXHAUSTED", "rate_limit_error"},
		{http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE", "api_error"},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			common.SetContextKey(ctx, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
			apiError := types.NewErrorWithStatusCode(errors.New("admission rejected"), tc.code, tc.status)
			writeRelayError(ctx, types.RelayFormatClaude, apiError, nil)
			assert.Equal(t, tc.status, recorder.Code)
			var body struct {
				Type  string `json:"type"`
				Error struct {
					Type string `json:"type"`
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, "error", body.Type)
			assert.Equal(t, tc.kind, body.Error.Type)
			assert.Equal(t, string(tc.code), body.Error.Code)
			assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
		})
	}
}

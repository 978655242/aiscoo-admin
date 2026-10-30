package controller

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeChatOpenDefaultCapabilityIsDisabled(t *testing.T) {
	assert.True(t, common.WeChatOpenEnabled, "website login should be requested by default")
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	GetStatus(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			WeChatOpen *struct {
				Enabled     bool   `json:"enabled"`
				AppID       string `json:"app_id"`
				RedirectURI string `json:"redirect_uri"`
			} `json:"wechat_open"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotNil(t, response.Data.WeChatOpen, "website capability must be distinct from legacy official-account login")
	assert.False(t, response.Data.WeChatOpen.Enabled)
}

func TestWeChatOpenStatusRequiresValidConfiguration(t *testing.T) {
	previousEnabled, previousID := common.WeChatOpenEnabled, common.WeChatOpenAppID
	previousSecret, previousCallback := common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL
	previousAddress := system_setting.ServerAddress
	t.Cleanup(func() {
		common.WeChatOpenEnabled, common.WeChatOpenAppID = previousEnabled, previousID
		common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL = previousSecret, previousCallback
		system_setting.ServerAddress = previousAddress
	})
	common.WeChatOpenEnabled = true
	common.WeChatOpenAppID = "wx1234567890abcdef"
	common.WeChatOpenAppSecret = "synthetic-status-secret"
	common.WeChatOpenCallbackURL = "https://wechat.example.test/oauth/wechat-open"
	system_setting.ServerAddress = "https://wechat.example.test"

	for _, test := range []struct {
		name     string
		callback string
		enabled  bool
	}{
		{"valid", "https://wechat.example.test/oauth/wechat-open", true},
		{"invalid callback", "https://other.example.test/oauth/wechat-open", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			common.WeChatOpenCallbackURL = test.callback
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
			GetStatus(c)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Data struct {
					WeChatOpen struct {
						Enabled bool `json:"enabled"`
					} `json:"wechat_open"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Equal(t, test.enabled, response.Data.WeChatOpen.Enabled)
			assert.NotContains(t, recorder.Body.String(), common.WeChatOpenAppSecret)
		})
	}
}

type weChatOpenTestTransport func(*http.Request) (*http.Response, error)

func (transport weChatOpenTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func setupWeChatOpenHTTP(t *testing.T) (*gin.Engine, string, *atomic.Int32) {
	t.Helper()
	setupSecurityEnrollmentTest(t, model.EnsureWeChatOpenIdentitySchema)
	require.NoError(t, model.EnsureWeChatOpenIdentitySchema(model.DB))
	require.NoError(t, model.EnsureWeChatOpenIdentitySchema(model.DB))
	previousEnabled, previousID := common.WeChatOpenEnabled, common.WeChatOpenAppID
	previousSecret, previousCallback := common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL
	previousOrigin, previousRegister := system_setting.ServerAddress, common.RegisterEnabled
	previousPasswordLogin := common.PasswordLoginEnabled
	common.PasswordLoginEnabled = true
	common.WeChatOpenEnabled = true
	common.WeChatOpenAppID = "wx1234567890abcdef"
	common.WeChatOpenAppSecret = "synthetic-wechat-secret"
	common.WeChatOpenCallbackURL = "https://wechat.example.test/oauth/wechat-open"
	system_setting.ServerAddress = "https://wechat.example.test"
	common.RegisterEnabled = true
	t.Cleanup(func() {
		common.WeChatOpenEnabled, common.WeChatOpenAppID = previousEnabled, previousID
		common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL = previousSecret, previousCallback
		system_setting.ServerAddress, common.RegisterEnabled = previousOrigin, previousRegister
		common.PasswordLoginEnabled = previousPasswordLogin
	})
	requests := &atomic.Int32{}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = weChatOpenTestTransport(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		assert.Equal(t, "https", request.URL.Scheme)
		assert.Equal(t, "api.weixin.qq.com", request.URL.Host)
		assert.Equal(t, http.MethodGet, request.Method)
		query := request.URL.Query()
		var body gin.H
		switch request.URL.Path {
		case "/sns/oauth2/access_token":
			assert.Equal(t, common.WeChatOpenAppID, query.Get("appid"))
			assert.Equal(t, common.WeChatOpenAppSecret, query.Get("secret"))
			body = gin.H{"access_token": "synthetic-wechat-access-" + query.Get("code"), "openid": query.Get("code"), "scope": "snsapi_login", "expires_in": 7200}
		case "/sns/userinfo":
			openid := strings.TrimPrefix(query.Get("access_token"), "synthetic-wechat-access-")
			body = gin.H{"openid": openid, "nickname": "Synthetic WeChat User"}
		default:
			t.Errorf("unexpected official endpoint path %q", request.URL.Path)
			body = gin.H{"errcode": 40029}
		}
		data, err := common.Marshal(body)
		require.NoError(t, err)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	router := gin.New()
	router.Use(middleware.TryUserAuth())
	router.POST("/api/oauth/state", GenerateOAuthCode)
	router.GET("/api/oauth/:provider", HandleOAuth)
	proof := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	return router, proof, requests
}

func weChatOpenHTTP(router *gin.Engine, method, target, proof, body string, headers ...http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if proof != "" {
		request.Header.Set(weChatOpenBrowserProofHeader, proof)
	}
	for _, header := range headers {
		for name, values := range header {
			request.Header[name] = values
		}
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func weChatOpenState(t *testing.T, router *gin.Engine, proof string) string {
	t.Helper()
	return weChatOpenStart(t, router, proof, `{"provider":"wechat-open","intent":"login"}`, nil)
}

func weChatOpenStart(t *testing.T, router *gin.Engine, proof, body string, headers http.Header) string {
	t.Helper()
	response := weChatOpenHTTP(router, http.MethodPost, "/api/oauth/state", proof, body, headers)
	require.Equal(t, http.StatusOK, response.Code)
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	require.True(t, result.Success, response.Body.String())
	require.NotEmpty(t, result.Data.FlowToken)
	return result.Data.FlowToken
}

func weChatOpenCallback(router *gin.Engine, state, proof, code string, headers ...http.Header) *httptest.ResponseRecorder {
	query := url.Values{"state": {state}, "code": {code}}
	return weChatOpenHTTP(router, http.MethodGet, "/api/oauth/wechat-open?"+query.Encode(), proof, "", headers...)
}

func TestWeChatOpenStateRequiresMatchingBrowserProof(t *testing.T) {
	router, proof, requests := setupWeChatOpenHTTP(t)
	missing := weChatOpenHTTP(router, http.MethodPost, "/api/oauth/state", "", `{"provider":"wechat-open","intent":"login"}`)
	assert.NotContains(t, missing.Body.String(), "flow_token")
	state := weChatOpenState(t, router, proof)
	for _, invalid := range []string{"", "invalid", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))} {
		response := weChatOpenCallback(router, state, invalid, "proof-subject")
		assert.NotContains(t, response.Body.String(), `"success":true`)
		assert.Empty(t, response.Result().Cookies())
	}
	assert.Zero(t, requests.Load())
	response := weChatOpenCallback(router, state, proof, "proof-subject")
	assert.Contains(t, response.Body.String(), `"success":true`)
	assert.NotEmpty(t, response.Result().Cookies())
	assert.NotContains(t, response.Body.String(), "synthetic-wechat-access")
	assert.NotContains(t, response.Body.String(), common.WeChatOpenAppSecret)
	replay := weChatOpenCallback(router, state, proof, "proof-subject")
	assert.NotContains(t, replay.Body.String(), `"success":true`)
	assert.Empty(t, replay.Result().Cookies())
	assert.EqualValues(t, 2, requests.Load())
}

func TestWeChatOpenConfigurationChangeRejectsPendingFlow(t *testing.T) {
	router, proof, requests := setupWeChatOpenHTTP(t)
	state := weChatOpenState(t, router, proof)
	common.WeChatOpenAppSecret = "changed-synthetic-secret"
	response := weChatOpenCallback(router, state, proof, "config-subject")
	assert.NotContains(t, response.Body.String(), `"success":true`)
	assert.Empty(t, response.Result().Cookies())
	assert.Zero(t, requests.Load())
}

func TestWeChatOpenRegistrationGateAndExistingAccount(t *testing.T) {
	router, proof, _ := setupWeChatOpenHTTP(t)
	common.RegisterEnabled = false
	response := weChatOpenCallback(router, weChatOpenState(t, router, proof), proof, "registration-subject")
	assert.NotContains(t, response.Body.String(), `"success":true`)
	assert.Empty(t, response.Result().Cookies())
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("wechat_open_id IS NOT NULL").Count(&count).Error)
	assert.Zero(t, count)
	common.RegisterEnabled = true
	response = weChatOpenCallback(router, weChatOpenState(t, router, proof), proof, "registration-subject")
	var created struct {
		Success bool `json:"success"`
		Data    struct {
			User struct {
				ID int `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &created))
	require.True(t, created.Success, response.Body.String())
	require.Positive(t, created.Data.User.ID)
	common.RegisterEnabled = false
	response = weChatOpenCallback(router, weChatOpenState(t, router, proof), proof, "registration-subject")
	var existing struct {
		Success bool `json:"success"`
		Data    struct {
			User struct {
				ID int `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &existing))
	assert.True(t, existing.Success)
	assert.Equal(t, created.Data.User.ID, existing.Data.User.ID)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", existing.Data.User.ID).Update("status", common.UserStatusDisabled).Error)
	response = weChatOpenCallback(router, weChatOpenState(t, router, proof), proof, "registration-subject")
	assert.NotContains(t, response.Body.String(), `"success":true`)
	assert.Empty(t, response.Result().Cookies())
}

func TestWeChatOpenConcurrentRegistrationDoesNotLeaveOrphanUsers(t *testing.T) {
	router, proof, _ := setupWeChatOpenHTTP(t)
	states := []string{weChatOpenState(t, router, proof), weChatOpenState(t, router, proof)}
	var before int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&before).Error)
	responses := make(chan *httptest.ResponseRecorder, len(states))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, state := range states {
		workers.Go(func() {
			<-start
			responses <- weChatOpenCallback(router, state, proof, "same-subject")
		})
	}
	close(start)
	workers.Wait()
	close(responses)
	for response := range responses {
		assert.Contains(t, response.Body.String(), `"success":true`)
	}
	var after, bindings int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&after).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("wechat_open_id = ?", "wx1234567890abcdef:same-subject").Count(&bindings).Error)
	assert.Equal(t, before+1, after)
	assert.EqualValues(t, 1, bindings)
}

func TestWeChatOpenIdentityIsCaseSensitiveAndLegacyIndependent(t *testing.T) {
	setupWeChatOpenHTTP(t)
	first, created, err := model.FindOrCreateWeChatOpenUser("wx1234567890abcdef:Subject", "First", 0, true)
	require.NoError(t, err)
	require.True(t, created)
	second, created, err := model.FindOrCreateWeChatOpenUser("wx1234567890abcdef:subject", "Second", 0, true)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, first.Id, second.Id)
	assert.Empty(t, first.WeChatId)
	assert.Empty(t, second.WeChatId)
	for _, username := range []string{"unbound-website-one", "unbound-website-two"} {
		require.NoError(t, model.DB.Create(&model.User{Username: username, AffCode: username, Role: common.RoleCommonUser, Status: common.UserStatusEnabled}).Error)
	}
}

func weChatOpenTestBrowser(t *testing.T) (*model.User, service.AuthIdentity, http.Header) {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "enrollment-user").First(&user).Error)
	identity, token := createAccessTokenTestSession(t, user.Id, "wechat-open-browser")
	return &user, identity, http.Header{"Authorization": {"Bearer " + token}}
}

func TestWeChatOpenMFARequiresVerificationBeforeSessionCookie(t *testing.T) {
	router, proof, _ := setupWeChatOpenHTTP(t)
	user, _, _ := weChatOpenTestBrowser(t)
	require.NoError(t, model.DB.Model(user).Update("wechat_open_id", common.WeChatOpenAppID+":mfa-subject").Error)
	factor := &model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}
	require.NoError(t, model.DB.Create(factor).Error)
	before, err := model.CountActiveUserSessions(user.Id, time.Now().Unix())
	require.NoError(t, err)
	response := weChatOpenCallback(router, weChatOpenState(t, router, proof), proof, "mfa-subject")
	result := decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	var challenge service.LoginChallenge
	require.NoError(t, common.Unmarshal(result.Data, &challenge))
	require.True(t, challenge.RequireVerification)
	require.NotEmpty(t, challenge.FlowToken)
	assert.Equal(t, []service.VerificationMethodOption{{Method: "2fa", Available: true}}, challenge.Methods)
	assert.Empty(t, response.Result().Cookies())
	assert.NotContains(t, response.Body.String(), "access_token")
	after, err := model.CountActiveUserSessions(user.Id, time.Now().Unix())
	require.NoError(t, err)
	assert.Equal(t, before, after)
	router.POST("/api/user/login/verify", VerifyLogin)
	body, err := common.Marshal(gin.H{"flow_token": challenge.FlowToken, "code": "invalid"})
	require.NoError(t, err)
	response = weChatOpenHTTP(router, http.MethodPost, "/api/user/login/verify", "", string(body))
	assert.False(t, decodeSecurityEnrollmentResponse(t, response).Success)
	assert.Empty(t, response.Result().Cookies())
	code, err := totp.GenerateCode(factor.Secret, time.Now())
	require.NoError(t, err)
	body, err = common.Marshal(gin.H{"flow_token": challenge.FlowToken, "code": code})
	require.NoError(t, err)
	response = weChatOpenHTTP(router, http.MethodPost, "/api/user/login/verify", "", string(body))
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	var bundle service.AuthBundle
	require.NoError(t, common.Unmarshal(result.Data, &bundle))
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, user.Id, identity.UserID)
	assert.Equal(t, "oauth:wechat-open", bundle.Session.LoginMethod)
	assert.NotEmpty(t, response.Result().Cookies())
	after, err = model.CountActiveUserSessions(user.Id, time.Now().Unix())
	require.NoError(t, err)
	assert.Equal(t, before+1, after)
}

func TestWeChatOpenExpiredFlowRejectsBeforeUpstream(t *testing.T) {
	router, proof, requests := setupWeChatOpenHTTP(t)
	state := weChatOpenState(t, router, proof)
	flow, err := model.GetAuthFlow(state, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(flow).Update("expires_at", time.Now().Add(-time.Second)).Error)
	response := weChatOpenCallback(router, state, proof, "expired-subject")
	assert.False(t, decodeSecurityEnrollmentResponse(t, response).Success)
	assert.Empty(t, response.Result().Cookies())
	assert.Zero(t, requests.Load())
	var bindings int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("wechat_open_id IS NOT NULL").Count(&bindings).Error)
	assert.Zero(t, bindings)
}

func TestWeChatOpenConfigurationMutationDuringExchangeHasNoSideEffects(t *testing.T) {
	for _, change := range []string{"appid", "secret", "callback"} {
		t.Run(change, func(t *testing.T) {
			router, proof, requests := setupWeChatOpenHTTP(t)
			state := weChatOpenState(t, router, proof)
			var usersBefore, sessionsBefore int64
			require.NoError(t, model.DB.Model(&model.User{}).Count(&usersBefore).Error)
			require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessionsBefore).Error)
			transport := http.DefaultTransport
			http.DefaultTransport = weChatOpenTestTransport(func(request *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(request)
				if request.URL.Path == "/sns/oauth2/access_token" {
					switch change {
					case "appid":
						common.WeChatOpenAppID = "wxabcdef1234567890"
					case "secret":
						common.WeChatOpenAppSecret = "rotated-synthetic-secret"
					case "callback":
						system_setting.ServerAddress = "https://replacement.example.test"
						common.WeChatOpenCallbackURL = system_setting.ServerAddress + "/oauth/wechat-open"
					}
				}
				return response, err
			})
			response := weChatOpenCallback(router, state, proof, "mutated-config-subject")
			assert.False(t, decodeSecurityEnrollmentResponse(t, response).Success)
			assert.Empty(t, response.Result().Cookies())
			assert.EqualValues(t, 2, requests.Load(), "mutation happens after the initial config check and token request")
			var usersAfter, sessionsAfter, bindings int64
			require.NoError(t, model.DB.Model(&model.User{}).Count(&usersAfter).Error)
			require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessionsAfter).Error)
			require.NoError(t, model.DB.Model(&model.User{}).Where("wechat_open_id IS NOT NULL").Count(&bindings).Error)
			assert.Equal(t, usersBefore, usersAfter)
			assert.Equal(t, sessionsBefore, sessionsAfter)
			assert.Zero(t, bindings)
		})
	}
}

func TestWeChatOpenBindingRequiresNativeProofAndOriginatingSID(t *testing.T) {
	router, browserProof, requests := setupWeChatOpenHTTP(t)
	user, identity, headers := weChatOpenTestBrowser(t)
	require.NoError(t, model.DB.Model(user).Update("quota", 12345).Error)
	body := `{"provider":"wechat-open","intent":"bind"}`
	response := weChatOpenHTTP(router, http.MethodPost, "/api/oauth/state", browserProof, body, headers)
	result := decodeSecurityEnrollmentResponse(t, response)
	assert.False(t, result.Success)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", result.Code)
	var flows int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ? AND provider = ?", model.AuthFlowPurposeOAuth, "wechat-open").Count(&flows).Error)
	assert.Zero(t, flows)
	response = securityEnrollmentRequest(http.MethodPost, "/api/verify", `{"method":"password","scope":"account.binding.bind","context":{"provider":"wechat-open"},"password":"enrollment-password"}`, "", identity, UniversalVerify)
	result = decodeSecurityEnrollmentResponse(t, response)
	require.True(t, result.Success, response.Body.String())
	var nativeProof service.SecurityProof
	require.NoError(t, common.Unmarshal(result.Data, &nativeProof))
	headers.Set("X-Security-Proof", nativeProof.ProofToken)
	state := weChatOpenStart(t, router, browserProof, body, headers)
	flow, err := model.GetAuthFlow(state, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	require.NoError(t, err)
	assert.Equal(t, identity.UserID, flow.UserId)
	assert.Equal(t, identity.SessionID, flow.SessionId)
	_, otherToken := createAccessTokenTestSession(t, user.Id, "other-wechat-browser")
	for _, caller := range []http.Header{nil, {"Authorization": {"Bearer " + otherToken}}} {
		response = weChatOpenCallback(router, state, browserProof, "bound-subject", caller)
		assert.False(t, decodeSecurityEnrollmentResponse(t, response).Success)
		assert.Empty(t, response.Result().Cookies())
	}
	assert.Zero(t, requests.Load())
	response = weChatOpenCallback(router, state, browserProof, "bound-subject", headers)
	require.True(t, decodeSecurityEnrollmentResponse(t, response).Success, response.Body.String())
	assert.Empty(t, response.Result().Cookies())
	stored, err := model.GetUserById(user.Id, false)
	require.NoError(t, err)
	require.NotNil(t, stored.WeChatOpenId)
	assert.Equal(t, common.WeChatOpenAppID+":bound-subject", *stored.WeChatOpenId)
	assert.Equal(t, 12345, stored.Quota)
	assert.Equal(t, user.AuthVersion, stored.AuthVersion)
	assert.EqualValues(t, 2, requests.Load())
	response = weChatOpenCallback(router, state, browserProof, "bound-subject", headers)
	assert.False(t, decodeSecurityEnrollmentResponse(t, response).Success)
	assert.EqualValues(t, 2, requests.Load())
}

func TestWeChatOpenBindingConflictsNeverOverwriteIdentity(t *testing.T) {
	for _, conflict := range []string{"existing own binding", "another owner"} {
		t.Run(conflict, func(t *testing.T) {
			router, browserProof, _ := setupWeChatOpenHTTP(t)
			user, identity, headers := weChatOpenTestBrowser(t)
			existing := common.WeChatOpenAppID + ":existing-subject"
			if conflict == "existing own binding" {
				require.NoError(t, model.DB.Model(user).Update("wechat_open_id", existing).Error)
			} else {
				existing = common.WeChatOpenAppID + ":claimed-subject"
				require.NoError(t, model.DB.Create(&model.User{Username: "wechat-binding-owner", AffCode: "wechat-binding-owner", Status: common.UserStatusEnabled, AuthVersion: 1, WeChatOpenId: &existing}).Error)
			}
			operation := service.VerificationOperation{Scope: service.VerificationScopeAccountBind, Context: []byte(`{"provider":"wechat-open"}`)}
			headers.Set("X-Security-Proof", issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword))
			state := weChatOpenStart(t, router, browserProof, `{"provider":"wechat-open","intent":"bind"}`, headers)
			response := weChatOpenCallback(router, state, browserProof, "claimed-subject", headers)
			assert.False(t, decodeSecurityEnrollmentResponse(t, response).Success)
			assert.Empty(t, response.Result().Cookies())
			stored, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			if conflict == "existing own binding" {
				require.NotNil(t, stored.WeChatOpenId)
				assert.Equal(t, existing, *stored.WeChatOpenId)
			} else {
				assert.Nil(t, stored.WeChatOpenId)
			}
			var owners int64
			require.NoError(t, model.DB.Model(&model.User{}).Where("wechat_open_id = ?", existing).Count(&owners).Error)
			assert.EqualValues(t, 1, owners)
		})
	}
}

func TestWeChatOpenVerificationUsesOnlyExistingLinkedIdentity(t *testing.T) {
	for _, subject := range []string{"verified-subject", "different-subject"} {
		t.Run(subject, func(t *testing.T) {
			router, browserProof, _ := setupWeChatOpenHTTP(t)
			user, identity, headers := weChatOpenTestBrowser(t)
			linked := common.WeChatOpenAppID + ":verified-subject"
			require.NoError(t, model.DB.Model(user).Updates(map[string]any{"password": "", "wechat_open_id": linked}).Error)
			requirements, err := service.GetVerificationRequirements(identity, service.VerificationScopeTwoFASetup)
			require.NoError(t, err)
			assert.Contains(t, requirements.Methods, service.VerificationMethodOption{Method: "oauth", Available: true})
			assert.Contains(t, requirements.OAuthProviders, service.VerificationOAuthProvider{Slug: "wechat-open", Name: "WeChat website", UserID: linked})
			state := weChatOpenStart(t, router, browserProof, `{"provider":"wechat-open","intent":"verify","scope":"2fa.setup"}`, headers)
			response := weChatOpenCallback(router, state, browserProof, subject, headers)
			result := decodeSecurityEnrollmentResponse(t, response)
			assert.Equal(t, subject == "verified-subject", result.Success, response.Body.String())
			assert.Empty(t, response.Result().Cookies())
			assert.NotContains(t, response.Body.String(), "access_token")
			if result.Success {
				var proof service.SecurityProof
				require.NoError(t, common.Unmarshal(result.Data, &proof))
				assert.Equal(t, service.VerificationMethodOAuth, proof.Method)
				_, err = service.ConsumeOperationProof(proof.ProofToken, identity, service.VerificationOperation{Scope: service.VerificationScopeTwoFASetup})
				require.NoError(t, err)
			}
			var users, sessions int64
			require.NoError(t, model.DB.Model(&model.User{}).Count(&users).Error)
			require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessions).Error)
			assert.EqualValues(t, 1, users)
			assert.EqualValues(t, 2, sessions)
			stored, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			require.NotNil(t, stored.WeChatOpenId)
			assert.Equal(t, linked, *stored.WeChatOpenId)
		})
	}
}

func TestWeChatOpenUnlinkRotatesCurrentSessionAndProtectsLastFactor(t *testing.T) {
	for _, lastFactor := range []bool{false, true} {
		name := "password remains"
		if lastFactor {
			name = "last factor"
		}
		t.Run(name, func(t *testing.T) {
			router, _, _ := setupWeChatOpenHTTP(t)
			user, identity, headers := weChatOpenTestBrowser(t)
			linked := common.WeChatOpenAppID + ":unlink-subject"
			require.NoError(t, model.DB.Model(user).Update("wechat_open_id", linked).Error)
			method := service.VerificationMethodPassword
			if lastFactor {
				require.NoError(t, model.DB.Model(user).Update("password", "").Error)
				method = service.VerificationMethodOAuth
			}
			otherIdentity, _ := createAccessTokenTestSession(t, user.Id, "other-unlink-browser")
			router.DELETE("/api/user/oauth/bindings/:provider_id", middleware.UserAuth(), UnbindCustomOAuth)
			path := "/api/user/oauth/bindings/wechat-open"
			response := weChatOpenHTTP(router, http.MethodDelete, path, "", "", headers)
			assert.Equal(t, "SECURITY_PROOF_REQUIRED", decodeSecurityEnrollmentResponse(t, response).Code)
			operation := service.VerificationOperation{Scope: service.VerificationScopeAccountUnbind, Context: []byte(`{"provider":"wechat-open"}`)}
			headers.Set("X-Security-Proof", issueSecurityEnrollmentProof(t, identity, operation, method))
			response = weChatOpenHTTP(router, http.MethodDelete, path, "", "", headers)
			result := decodeSecurityEnrollmentResponse(t, response)
			stored, err := model.GetUserById(user.Id, false)
			require.NoError(t, err)
			if lastFactor {
				assert.False(t, result.Success)
				assert.Equal(t, "LAST_LOGIN_METHOD", result.Code)
				require.NotNil(t, stored.WeChatOpenId)
				assert.Equal(t, linked, *stored.WeChatOpenId)
				assert.Equal(t, identity.UserAuthVersion, stored.AuthVersion)
				_, _, err = service.ValidateLoginSession(identity)
				require.NoError(t, err)
				_, _, err = service.ValidateLoginSession(otherIdentity)
				require.NoError(t, err)
			} else {
				require.True(t, result.Success, response.Body.String())
				assert.Nil(t, stored.WeChatOpenId)
				assert.Equal(t, identity.UserAuthVersion+1, stored.AuthVersion)
				var rotation struct {
					AccessToken string `json:"access_token"`
				}
				require.NoError(t, common.Unmarshal(result.Data, &rotation))
				current, err := service.ParseAccessToken(rotation.AccessToken)
				require.NoError(t, err)
				assert.Equal(t, identity.SessionID, current.SessionID)
				assert.Equal(t, stored.AuthVersion, current.UserAuthVersion)
				_, _, err = service.ValidateLoginSession(current)
				require.NoError(t, err)
				_, _, err = service.ValidateLoginSession(identity)
				assert.Error(t, err)
				_, _, err = service.ValidateLoginSession(otherIdentity)
				assert.Error(t, err)
			}
		})
	}
}

func TestWeChatOpenDeletedAccountCannotRegisterReplacement(t *testing.T) {
	router, proof, _ := setupWeChatOpenHTTP(t)
	user, _, _ := weChatOpenTestBrowser(t)
	linked := common.WeChatOpenAppID + ":deleted-subject"
	require.NoError(t, model.DB.Model(user).Update("wechat_open_id", linked).Error)
	require.NoError(t, model.DB.Delete(user).Error)
	response := weChatOpenCallback(router, weChatOpenState(t, router, proof), proof, "deleted-subject")
	assert.False(t, decodeSecurityEnrollmentResponse(t, response).Success)
	assert.Empty(t, response.Result().Cookies())
	var users, sessions int64
	require.NoError(t, model.DB.Unscoped().Model(&model.User{}).Count(&users).Error)
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessions).Error)
	assert.EqualValues(t, 1, users)
	assert.EqualValues(t, 2, sessions)
}

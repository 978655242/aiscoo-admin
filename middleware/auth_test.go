package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupDashboardAuthMiddlewareTest(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousType := common.MainDatabaseType()
	previousRedis := common.RedisEnabled
	previousSecret := common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuditLog{}, &model.UserAccessToken{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.SessionSecret = "middleware-auth-test-secret"
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(time.Now().Unix()))
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetMainDatabaseType(previousType)
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
	})
}

func issueExpiredDashboardAccessToken(t *testing.T, identity service.AuthIdentity) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":       "new-api",
		"aud":       []string{"new-api-dashboard"},
		"sub":       fmt.Sprintf("%d", identity.UserID),
		"token_use": "access",
		"sid":       identity.SessionID,
		"uv":        identity.UserAuthVersion,
		"sv":        identity.SessionVersion,
		"exp":       time.Now().Add(-time.Minute).Unix(),
		"nbf":       time.Now().Add(-2 * time.Minute).Unix(),
		"iat":       time.Now().Add(-2 * time.Minute).Unix(),
	}
	mac := hmac.New(sha256.New, []byte(common.SessionSecret))
	_, err := mac.Write([]byte("new-api/auth/access/v1"))
	require.NoError(t, err)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(mac.Sum(nil))
	require.NoError(t, err)
	return token
}

func tamperDashboardToken(token string) string {
	tamperAt := len(token) - 2
	replacement := "x"
	if token[tamperAt] == 'x' {
		replacement = "y"
	}
	return token[:tamperAt] + replacement + token[tamperAt+1:]
}

func createMiddlewarePATUser(t *testing.T, username, token string) *model.User {
	t.Helper()
	user := &model.User{
		Username: username, Password: "password-placeholder", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AccessToken: &token, AuthVersion: 1,
		AffCode: "middleware-aff-" + username,
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func TestUserAuthAllowsOpaqueDottedPAT(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	user := createMiddlewarePATUser(t, "dotted-pat-user", "opaque.key.with-dots")
	router := gin.New()
	router.GET("/protected", UserAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": c.GetInt("id")})
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer opaque.key.with-dots")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	var body struct {
		ID int `json:"id"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, user.Id, body.ID)
}

func TestUserAuthNeverFallsBackForRecognizedInvalidInternalJWT(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	identity := service.AuthIdentity{UserID: 42, SessionID: "session-42", UserAuthVersion: 1, SessionVersion: 1}
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	tampered := tamperDashboardToken(token)
	createMiddlewarePATUser(t, "jwt-fallback-user", tampered)
	router := gin.New()
	router.GET("/protected", UserAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+tampered)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Contains(t, response.Body.String(), "AUTH_UNAUTHORIZED")
}

func TestXCodeControlAndNativeRoutePurposeIsolation(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	user := createMiddlewarePATUser(t, "xcode-root", "opaque-control-pat")
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	bundle, err := service.CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", bundle.Session.SID).Update("client_purpose", model.ClientPurposeXCode).Error)
	session, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	control, _, err := service.IssueXCodeAccessToken(session)
	require.NoError(t, err)
	web, _, err := service.IssueXCodeWebToken(session)
	require.NoError(t, err)
	router := gin.New()
	handler := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	for _, path := range []string{"/me", "/plans", "/entitlements", "/models", "/usage", "/client-config"} {
		router.GET("/api/xcode/v1"+path, XCodeControlAuth(), handler)
	}
	router.POST("/api/xcode/v1/web-handoffs", XCodeControlAuth(), handler)
	router.POST("/api/xcode/v1/auth/logout", XCodeControlAuth(), handler)
	router.POST("/api/xcode/v1/admin/users/:id/authorization", XCodeControlAuth(), handler)
	router.GET("/api/xcode/v1/me/", XCodeControlAuth(), handler)
	router.POST("/api/xcode/v1/me", XCodeControlAuth(), handler)
	router.GET("/native", RootAuth(), handler)
	router.GET("/optional", TryUserAuth(), handler)
	router.GET("/mixed", TokenOrUserAuth(), handler)
	for _, test := range []struct {
		method, path, token string
		status              int
		code                string
	}{
		{http.MethodGet, "/api/xcode/v1/me", control, 204, ""},
		{http.MethodGet, "/api/xcode/v1/plans", control, 204, ""},
		{http.MethodGet, "/api/xcode/v1/entitlements", control, 204, ""},
		{http.MethodGet, "/api/xcode/v1/models", control, 204, ""},
		{http.MethodGet, "/api/xcode/v1/usage", control, 204, ""},
		{http.MethodGet, "/api/xcode/v1/client-config", control, 204, ""},
		{http.MethodPost, "/api/xcode/v1/web-handoffs", control, 204, ""},
		{http.MethodPost, "/api/xcode/v1/auth/logout", control, 204, ""},
		{http.MethodPost, "/api/xcode/v1/admin/users/1/authorization", control, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodGet, "/api/xcode/v1/me/", control, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodPost, "/api/xcode/v1/me", control, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodGet, "/native", control, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodGet, "/optional", control, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodGet, "/mixed", control, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodGet, "/native", bundle.AccessToken, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodGet, "/api/xcode/v1/me", web, 403, "XCODE_CREDENTIAL_SCOPE_DENIED"},
		{http.MethodGet, "/api/xcode/v1/me", "opaque-control-pat", 401, "XCODE_SESSION_INVALID"},
		{http.MethodGet, "/native", tamperDashboardToken(control), 401, "AUTH_UNAUTHORIZED"},
	} {
		t.Run(test.method+" "+test.path+" "+test.code, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+test.token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code)
			if test.code != "" {
				assert.Contains(t, response.Body.String(), test.code)
			}
		})
	}
}

func TestXCodeJWTNeverFallsBackToPATOrRelayKey(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	session := &model.UserSession{UserID: 42, SID: "xcode-test-sid", Version: 1, UserAuthVersion: 1,
		ClientPurpose: model.ClientPurposeXCode, Status: model.UserSessionStatusActive, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	control, _, err := service.IssueXCodeAccessToken(session)
	require.NoError(t, err)
	tampered := tamperDashboardToken(control)
	malformed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "new-api", "aud": "new-api-xcode", "token_use": "xcode_access",
		"scopes": 42, "exp": time.Now().Add(time.Minute).Unix(),
	}).SignedString([]byte("untrusted-key"))
	require.NoError(t, err)
	wrongIssuer, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "different-issuer", "aud": "new-api-xcode", "token_use": "xcode_access",
		"exp": time.Now().Add(time.Minute).Unix(),
	}).SignedString([]byte("untrusted-key"))
	require.NoError(t, err)
	for i, raw := range []string{tampered, malformed, wrongIssuer} {
		user := createMiddlewarePATUser(t, fmt.Sprintf("xcode-fallback-user-%d", i), raw)
		require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: raw, Status: common.TokenStatusEnabled, UnlimitedQuota: true, ExpiredTime: -1}).Error)
		for _, auth := range []gin.HandlerFunc{UserAuth(), TryUserAuth(), TokenOrUserAuth(), TokenAuth(), TokenAuthReadOnly()} {
			router := gin.New()
			router.GET("/protected", auth, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			request.Header.Set("Authorization", "Bearer "+raw)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.Contains(t, response.Body.String(), "AUTH_UNAUTHORIZED")
		}
	}
}

func TestTryUserAuthCredentialClassification(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	gin.SetMode(gin.TestMode)

	patUser := createMiddlewarePATUser(t, "optional-pat-user", "optional.pat.with-dots")
	internalUser := createMiddlewarePATUser(t, "optional-session-user", "unrelated-pat")
	scopedPAT, _ := createMiddlewareScopedToken(t, patUser.Id, 0, "profile:read")
	now := time.Now().Unix()
	session := &model.UserSession{
		SID:             "optional-auth-session",
		UserID:          internalUser.Id,
		Version:         1,
		UserAuthVersion: internalUser.AuthVersion,
		Status:          model.UserSessionStatusActive,
		RefreshHash:     "refresh-hash",
		LoginMethod:     "password",
		LastActiveAt:    now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.CreateUserSession(session))
	identity := service.AuthIdentity{
		UserID:          internalUser.Id,
		SessionID:       session.SID,
		UserAuthVersion: session.UserAuthVersion,
		SessionVersion:  session.Version,
	}
	accessToken, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	require.NoError(t, model.DB.AutoMigrate(&model.AuthFlow{}))
	binding, err := service.BindVerificationOperation(service.VerificationOperation{Scope: "channel.key.read", Context: []byte(`{"channel_id":123}`)})
	require.NoError(t, err)
	securityProof, _, err := service.IssueSecurityProof(identity, "2fa", binding)
	require.NoError(t, err)
	externalToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "external-issuer",
		"aud": "external-audience",
		"exp": time.Now().Add(time.Minute).Unix(),
	}).SignedString([]byte("external-secret"))
	require.NoError(t, err)

	router := gin.New()
	router.GET("/optional", TryUserAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"id":               c.GetInt("id"),
			"use_access_token": c.GetBool("use_access_token"),
		})
	})

	tests := []struct {
		name          string
		token         string
		wantStatus    int
		wantUserID    int
		wantPAT       bool
		wantErrorCode string
	}{
		{name: "no authorization header", wantStatus: http.StatusOK},
		{name: "opaque unmatched credential", token: "opaque-relay-key", wantStatus: http.StatusOK},
		{name: "dotted unmatched credential", token: "ordinary.key.with-dots", wantStatus: http.StatusOK},
		{name: "third party jwt", token: externalToken, wantStatus: http.StatusOK},
		{name: "valid pat", token: "optional.pat.with-dots", wantStatus: http.StatusOK, wantUserID: patUser.Id, wantPAT: true},
		{name: "scoped pat outside its grant stays anonymous", token: scopedPAT, wantStatus: http.StatusOK},
		{name: "valid internal access jwt", token: accessToken, wantStatus: http.StatusOK, wantUserID: internalUser.Id},
		{name: "expired internal access jwt", token: issueExpiredDashboardAccessToken(t, identity), wantStatus: http.StatusUnauthorized, wantErrorCode: "AUTH_TOKEN_EXPIRED"},
		{name: "tampered internal access jwt", token: tamperDashboardToken(accessToken), wantStatus: http.StatusUnauthorized, wantErrorCode: "AUTH_UNAUTHORIZED"},
		{name: "security proof used as access", token: securityProof, wantStatus: http.StatusUnauthorized, wantErrorCode: "AUTH_UNAUTHORIZED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/optional", nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, test.wantStatus, response.Code)
			if test.wantErrorCode != "" {
				assert.Contains(t, response.Body.String(), test.wantErrorCode)
				return
			}
			var body struct {
				ID             int  `json:"id"`
				UseAccessToken bool `json:"use_access_token"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, test.wantUserID, body.ID)
			assert.Equal(t, test.wantPAT, body.UseAccessToken)
		})
	}

	requiredRouter := gin.New()
	requiredRouter.GET("/required", UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	requiredRequest := httptest.NewRequest(http.MethodGet, "/required", nil)
	requiredRequest.Header.Set("Authorization", "Bearer ordinary-unmatched-key")
	requiredResponse := httptest.NewRecorder()
	requiredRouter.ServeHTTP(requiredResponse, requiredRequest)
	assert.Equal(t, http.StatusUnauthorized, requiredResponse.Code, "required dashboard authentication must not adopt optional-auth fallback semantics")

	var patUserQueries int
	forcedCacheError := errors.New("forced PAT user cache lookup failure")
	const callbackName = "test:optional-auth-pat-user-cache-failure"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "users" {
			return
		}
		patUserQueries++
		if patUserQueries == 2 {
			tx.AddError(forcedCacheError)
		}
	}))
	cacheFailureRequest := httptest.NewRequest(http.MethodGet, "/optional", nil)
	cacheFailureRequest.Header.Set("Authorization", "Bearer optional.pat.with-dots")
	cacheFailureResponse := httptest.NewRecorder()
	router.ServeHTTP(cacheFailureResponse, cacheFailureRequest)
	model.DB.Callback().Query().Remove(callbackName)
	assert.Equal(t, http.StatusInternalServerError, cacheFailureResponse.Code)
	assert.Contains(t, cacheFailureResponse.Body.String(), "AUTH_INTERNAL_ERROR")

	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	databaseFailureRequest := httptest.NewRequest(http.MethodGet, "/optional", nil)
	databaseFailureRequest.Header.Set("Authorization", "Bearer database-failure-key")
	databaseFailureResponse := httptest.NewRecorder()
	router.ServeHTTP(databaseFailureResponse, databaseFailureRequest)
	assert.Equal(t, http.StatusInternalServerError, databaseFailureResponse.Code)
	assert.Contains(t, databaseFailureResponse.Body.String(), "AUTH_INTERNAL_ERROR")
}

func createMiddlewareScopedToken(t *testing.T, userID int, expiresAt int64, scopes ...string) (string, *model.UserAccessToken) {
	t.Helper()
	suffix, err := common.GenerateRandomCharsKey(43)
	require.NoError(t, err)
	raw := model.AccessTokenPrefix + suffix
	token := &model.UserAccessToken{Name: "middleware token", TokenHash: model.AccessTokenFingerprint(raw), TokenHint: model.AccessTokenHint(raw), ExpiresAt: expiresAt}
	require.NoError(t, token.SetScopes(scopes))
	require.NoError(t, model.CreateUserAccessToken(userID, token, service.AccessTokenMaxPerUser))
	return raw, token
}

func middlewareBearerRequest(router *gin.Engine, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestUserAuthAppliesAccessTokenRouteRules(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	gin.SetMode(gin.TestMode)
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = wasMaster })
	require.NoError(t, model.DB.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, authz.Init(model.DB))
	// The channel router declares this rule when it registers the route.
	DeclareAccessTokenPermissionRoute(http.MethodGet, "/api/channel/", authz.ChannelRead)

	legacy := "middleware-legacy-token"
	admin := createMiddlewarePATUser(t, "route-rule-admin", legacy)
	require.NoError(t, model.DB.Model(admin).Update("role", common.RoleAdminUser).Error)
	profile, _ := createMiddlewareScopedToken(t, admin.Id, 0, "profile:read")
	channel, _ := createMiddlewareScopedToken(t, admin.Id, time.Now().Unix()+3600, "channel:read")
	expired, _ := createMiddlewareScopedToken(t, admin.Id, time.Now().Unix()-1, "profile:read")

	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"id": c.GetInt("id")}) }
	router := gin.New()
	router.GET("/api/user/self", UserAuth(), ok)
	router.GET("/api/user/access_tokens", UserAuth(), ok)
	router.GET("/api/undeclared", UserAuth(), ok)
	router.GET("/api/channel/", AdminAuth(), RequirePermission(authz.ChannelRead), ok)
	router.GET("/api/pricing", TryUserAuth(), ok)

	for _, test := range []struct {
		name, path, token, code, reason string
		status                          int
	}{
		{name: "granted scope on a never expiring token", path: "/api/user/self", token: profile, status: http.StatusOK},
		{name: "missing scope", path: "/api/user/self", token: channel, status: http.StatusForbidden, code: "ACCESS_TOKEN_SCOPE_DENIED", reason: "scope_denied"},
		{name: "undeclared route", path: "/api/undeclared", token: profile, status: http.StatusForbidden, code: "ACCESS_TOKEN_ROUTE_UNDECLARED", reason: "route_undeclared"},
		{name: "session route", path: "/api/user/access_tokens", token: profile, status: http.StatusForbidden, code: "AUTH_SESSION_REQUIRED", reason: "session_required"},
		{name: "permission route with its scope", path: "/api/channel/", token: channel, status: http.StatusOK},
		{name: "permission route without its scope", path: "/api/channel/", token: profile, status: http.StatusForbidden, code: "ACCESS_TOKEN_SCOPE_DENIED", reason: "scope_denied"},
		{name: "expired token", path: "/api/user/self", token: expired, status: http.StatusUnauthorized, code: "ACCESS_TOKEN_EXPIRED", reason: "expired"},
		{name: "legacy token before the deadline", path: "/api/user/self", token: legacy, status: http.StatusOK},
		{name: "legacy token on a session route", path: "/api/user/access_tokens", token: legacy, status: http.StatusForbidden, code: "AUTH_SESSION_REQUIRED", reason: "session_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := middlewareBearerRequest(router, test.path, test.token)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			if test.code != "" {
				assert.Contains(t, response.Body.String(), `"code":"`+test.code+`"`)
			}
			var audit model.AuditLog
			require.NoError(t, model.LOG_DB.Where("category = ?", model.AuditCategoryAccessToken).Order("id desc").First(&audit).Error)
			assert.Equal(t, model.AccessTokenFingerprint(test.token), audit.TokenRef)
			assert.Equal(t, test.status, audit.Status)
			if test.reason == "" {
				return
			}
			require.NotNil(t, audit.Other.Op)
			params, err := common.Marshal(audit.Other.Op.Params)
			require.NoError(t, err)
			assert.Contains(t, string(params), `"failure_reason":"`+test.reason+`"`)
			if test.code == "ACCESS_TOKEN_SCOPE_DENIED" {
				assert.Contains(t, string(params), `"required_scope":"`)
			}
		})
	}

	// The token grant never widens the account: revoking the Casbin permission
	// rejects a token that still carries the scope.
	require.NoError(t, authz.SetUserPermissions(admin.Id, authz.PermissionsMap{authz.ResourceChannel: {authz.ActionRead: false}}))
	response := middlewareBearerRequest(router, "/api/channel/", channel)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.NotContains(t, response.Body.String(), "ACCESS_TOKEN_SCOPE_DENIED")

	require.NoError(t, model.DB.Model(admin).Update("status", common.UserStatusDisabled).Error)
	response = middlewareBearerRequest(router, "/api/user/self", profile)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"AUTH_USER_DISABLED"`)
	require.NoError(t, model.DB.Model(admin).Update("status", common.UserStatusEnabled).Error)
	assert.Equal(t, http.StatusOK, middlewareBearerRequest(router, "/api/user/self", profile).Code, "disabling a user keeps the token")

	require.NoError(t, model.DB.Model(&model.Option{}).Where(&model.Option{Key: "LegacyAccessTokenRetireAt"}).Update("value", fmt.Sprint(time.Now().Unix()-1)).Error)
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(time.Now().Unix()))
	response = middlewareBearerRequest(router, "/api/user/self", legacy)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"ACCESS_TOKEN_LEGACY_RETIRED"`)
	response = middlewareBearerRequest(router, "/api/pricing", legacy)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"id":0}`, response.Body.String(), "optional authentication treats a retired token as anonymous")
	assert.Equal(t, http.StatusOK, middlewareBearerRequest(router, "/api/user/self", profile).Code, "the deadline only retires legacy tokens")
}

func TestAccessTokenIdentityAndSingleLookup(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	gin.SetMode(gin.TestMode)
	legacy := "middleware-identity-legacy"
	user := createMiddlewarePATUser(t, "identity-user", legacy)
	scoped, token := createMiddlewareScopedToken(t, user.Id, 0, "profile:read")

	router := gin.New()
	router.Use(AccessTokenAudit())
	router.GET("/api/user/self", UserAuth(), func(c *gin.Context) {
		_, session := GetSessionAuthIdentity(c)
		identity, stepUp := GetStepUpIdentity(c)
		c.JSON(http.StatusOK, gin.H{"session": session, "step_up": stepUp, "identity": identity})
	})

	var queries []string
	const callbackName = "test:access-token-lookup-count"
	require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		queries = append(queries, tx.Statement.Table+" "+tx.Statement.SQL.String())
	}))
	t.Cleanup(func() { model.DB.Callback().Query().Remove(callbackName) })
	countQueries := func(table, fragment string) int {
		count := 0
		for _, query := range queries {
			if strings.HasPrefix(query, table+" ") && strings.Contains(query, fragment) {
				count++
			}
		}
		return count
	}

	for _, test := range []struct {
		name, token string
		stepUp      bool
		tokenTable  int
		legacyQuery int
	}{
		{name: "scoped token", token: scoped, stepUp: true, tokenTable: 1},
		{name: "legacy token", token: legacy, legacyQuery: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			queries = nil
			response := middlewareBearerRequest(router, "/api/user/self", test.token)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var body struct {
				Session  bool                 `json:"session"`
				StepUp   bool                 `json:"step_up"`
				Identity service.AuthIdentity `json:"identity"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
			assert.False(t, body.Session, "an access token is never a browser session")
			assert.Equal(t, test.stepUp, body.StepUp)
			if test.stepUp {
				assert.Equal(t, service.AuthIdentity{UserID: user.Id, SessionID: model.AccessTokenSessionID(token.Id), UserAuthVersion: user.AuthVersion, SessionVersion: model.AccessTokenSessionVersion}, body.Identity)
			}
			// The access audit and authentication share one lookup: the
			// credential query plus the owner's user row.
			assert.Len(t, queries, 2)
			assert.Equal(t, test.tokenTable, countQueries("user_access_tokens", "token_hash"))
			assert.Equal(t, test.legacyQuery, countQueries("users", "access_token = "))
		})
	}
}

func TestAPIKeyFromWebSocketSubprotocol(t *testing.T) {
	tests := []struct {
		name      string
		protocols string
		wantKey   string
		wantOK    bool
	}{
		{
			name:      "responses protocol only",
			protocols: "responses",
			wantOK:    false,
		},
		{
			name:      "realtime protocol only",
			protocols: "realtime",
			wantOK:    false,
		},
		{
			name:      "responses with insecure key",
			protocols: "responses, openai-insecure-api-key.sk-test",
			wantKey:   "sk-test",
			wantOK:    true,
		},
		{
			name:      "realtime with beta and insecure key",
			protocols: "realtime, openai-insecure-api-key.sk-realtime, openai-beta.realtime-v1",
			wantKey:   "sk-realtime",
			wantOK:    true,
		},
		{
			name:      "empty insecure key",
			protocols: "responses, openai-insecure-api-key.",
			wantOK:    false,
		},
		{
			name:      "bare insecure marker is not a key",
			protocols: "openai-insecure-api-key",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotOK := apiKeyFromWebSocketSubprotocol(tt.protocols)
			assert.Equal(t, tt.wantOK, gotOK)
			assert.Equal(t, tt.wantKey, gotKey)
		})
	}
}

func TestApplyWebSocketSubprotocolAuthorizationDoesNotOverrideProtocolOnly(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer sk-original")
	header.Set("Sec-WebSocket-Protocol", "responses")
	header.Add("Sec-WebSocket-Protocol", "openai-beta.realtime-v1")

	assert.False(t, applyWebSocketSubprotocolAuthorization(header))
	assert.Equal(t, "Bearer sk-original", header.Get("Authorization"))
}

func TestApplyWebSocketSubprotocolAuthorizationOverridesWithInsecureKey(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer sk-original")
	header.Set("Sec-WebSocket-Protocol", "responses, openai-insecure-api-key.sk-from-protocol")

	assert.True(t, applyWebSocketSubprotocolAuthorization(header))
	assert.Equal(t, "Bearer sk-from-protocol", header.Get("Authorization"))
}

func TestApplyWebSocketSubprotocolAuthorizationReadsRepeatedHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer sk-original")
	header.Add("Sec-WebSocket-Protocol", "responses")
	header.Add("Sec-WebSocket-Protocol", "openai-insecure-api-key.sk-later-field")

	assert.True(t, applyWebSocketSubprotocolAuthorization(header))
	assert.Equal(t, "Bearer sk-later-field", header.Get("Authorization"))
}

func setupXCodeAdmissionTest(t *testing.T) (*model.User, *model.Token, *model.UserSession, *model.UserSubscription) {
	t.Helper()
	setupDashboardAuthMiddlewareTest(t)
	previousMaster, previousLogType := common.IsMasterNode, common.LogDatabaseType()
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode = false
	// Initialize the native dialect column quoting against this isolated fixture.
	err := model.InitLogDB()
	common.IsMasterNode = previousMaster
	t.Cleanup(func() { common.SetLogDatabaseType(previousLogType) })
	require.NoError(t, err)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.XCodeAuthorization{}))
	user := createMiddlewarePATUser(t, "xcode-admission-user", "unrelated-dashboard-token")
	session := &model.UserSession{
		SID: "xcode-admission-session", UserID: user.Id, Version: 7, UserAuthVersion: user.AuthVersion,
		Status: model.UserSessionStatusActive, ClientPurpose: model.ClientPurposeXCode,
		RefreshHash: "xcode-refresh-hash", LoginMethod: "password", ExpiresAt: time.Now().Unix() + 3600,
	}
	require.NoError(t, model.DB.Create(session).Error)
	token := &model.Token{
		UserId: user.Id, Key: "xcodeadmissionkey", Status: common.TokenStatusEnabled, ExpiredTime: session.ExpiresAt,
		UnlimitedQuota: true, ClientPurpose: model.ClientPurposeXCode, ClientSessionSID: session.SID,
	}
	require.NoError(t, model.DB.Create(token).Error)
	plan := &model.SubscriptionPlan{Title: "XCode admission", Enabled: true, QuotaResetPeriod: model.SubscriptionResetNever}
	require.NoError(t, model.DB.Create(plan).Error)
	model.InvalidateSubscriptionPlanCache(plan.Id)
	t.Cleanup(func() { model.InvalidateSubscriptionPlanCache(plan.Id) })
	subscription := &model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin",
		Status: "active", StartTime: time.Now().Unix() - 60, EndTime: time.Now().Unix() + 3600,
		AmountTotal: 100, QuotaResetPeriodSnapshot: model.SubscriptionResetNever, QuotaResetGeneration: 1,
	}
	require.NoError(t, model.DB.Create(subscription).Error)
	require.NoError(t, model.DB.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: subscription.Id, Revision: 3}).Error)
	return user, token, session, subscription
}

func xcodeAdmissionRequest(router *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(`{"model":"free-model","max_tokens":0}`))
	request.Header.Set("Authorization", "Bearer sk-"+token)
	request.Header.Set("Content-Type", "application/json")
	// Untrusted funding facts must never override server admission.
	request.Header.Set("X-Funding-Scope", "general")
	request.Header.Set("X-Funding-Subscription-ID", "999")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestXCodeAdmissionAllowedRoutesUseOnlyDesignatedFunding(t *testing.T) {
	user, token, session, subscription := setupXCodeAdmissionTest(t)
	setting := user.GetSetting()
	setting.BillingPreference = "wallet_only"
	user.SetSetting(setting)
	require.NoError(t, model.DB.Model(user).Update("setting", user.Setting).Error)
	router := gin.New()
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		router.POST(path, TokenAuth(), func(c *gin.Context) {
			info := relaycommon.GenRelayInfoOpenAI(c, &dto.GeneralOpenAIRequest{Model: "free-model"})
			info.ForcePreConsume = true
			_, apiErr := service.NewBillingSession(c, info, 20)
			require.Nil(t, apiErr)
			assert.Equal(t, service.BillingSourceSubscription, info.BillingSource)
			assert.Equal(t, subscription.Id, info.SubscriptionId)
			c.Status(http.StatusNoContent)
		})
	}
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		response := xcodeAdmissionRequest(router, http.MethodPost, path, token.Key)
		assert.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	}
	require.NoError(t, model.DB.Model(session).Update("version", session.Version+1).Error)
	response := xcodeAdmissionRequest(router, http.MethodPost, "/v1/responses", token.Key)
	assert.Equal(t, http.StatusNoContent, response.Code, "ordinary refresh must not revoke the bound relay key")
	require.NoError(t, model.DB.First(subscription, subscription.Id).Error)
	assert.Equal(t, int64(80), subscription.AmountUsed, "all admitted requests debit their designated instance")
	var observedUser model.User
	require.NoError(t, model.DB.First(&observedUser, user.Id).Error)
	assert.Equal(t, user.Quota, observedUser.Quota, "managed requests never debit the wallet")
	assert.Equal(t, "wallet_only", observedUser.GetSetting().BillingPreference, "managed funding must not rewrite native preferences")
}

func TestXCodeAdmissionDeniedMethodPathAndKeyEntrypoints(t *testing.T) {
	_, token, _, _ := setupXCodeAdmissionTest(t)
	for _, entrypoint := range []struct {
		name string
		auth gin.HandlerFunc
	}{
		{"relay", TokenAuth()}, {"readonly", TokenAuthReadOnly()},
		{"token-or-user", TokenOrUserAuth()}, {"artifact-key", TokenOrTaskArtifactAccessAuth("key", "artifact_key")},
	} {
		t.Run(entrypoint.name, func(t *testing.T) {
			router := gin.New()
			router.RedirectTrailingSlash = false
			called := false
			for _, route := range []struct{ method, path string }{
				{http.MethodGet, "/v1/models"}, {http.MethodGet, "/v1/models/model"},
				{http.MethodGet, "/v1beta/models"}, {http.MethodGet, "/v1beta/openai/models"},
				{http.MethodGet, "/api/usage/token/"}, {http.MethodGet, "/api/log/token"},
				{http.MethodGet, "/dashboard/billing/subscription"}, {http.MethodGet, "/v1/dashboard/billing/subscription"},
				{http.MethodPost, "/v1/responses/compact"}, {http.MethodGet, "/v1/responses"},
				{http.MethodGet, "/v1/realtime"}, {http.MethodGet, "/v1/messages"},
				{http.MethodPut, "/v1/chat/completions"}, {http.MethodDelete, "/v1/responses"},
				{http.MethodPost, "/v1/messages/count_tokens"}, {http.MethodPost, "/v1/chat/completions/"},
				{http.MethodPost, "/v1/images/generations"}, {http.MethodPost, "/v1/images/edits"},
				{http.MethodPost, "/v1/video/generations"}, {http.MethodPost, "/v1/videos"},
				{http.MethodGet, "/v1/videos/video/content"}, {http.MethodPost, "/v1/tasks/plugin"},
				{http.MethodGet, "/v1/tasks/task"}, {http.MethodGet, "/v1/tasks/task/artifacts"},
				{http.MethodGet, "/v1/tasks/task/artifacts/artifact/content"}, {http.MethodHead, "/v1/tasks/task/artifacts/artifact/content"},
				{http.MethodPost, "/mj/submit/imagine"}, {http.MethodGet, "/vendor/plugin/task"},
			} {
				router.Handle(route.method, route.path, entrypoint.auth, func(c *gin.Context) {
					called = true
					c.Status(http.StatusNoContent)
				})
				response := xcodeAdmissionRequest(router, route.method, route.path, token.Key)
				assert.Equal(t, http.StatusForbidden, response.Code, route.method+" "+route.path+" "+response.Body.String())
				assert.Contains(t, response.Body.String(), `"code":"XCODE_RELAY_SCOPE_DENIED"`)
			}
			assert.False(t, called, "unsupported keys must be rejected before dispatch")
		})
	}
}

func TestXCodeAdmissionSessionPlanAndTokenFailures(t *testing.T) {
	for _, test := range []struct {
		name, code string
		status     int
		mutate     func(*testing.T, *model.User, *model.Token, *model.UserSession, *model.UserSubscription)
	}{
		{"missing plan even for zero estimate", "XCODE_PLAN_UNASSIGNED", http.StatusForbidden, func(t *testing.T, u *model.User, _ *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Where("user_id = ?", u.Id).Delete(&model.XCodeAuthorization{}).Error)
		}},
		{"expired plan", "XCODE_PLAN_EXPIRED", http.StatusForbidden, func(t *testing.T, _ *model.User, _ *model.Token, _ *model.UserSession, s *model.UserSubscription) {
			require.NoError(t, model.DB.Model(s).Update("end_time", time.Now().Unix()-1).Error)
		}},
		{"revoked plan", "XCODE_PLAN_REVOKED", http.StatusForbidden, func(t *testing.T, _ *model.User, _ *model.Token, _ *model.UserSession, s *model.UserSubscription) {
			require.NoError(t, model.DB.Model(s).Update("status", "cancelled").Error)
		}},
		{"cross user plan", "XCODE_PLAN_REVOKED", http.StatusForbidden, func(t *testing.T, _ *model.User, _ *model.Token, _ *model.UserSession, s *model.UserSubscription) {
			require.NoError(t, model.DB.Model(s).Update("user_id", s.UserId+1).Error)
		}},
		{"revoked session", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, _ *model.User, _ *model.Token, s *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(s).Update("status", model.UserSessionStatusRevoked).Error)
		}},
		{"expired session", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, _ *model.User, _ *model.Token, s *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(s).Update("expires_at", time.Now().Unix()-1).Error)
		}},
		{"cross user session", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, _ *model.User, _ *model.Token, s *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(s).Update("user_id", s.UserID+1).Error)
		}},
		{"dashboard session", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, _ *model.User, _ *model.Token, s *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(s).Update("client_purpose", model.ClientPurposeDashboard).Error)
		}},
		{"auth version changed", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, u *model.User, _ *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(u).Update("auth_version", u.AuthVersion+1).Error)
		}},
		{"user disabled", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, u *model.User, _ *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(u).Update("status", common.UserStatusDisabled).Error)
		}},
		{"missing session binding", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, _ *model.User, token *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(token).Update("client_session_sid", "").Error)
		}},
		{"unknown purpose", "XCODE_CREDENTIAL_SCOPE_DENIED", http.StatusForbidden, func(t *testing.T, _ *model.User, token *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(token).Update("client_purpose", "unknown").Error)
		}},
		{"general with bound session", "XCODE_CREDENTIAL_SCOPE_DENIED", http.StatusForbidden, func(t *testing.T, _ *model.User, token *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(token).Update("client_purpose", model.ClientPurposeGeneral).Error)
		}},
		{"empty with bound session", "XCODE_CREDENTIAL_SCOPE_DENIED", http.StatusForbidden, func(t *testing.T, _ *model.User, token *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(token).Update("client_purpose", "").Error)
		}},
		{"disabled token", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, _ *model.User, token *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(token).Update("status", common.TokenStatusDisabled).Error)
		}},
		{"expired token", "XCODE_SESSION_INVALID", http.StatusUnauthorized, func(t *testing.T, _ *model.User, token *model.Token, _ *model.UserSession, _ *model.UserSubscription) {
			require.NoError(t, model.DB.Model(token).Update("expired_time", time.Now().Unix()-1).Error)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, token, session, subscription := setupXCodeAdmissionTest(t)
			test.mutate(t, user, token, session, subscription)
			for _, auth := range []gin.HandlerFunc{TokenAuth(), TokenAuthReadOnly(), TokenOrUserAuth()} {
				router := gin.New()
				called := false
				router.POST("/v1/messages", auth, func(c *gin.Context) { called = true; c.Status(http.StatusNoContent) })
				response := xcodeAdmissionRequest(router, http.MethodPost, "/v1/messages", token.Key)
				assert.Equal(t, test.status, response.Code, response.Body.String())
				assert.Contains(t, response.Body.String(), `"code":"`+test.code+`"`)
				assert.Contains(t, response.Body.String(), `"type":"error"`, "Anthropic errors retain their top-level discriminator")
				assert.False(t, called)
			}
		})
	}
}

func TestXCodeAdmissionBackendFailuresAreUnavailable(t *testing.T) {
	for _, table := range []string{"users", "user_sessions", "x_code_authorizations", "user_subscriptions", "database_clock"} {
		t.Run(table, func(t *testing.T) {
			_, token, _, _ := setupXCodeAdmissionTest(t)
			const callbackName = "test:xcode-admission-backend-failure"
			callbacks := model.DB.Callback().Query()
			before := "gorm:query"
			if table == "database_clock" {
				callbacks = model.DB.Callback().Row()
				before = "gorm:row"
			}
			require.NoError(t, callbacks.Before(before).Register(callbackName, func(tx *gorm.DB) {
				if table == "database_clock" || tx.Statement.Table == table {
					tx.AddError(errors.New("forced backend failure"))
				}
			}))
			t.Cleanup(func() { callbacks.Remove(callbackName) })
			router := gin.New()
			router.POST("/v1/responses", TokenAuth(), func(c *gin.Context) { t.Error("backend failure reached handler") })
			response := xcodeAdmissionRequest(router, http.MethodPost, "/v1/responses", token.Key)
			assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), `"code":"XCODE_BACKEND_UNAVAILABLE"`)
		})
	}
}

func TestXCodeAdmissionMalformedTokenCacheStillRejectsHTTP(t *testing.T) {
	_, token, _, _ := setupXCodeAdmissionTest(t)
	_, client := useRateLimitMiniRedis(t)
	router := gin.New()
	router.GET("/api/log/token", TokenAuthReadOnly(), func(c *gin.Context) { t.Error("managed readonly credential reached handler") })
	router.GET("/v1/models", TokenAuth(), func(c *gin.Context) { t.Error("managed relay credential reached models handler") })
	key := "token:" + common.GenerateHMAC(token.Key)
	for _, field := range []string{"CacheSchema", "ClientPurpose", "ClientSessionSID"} {
		t.Run(field, func(t *testing.T) {
			for _, path := range []string{"/api/log/token", "/v1/models"} {
				_, err := model.GetTokenByKey(token.Key, false)
				require.NoError(t, err)
				require.NoError(t, client.HDel(client.Context(), key, field).Err())
				response := xcodeAdmissionRequest(router, http.MethodGet, path, token.Key)
				assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
				assert.Contains(t, response.Body.String(), `"code":"XCODE_RELAY_SCOPE_DENIED"`)
			}
			assert.Equal(t, model.ClientPurposeXCode, client.HGet(client.Context(), key, "ClientPurpose").Val())
			assert.Equal(t, token.ClientSessionSID, client.HGet(client.Context(), key, "ClientSessionSID").Val())
			assert.NotEmpty(t, client.HGet(client.Context(), key, "CacheSchema").Val())
		})
	}
}

func TestXCodeAdmissionNativeReadonlyAndIndependentCapability(t *testing.T) {
	_, token, _, _ := setupXCodeAdmissionTest(t)
	native := &model.Token{
		UserId: token.UserId, Key: "nativereadonlykey", Status: common.TokenStatusExpired,
		ExpiredTime: time.Now().Unix() - 1, RemainQuota: 0,
	}
	require.NoError(t, model.DB.Create(native).Error)
	router := gin.New()
	router.GET("/api/log/token", TokenAuthReadOnly(), func(c *gin.Context) {
		assert.Equal(t, model.ClientPurposeGeneral, common.GetContextKeyString(c, constant.ContextKeyClientPurpose))
		assert.Equal(t, model.SubscriptionUsageScopeGeneral, common.GetContextKeyString(c, constant.ContextKeyFundingScope))
		assert.Zero(t, common.GetContextKeyInt(c, constant.ContextKeyFundingSubscriptionID))
		assert.Zero(t, c.GetInt64(string(constant.ContextKeyFundingAuthorizationRevision)))
		c.Status(http.StatusNoContent)
	})
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "xcode-independent-capability-test"
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	access, err := service.IssueTaskArtifactAccess("task-1", "artifact-1")
	require.NoError(t, err)
	router.GET("/v1/tasks/:key/artifacts/:artifact_key/content", TokenOrTaskArtifactAccessAuth("key", "artifact_key"), func(c *gin.Context) {
		assert.True(t, IsTaskArtifactAccess(c))
		assert.Empty(t, common.GetContextKeyString(c, constant.ContextKeyClientPurpose))
		c.Status(http.StatusNoContent)
	})
	assert.Equal(t, http.StatusNoContent, xcodeAdmissionRequest(router, http.MethodGet, "/api/log/token", native.Key).Code)
	require.NoError(t, model.DB.Model(native).Update("client_purpose", model.ClientPurposeGeneral).Error)
	assert.Equal(t, http.StatusNoContent, xcodeAdmissionRequest(router, http.MethodGet, "/api/log/token", native.Key).Code)
	response := xcodeAdmissionRequest(router, http.MethodGet, "/v1/tasks/task-1/artifacts/artifact-1/content?access="+urlQueryEscape(access), token.Key)
	assert.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	response = xcodeAdmissionRequest(router, http.MethodGet, "/v1/tasks/task-2/artifacts/artifact-1/content?access="+urlQueryEscape(access), token.Key)
	assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
}

func TestXCodeAdmissionAlternateAllowedKeyEntrypoints(t *testing.T) {
	_, token, _, subscription := setupXCodeAdmissionTest(t)
	for _, auth := range []gin.HandlerFunc{TokenAuthReadOnly(), TokenOrUserAuth()} {
		router := gin.New()
		for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
			router.POST(path, auth, func(c *gin.Context) {
				assert.Equal(t, subscription.Id, common.GetContextKeyInt(c, constant.ContextKeyFundingSubscriptionID))
				assert.Equal(t, model.SubscriptionUsageScopeXCode, common.GetContextKeyString(c, constant.ContextKeyFundingScope))
				c.Status(http.StatusNoContent)
			})
			response := xcodeAdmissionRequest(router, http.MethodPost, path, token.Key)
			assert.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
		}
	}
}

func TestXCodeAdmissionAcceptsOnlyPostOnExactRoutes(t *testing.T) {
	_, token, _, _ := setupXCodeAdmissionTest(t)
	router := gin.New()
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
			router.Handle(method, path, TokenAuth(), func(c *gin.Context) { t.Error("non-POST managed request reached relay handler") })
			response := xcodeAdmissionRequest(router, method, path, token.Key)
			assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), `"code":"XCODE_RELAY_SCOPE_DENIED"`)
		}
	}
	router.POST("/v1/:endpoint", TokenAuth(), func(c *gin.Context) { t.Error("parameterized alias reached handler") })
	response := xcodeAdmissionRequest(router, http.MethodPost, "/v1/responses", token.Key)
	assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"code":"XCODE_RELAY_SCOPE_DENIED"`)
}

func TestXCodeAdmissionKeyHeaderAndQueryDispatch(t *testing.T) {
	_, token, _, subscription := setupXCodeAdmissionTest(t)
	for _, test := range []struct {
		name, method, path, header, value string
		status                            int
	}{
		{"anthropic relay", http.MethodPost, "/v1/messages", "x-api-key", "sk-" + token.Key, http.StatusNoContent},
		{"anthropic models", http.MethodGet, "/v1/models", "x-api-key", "sk-" + token.Key, http.StatusForbidden},
		{"google models", http.MethodGet, "/v1beta/models", "x-goog-api-key", "sk-" + token.Key, http.StatusForbidden},
		{"google query", http.MethodGet, "/v1/models?key=sk-" + token.Key, "", "", http.StatusForbidden},
		{"responses websocket", http.MethodGet, "/v1/responses", "Sec-WebSocket-Protocol", "responses, openai-insecure-api-key.sk-" + token.Key, http.StatusForbidden},
		{"midjourney secret", http.MethodPost, "/mj/submit/imagine", "mj-api-secret", "sk-" + token.Key, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			path, _, _ := strings.Cut(test.path, "?")
			router.Handle(test.method, path, TokenAuth(), func(c *gin.Context) {
				assert.Equal(t, subscription.Id, common.GetContextKeyInt(c, constant.ContextKeyFundingSubscriptionID))
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.header != "" {
				request.Header.Set(test.header, test.value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			if test.status == http.StatusForbidden {
				assert.Contains(t, response.Body.String(), `"code":"XCODE_RELAY_SCOPE_DENIED"`)
			}
		})
	}
}

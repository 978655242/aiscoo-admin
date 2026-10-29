package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func xcodeAPIError(c *gin.Context, status int, code, message string) {
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(status, gin.H{
		"success": false, "code": code, "message": strings.ReplaceAll(strings.ReplaceAll(message, "XCode", common.DefaultSystemName), "XWork", common.DefaultSystemName),
		"request_id": c.GetString(common.RequestIdKey),
	})
}

func xcodeReadError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrXCodeUsageRequestInvalid) {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_USAGE_REQUEST_INVALID", "Invalid usage date range or time zone")
		return
	}
	logger.LogError(c, "XCode read unavailable: "+common.LocalLogPreview(err.Error()))
	if errors.Is(err, service.ErrXCodeUsageUnavailable) {
		xcodeAPIError(c, http.StatusServiceUnavailable, "XCODE_USAGE_UNAVAILABLE", "Recorded account usage is temporarily unavailable")
		return
	}
	xcodeAPIError(c, http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE", "Account data is temporarily unavailable")
}

func GetXCodeMe(c *gin.Context) {
	snapshot, err := service.GetXCodeMe(c.GetInt("id"), c.GetString("session_id"))
	if err != nil {
		xcodeReadError(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func GetXCodePlans(c *gin.Context) {
	snapshot, err := service.GetXCodePlans()
	if err != nil {
		xcodeReadError(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func GetXCodeEntitlements(c *gin.Context) {
	snapshot, err := service.GetXCodeEntitlements(c.GetInt("id"))
	if err != nil {
		xcodeReadError(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func GetXCodeUsage(c *gin.Context) {
	snapshot, err := service.GetXCodeAccountUsage(c.GetInt("id"), dto.XCodeUsageQuery{
		Range: c.Query("range"), TimeZone: c.Query("time_zone"),
		StartDate: c.Query("start_date"), EndDate: c.Query("end_date"),
	}, time.Now())
	if err != nil {
		xcodeReadError(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func GetXCodeClientConfig(c *gin.Context) {
	snapshot, err := service.GetXCodeClientConfig()
	if err != nil {
		xcodeReadError(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func GetXCodeModels(c *gin.Context) {
	snapshot, err := service.GetXCodeModelCatalog(c.GetInt("id"))
	if err != nil {
		logger.LogError(c, "XCode model catalog unavailable: "+err.Error())
		xcodeAPIError(c, http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE", "Model catalog is temporarily unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, snapshot)
}

func xcodeAuthorizationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrXCodeAuthorizationConflict), errors.Is(err, model.ErrSubscriptionRequestConflict):
		xcodeAPIError(c, http.StatusConflict, "XCODE_AUTHORIZATION_CONFLICT", "Authorization changed or this action is not valid for its current state")
	case errors.Is(err, model.ErrXCodePlanConfigInvalid):
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_PLAN_CONFIG_INVALID", "The enabled plan must allow unlimited historical purchases and have a valid duration and quota configuration")
	case errors.Is(err, model.ErrXCodePaymentComplianceRequired):
		xcodeAPIError(c, http.StatusForbidden, "XCODE_PLAN_CONFIG_INVALID", "An administrator must confirm the existing payment compliance declaration first")
	default:
		logger.LogError(c, "XCode authorization unavailable: "+err.Error())
		xcodeAPIError(c, http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE", "Authorization is temporarily unavailable")
	}
}

func AdminGetXCodeAuthorization(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Invalid target user ID")
		return
	}
	state, err := model.GetXCodeAuthorizationState(userID)
	if err != nil {
		xcodeAuthorizationError(c, err)
		return
	}
	snapshot, err := service.XCodeAuthorizationSnapshot(state)
	if err != nil {
		xcodeAuthorizationError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, snapshot)
}

func AdminMutateXCodeAuthorization(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Invalid target user ID")
		return
	}
	var input dto.XCodeAuthorizationMutation
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Invalid authorization request")
		return
	}
	revision, err := strconv.ParseInt(input.ExpectedRevision, 10, 64)
	if err != nil || revision < 0 || strconv.FormatInt(revision, 10) != input.ExpectedRevision {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "expected_revision must be a nonnegative decimal string")
		return
	}
	planID := 0
	switch input.Action {
	case "grant", "renew", "replace":
		planID, err = strconv.Atoi(input.PlanID)
		if err != nil || planID <= 0 || strconv.Itoa(planID) != input.PlanID || input.AdvanceResetTime != nil {
			xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "This action requires a valid plan_id decimal string")
			return
		}
	case "reset":
		if input.AdvanceResetTime == nil || input.PlanID != "" {
			xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "reset requires an explicit advance_reset_time boolean and no plan_id")
			return
		}
	case "revoke":
		if input.PlanID != "" || input.AdvanceResetTime != nil {
			xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "revoke does not accept plan or reset parameters")
			return
		}
	default:
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Unknown authorization action")
		return
	}
	advance := input.AdvanceResetTime != nil && *input.AdvanceResetTime
	state, mutationErr := model.MutateXCodeAuthorization(userID, input.Action, revision, planID, advance)
	if state != nil && state.Audit != nil {
		params := map[string]any{
			"action": input.Action, "old_subscription_id": state.Audit.OldSubscriptionID,
			"new_subscription_id":    state.Authorization.SubscriptionId,
			"authorization_revision": strconv.FormatInt(state.Authorization.Revision, 10),
			"old_end_time":           state.Audit.OldEndTime, "new_end_time": state.Subscription.EndTime,
			"old_last_reset_time": state.Audit.OldLastResetTime, "new_last_reset_time": state.Subscription.LastResetTime,
			"old_next_reset_time": state.Audit.OldNextResetTime, "new_next_reset_time": state.Subscription.NextResetTime,
			"old_reset_generation": state.Audit.OldResetGeneration, "new_reset_generation": state.Subscription.QuotaResetGeneration,
			"advance_reset_time": advance,
		}
		recordManageAuditFor(c, userID, "xcode.authorization."+input.Action, params)
	}
	if mutationErr != nil {
		xcodeAuthorizationError(c, mutationErr)
		return
	}
	snapshot, err := service.XCodeAuthorizationSnapshot(state)
	if err != nil {
		xcodeAuthorizationError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, snapshot)
}

func xcodeAuthenticationError(c *gin.Context, err error) {
	status, code := http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE"
	switch {
	case errors.Is(err, service.ErrXCodeAuthRequestInvalid):
		status, code = http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID"
	case errors.Is(err, service.ErrXCodeAuthCodeInvalid):
		status, code = http.StatusBadRequest, "XCODE_AUTH_CODE_INVALID"
	case errors.Is(err, model.ErrUserTokenLimit):
		status, code = http.StatusConflict, "XCODE_TOKEN_LIMIT"
	case errors.Is(err, service.ErrAuthCredentialScopeDenied):
		status, code = http.StatusForbidden, "XCODE_CREDENTIAL_SCOPE_DENIED"
	case errors.Is(err, service.ErrAuthTokenExpired):
		status, code = http.StatusUnauthorized, "XCODE_ACCESS_EXPIRED"
	case errors.Is(err, service.ErrRefreshTokenInvalid), errors.Is(err, service.ErrAuthTokenInvalid),
		errors.Is(err, service.ErrLoginSessionInvalid), errors.Is(err, service.ErrLoginSessionRevoked),
		errors.Is(err, model.ErrUserSessionInvalid), errors.Is(err, model.ErrUserSessionInactive):
		status, code = http.StatusUnauthorized, "XCODE_SESSION_INVALID"
	case errors.Is(err, model.ErrUserSessionLimit), errors.Is(err, model.ErrUserSessionIssuanceLimit),
		errors.Is(err, service.ErrLoginSessionMismatch), errors.Is(err, service.ErrRefreshRace):
		status, code = service.AuthSessionErrorCode(err)
	}
	xcodeAPIError(c, status, code, http.StatusText(status))
}

func xcodeAuthorizationOrigin(c *gin.Context) (string, bool) {
	if !common.XCodeIntegrationEnabled() {
		xcodeAPIError(c, http.StatusNotFound, "XCODE_AUTHORIZATION_DISABLED", "XCode authorization is disabled")
		return "", false
	}
	origin, err := common.GetXCodePublicOrigin()
	if err != nil {
		xcodeAuthenticationError(c, err)
		return "", false
	}
	return origin, true
}

func StartXCodeAuthorization(c *gin.Context) {
	origin, ok := xcodeAuthorizationOrigin(c)
	if !ok {
		return
	}
	query := c.Request.URL.Query()
	for _, key := range []string{"client_id", "response_type", "redirect_uri", "state", "code_challenge", "code_challenge_method"} {
		if len(query[key]) != 1 {
			xcodeAuthenticationError(c, service.ErrXCodeAuthRequestInvalid)
			return
		}
	}
	flowToken, err := service.StartXCodeAuthorization(service.XCodeAuthorizeRequest{
		ClientID: query.Get("client_id"), ResponseType: query.Get("response_type"),
		RedirectURI: query.Get("redirect_uri"), State: query.Get("state"),
		CodeChallenge: query.Get("code_challenge"), CodeChallengeMethod: query.Get("code_challenge_method"),
	})
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	c.Redirect(http.StatusFound, origin+"/xcode/authorize?"+url.Values{"flow_token": {flowToken}}.Encode())
}

func GetXCodeAuthorizationRequest(c *gin.Context) {
	if _, ok := xcodeAuthorizationOrigin(c); !ok {
		return
	}
	view, err := service.GetXCodeAuthorizationRequest(c.Query("flow_token"))
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	common.ApiSuccess(c, dto.XCodeAuthorizationRequestView{
		SchemaVersion: 1, ServerTime: time.Now().UnixMilli(), ClientID: view.ClientID, ExpiresAt: view.ExpiresAt * 1000,
	})
}

func CompleteXCodeAuthorization(c *gin.Context) {
	if _, ok := xcodeAuthorizationOrigin(c); !ok {
		return
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		xcodeAuthenticationError(c, service.ErrAuthCredentialScopeDenied)
		return
	}
	var input struct {
		FlowToken string `json:"flow_token"`
	}
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		xcodeAuthenticationError(c, service.ErrXCodeAuthRequestInvalid)
		return
	}
	redirectURL, err := service.CompleteXCodeAuthorization(identity, input.FlowToken)
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	common.ApiSuccess(c, dto.XCodeAuthorizationCompleteResponse{
		SchemaVersion: 1, ServerTime: time.Now().UnixMilli(), RedirectURL: redirectURL,
	})
}

func ExchangeXCodeAuthorizationCode(c *gin.Context) {
	if _, ok := xcodeAuthorizationOrigin(c); !ok {
		return
	}
	var input service.XCodeTokenRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		xcodeAuthenticationError(c, service.ErrXCodeAuthCodeInvalid)
		return
	}
	bundle, err := service.ExchangeXCodeAuthorizationCode(input, c.ClientIP(), c.GetHeader("User-Agent"))
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	writeXCodeSessionBundle(c, bundle)
}

func RefreshXCodeSession(c *gin.Context) {
	var input service.XCodeRefreshRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		xcodeAuthenticationError(c, service.ErrRefreshTokenInvalid)
		return
	}
	bundle, err := service.RefreshXCodeSession(input, c.ClientIP(), c.GetHeader("User-Agent"))
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	writeXCodeSessionBundle(c, bundle)
}

func LogoutXCodeSession(c *gin.Context) {
	var input service.XCodeLogoutRequest
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		xcodeAuthenticationError(c, service.ErrLoginSessionInvalid)
		return
	}
	var identity *service.AuthIdentity
	if header := strings.TrimSpace(c.GetHeader("Authorization")); header != "" {
		scheme, raw, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			xcodeAuthenticationError(c, service.ErrAuthTokenInvalid)
			return
		}
		parsed, err := service.ParseXCodeAccessToken(strings.TrimSpace(raw))
		if err != nil {
			xcodeAuthenticationError(c, err)
			return
		}
		identity = &parsed
	}
	if err := service.LogoutXCodeSession(identity, input); err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"schema_version": 1, "server_time": time.Now().UnixMilli(), "revoked": true})
}

func writeXCodeSessionBundle(c *gin.Context, bundle *service.XCodeSessionBundle) {
	if bundle == nil || bundle.User == nil || bundle.Session == nil || bundle.Token == nil {
		xcodeAuthenticationError(c, service.ErrXCodeBackendUnavailable)
		return
	}
	for _, deadline := range [3]int64{bundle.AccessExpiresAt, bundle.RefreshExpiresAt, bundle.Token.ExpiredTime} {
		if deadline <= 0 || deadline > 9007199254740991/1000 {
			xcodeAuthenticationError(c, service.ErrXCodeBackendUnavailable)
			return
		}
	}
	common.ApiSuccess(c, dto.XCodeSessionBundle{
		SchemaVersion: 1, ServerTime: time.Now().UnixMilli(), AccessToken: bundle.AccessToken,
		AccessExpiresAt: bundle.AccessExpiresAt * 1000, RefreshToken: bundle.RefreshToken,
		RefreshExpiresAt: bundle.RefreshExpiresAt * 1000, TokenType: "Bearer", SessionID: bundle.Session.SID,
		User:            dto.XCodeUser{ID: strconv.Itoa(bundle.User.Id), Username: bundle.User.Username, DisplayName: bundle.User.DisplayName},
		RelayCredential: dto.XCodeRelayCredential{Token: "sk-" + bundle.Token.GetFullKey(), ExpiresAt: bundle.Token.ExpiredTime * 1000},
	})
}

package middleware

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// validateTokenPurposeRoute runs as soon as a stored key is resolved, including
// before native token-state errors. Unsupported managed routes always fail closed.
func validateTokenPurposeRoute(c *gin.Context, token *model.Token) error {
	switch token.ClientPurpose {
	case "", model.ClientPurposeGeneral:
		if token.ClientSessionSID != "" {
			return abortXCodeAdmission(c, http.StatusForbidden, "XCODE_CREDENTIAL_SCOPE_DENIED", "Invalid credential purpose binding")
		}
	case model.ClientPurposeXCode:
		path := c.Request.URL.Path
		if c.Request.Method != http.MethodPost || c.FullPath() != path ||
			(path != "/v1/messages" && path != "/v1/chat/completions" && path != "/v1/responses") {
			return abortXCodeAdmission(c, http.StatusForbidden, "XCODE_RELAY_SCOPE_DENIED", "This route is not available to XCode relay credentials")
		}
	default:
		return abortXCodeAdmission(c, http.StatusForbidden, "XCODE_CREDENTIAL_SCOPE_DENIED", "Invalid credential purpose")
	}
	return nil
}

// admitTokenPurpose is shared by relay and readonly key authentication. Purpose
// and funding come only from stored credentials and the designated relationship.
func admitTokenPurpose(c *gin.Context, token *model.Token) error {
	if err := validateTokenPurposeRoute(c, token); err != nil {
		return err
	}
	if token.ClientPurpose != model.ClientPurposeXCode {
		common.SetContextKey(c, constant.ContextKeyClientPurpose, model.ClientPurposeGeneral)
		common.SetContextKey(c, constant.ContextKeyFundingScope, model.SubscriptionUsageScopeGeneral)
		common.SetContextKey(c, constant.ContextKeyFundingSubscriptionID, 0)
		common.SetContextKey(c, constant.ContextKeyFundingAuthorizationRevision, int64(0))
		return nil
	}

	now := time.Now().Unix()
	if token.ClientSessionSID == "" || token.Status != common.TokenStatusEnabled ||
		(token.ExpiredTime != -1 && token.ExpiredTime < now) || (!token.UnlimitedQuota && token.RemainQuota <= 0) {
		return abortXCodeAdmission(c, http.StatusUnauthorized, "XCODE_SESSION_INVALID", "XCode relay credential is no longer valid")
	}
	session, err := model.GetUserSessionCached(token.ClientSessionSID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, model.ErrUserSessionInvalid) || errors.Is(err, model.ErrUserSessionInactive) {
			return abortXCodeAdmission(c, http.StatusUnauthorized, "XCODE_SESSION_INVALID", "XCode session is no longer valid")
		}
		return abortXCodeAdmission(c, http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE", "XCode authentication is temporarily unavailable")
	}
	authVersion := c.GetInt64(string(constant.ContextKeyUserAuthVersion))
	// SessionVersion rotates during an ordinary refresh; it does not revoke the key.
	if session.UserID != token.UserId || session.ClientPurpose != model.ClientPurposeXCode ||
		session.Status != model.UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= now ||
		authVersion <= 0 || session.UserAuthVersion != authVersion ||
		common.GetContextKeyInt(c, constant.ContextKeyUserStatus) != common.UserStatusEnabled {
		return abortXCodeAdmission(c, http.StatusUnauthorized, "XCODE_SESSION_INVALID", "XCode session is no longer valid")
	}

	target, err := model.GetXCodeFundingTarget(token.UserId)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrXCodePlanUnassigned):
			return abortXCodeAdmission(c, http.StatusForbidden, "XCODE_PLAN_UNASSIGNED", "No XCode plan is assigned")
		case errors.Is(err, model.ErrXCodePlanExpired):
			return abortXCodeAdmission(c, http.StatusForbidden, "XCODE_PLAN_EXPIRED", "The XCode plan has expired")
		case errors.Is(err, model.ErrXCodePlanRevoked):
			return abortXCodeAdmission(c, http.StatusForbidden, "XCODE_PLAN_REVOKED", "The XCode plan has been revoked")
		default:
			return abortXCodeAdmission(c, http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE", "XCode authorization is temporarily unavailable")
		}
	}
	common.SetContextKey(c, constant.ContextKeyClientPurpose, model.ClientPurposeXCode)
	common.SetContextKey(c, constant.ContextKeyFundingScope, target.Scope)
	common.SetContextKey(c, constant.ContextKeyFundingSubscriptionID, target.SubscriptionID)
	common.SetContextKey(c, constant.ContextKeyFundingAuthorizationRevision, target.AuthorizationRevision)
	return nil
}

func abortXCodeAdmission(c *gin.Context, status int, code, message string) error {
	if c.Request.URL.Path == "/v1/messages" {
		errorType := "permission_error"
		if status == http.StatusUnauthorized {
			errorType = "authentication_error"
		} else if status >= http.StatusInternalServerError {
			errorType = "api_error"
		}
		c.AbortWithStatusJSON(status, gin.H{
			"type": "error",
			"error": gin.H{
				"type": errorType, "code": code,
				"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
			},
		})
	} else {
		abortWithOpenAiMessage(c, status, message, types.ErrorCode(code))
	}
	return errors.New(code)
}

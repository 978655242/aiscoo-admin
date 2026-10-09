package middleware

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// XCodeWebAuth accepts only the dedicated cookie on the single read-only route.
func XCodeWebAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		if c.Request.Method != http.MethodGet || c.Request.URL.Path != "/api/xcode/v1/web/view" ||
			c.FullPath() != c.Request.URL.Path || c.Request.URL.RawQuery != "" ||
			len(c.Request.Header.Values("Authorization")) != 0 {
			writeXCodeControlAuthError(c, service.ErrAuthCredentialScopeDenied)
			return
		}
		if !common.XCodeIntegrationEnabled() {
			abortXCodeFacadeError(c, http.StatusNotFound, "XCODE_AUTHORIZATION_DISABLED")
			return
		}
		origin, err := service.ValidateXCodeWebRequest(c.Request)
		if err != nil {
			if errors.Is(err, service.ErrXCodeWebOriginForbidden) {
				abortXCodeFacadeError(c, http.StatusForbidden, "XCODE_ORIGIN_FORBIDDEN")
				return
			}
			writeXCodeControlAuthError(c, err)
			return
		}
		cookies := c.Request.CookiesNamed(service.XCodeWebCookieName(origin))
		if len(cookies) != 1 || cookies[0].Value == "" || len(cookies[0].Name)+len(cookies[0].Value) > 4096 {
			writeXCodeControlAuthError(c, service.ErrAuthTokenInvalid)
			return
		}
		identity, err := service.ParseXCodeWebToken(cookies[0].Value)
		if err != nil {
			writeXCodeControlAuthError(c, err)
			return
		}
		_, user, err := service.ValidateXCodeLoginSession(identity)
		if err != nil {
			writeXCodeControlAuthError(c, err)
			return
		}
		setDashboardAuthContext(c, user, identity, false)
		common.SetContextKey(c, constant.ContextKeyClientPurpose, "xcode_web")
		c.Next()
	}
}

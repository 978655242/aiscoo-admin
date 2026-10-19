package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func xcodeWebTransport(c *gin.Context) (string, bool) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if !common.XCodeIntegrationEnabled() {
		xcodeAPIError(c, http.StatusNotFound, "XCODE_AUTHORIZATION_DISABLED", "XCode authorization is disabled")
		return "", false
	}
	origin, err := service.ValidateXCodeWebRequest(c.Request)
	if errors.Is(err, service.ErrXCodeWebOriginForbidden) {
		xcodeAPIError(c, http.StatusForbidden, "XCODE_ORIGIN_FORBIDDEN", "Request origin is not permitted")
		return "", false
	}
	if err != nil {
		xcodeAuthenticationError(c, err)
		return "", false
	}
	return origin, true
}

func CreateXCodeWebHandoff(c *gin.Context) {
	if _, ok := xcodeWebTransport(c); !ok {
		return
	}
	identity, ok := middleware.GetAuthIdentity(c)
	if !ok {
		xcodeAuthenticationError(c, service.ErrLoginSessionInvalid)
		return
	}
	response, err := service.CreateXCodeWebHandoff(identity)
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	common.ApiSuccess(c, response)
}

func ConsumeXCodeWebHandoff(c *gin.Context) {
	origin, ok := xcodeWebTransport(c)
	if !ok {
		return
	}
	c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	c.Header("X-Frame-Options", "DENY")
	if c.Request.Method != http.MethodGet || c.Request.URL.Path != "/xcode/plans" {
		xcodeAuthenticationError(c, service.ErrAuthCredentialScopeDenied)
		return
	}
	if c.Request.URL.RawQuery == "" {
		return
	}
	handoff, ok := strings.CutPrefix(c.Request.URL.RawQuery, "handoff=")
	if !ok || len(handoff) != 43 {
		xcodeAuthenticationError(c, service.ErrXCodeAuthCodeInvalid)
		return
	}
	webToken, expiresAt, err := service.ConsumeXCodeWebHandoff(handoff)
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	maxAge := int(time.Until(time.Unix(expiresAt, 0)) / time.Second)
	if maxAge <= 0 {
		xcodeAuthenticationError(c, service.ErrLoginSessionRevoked)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: service.XCodeWebCookieName(origin), Value: webToken, Path: "/api/xcode/v1/web",
		HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteStrictMode,
		MaxAge: maxAge, Expires: time.Unix(expiresAt, 0),
	})
	c.Redirect(http.StatusSeeOther, "/xcode/plans")
}

func GetXCodeWebView(c *gin.Context) {
	view, err := service.GetXCodeWebView(c.GetInt("id"))
	if err != nil {
		xcodeAuthenticationError(c, err)
		return
	}
	common.ApiSuccess(c, view)
}

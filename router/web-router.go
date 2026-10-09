package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// WebAssets holds the embedded dashboard frontend assets.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
}

func SetWebRouter(router *gin.Engine, assets WebAssets, pluginDispatcher gin.HandlerFunc) {
	frontendFS := common.EmbedFolder(assets.BuildFS, "web/dist")

	router.NoRoute(
		func(c *gin.Context) {
			if c.Request.URL.Path == "/xcode/plans" {
				controller.ConsumeXCodeWebHandoff(c)
				if c.Writer.Written() {
					c.Abort()
					return
				}
			}
			c.Next()
		},
		pluginDispatcher,
		middleware.RouteTag("web"),
		gzip.Gzip(gzip.DefaultCompression),
		middleware.AccessTokenAudit(),
		middleware.GlobalWebRateLimit(),
		middleware.Cache(),
		func(c *gin.Context) {
			path := strings.TrimRight(c.Request.URL.Path, "/")
			nativeAuthPage := false
			switch path {
			case "/sign-in", "/sign-up", "/register", "/otp", "/forgot-password", "/reset", "/oauth":
				nativeAuthPage = true
			}
			nativeAuthPage = nativeAuthPage || strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/user/register") || strings.HasPrefix(path, "/user/reset")
			if nativeAuthPage || strings.HasPrefix(path, "/xcode/") {
				c.Header("Cache-Control", "no-store")
				c.Header("Referrer-Policy", "no-referrer")
			}
			if nativeAuthPage || path == "/xcode/authorize" {
				c.Header("X-Frame-Options", "DENY")
				c.Header("Content-Security-Policy", "frame-ancestors 'none'")
			}
			c.Next()
		},
		static.Serve("/", frontendFS),
		func(c *gin.Context) {
			if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/assets") {
				controller.RelayNotFound(c)
				return
			}
			if c.Writer.Header().Get("Cache-Control") != "no-store" {
				c.Header("Cache-Control", "no-cache")
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.IndexPage)
		},
	)
}

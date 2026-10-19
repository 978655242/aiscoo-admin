package middleware

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const RouteTagKey = "route_tag"

func RouteTag(tag string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(RouteTagKey, tag)
		c.Next()
	}
}

func SetUpLogger(server *gin.Engine) {
	server.Use(redactTaskArtifactAccessQuery())
	server.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		var requestID string
		if param.Keys != nil {
			requestID, _ = param.Keys[common.RequestIdKey].(string)
		}
		tag, _ := param.Keys[RouteTagKey].(string)
		if tag == "" {
			tag = "web"
		}
		path := param.Path
		logPath, _, _ := strings.Cut(path, "?")
		logPath = strings.TrimRight(logPath, "/")
		nativeAuthPage := false
		switch logPath {
		case "/sign-in", "/sign-up", "/register", "/otp", "/forgot-password", "/reset", "/oauth":
			nativeAuthPage = true
		}
		// Authentication queries carry one-time codes, state and authorization flows.
		// Redact only the log value; handlers need the original query.
		if nativeAuthPage || strings.HasPrefix(logPath, "/api/oauth/") || strings.HasPrefix(logPath, "/oauth/") ||
			strings.HasPrefix(logPath, "/api/xcode/v1/auth/") || strings.HasPrefix(logPath, "/xcode/") ||
			strings.HasPrefix(logPath, "/user/register") || strings.HasPrefix(logPath, "/user/reset") {
			path, _, _ = strings.Cut(path, "?")
		}
		return fmt.Sprintf("[GIN] %s | %s | %s | %3d | %13v | %15s | %7s %s\n",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"),
			tag,
			requestID,
			param.StatusCode,
			param.Latency,
			param.ClientIP,
			param.Method,
			path,
		)
	}))
}

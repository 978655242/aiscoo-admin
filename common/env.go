package common

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func GetEnvOrDefault(env string, defaultValue int) int {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(os.Getenv(env))
	if err != nil {
		SysError(LogText("failed to parse %s: %s, using default value: %d", env, err.Error(), defaultValue))
		return defaultValue
	}
	return num
}

func GetEnvOrDefaultString(env string, defaultValue string) string {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	return os.Getenv(env)
}

func GetEnvOrDefaultBool(env string, defaultValue bool) bool {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(os.Getenv(env))
	if err != nil {
		SysError(LogText("failed to parse %s: %s, using default value: %t", env, err.Error(), defaultValue))
		return defaultValue
	}
	return b
}

func XCodeIntegrationEnabled() bool {
	return GetEnvOrDefaultBool("XCODE_INTEGRATION_ENABLED", false)
}

func GetXCodePublicOrigin() (string, error) {
	raw := strings.TrimSpace(os.Getenv("XCODE_PUBLIC_ORIGIN"))
	origin, err := url.Parse(raw)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Opaque != "" ||
		(origin.Path != "" && origin.Path != "/") || origin.RawPath != "" ||
		strings.ContainsAny(raw, "?#") || (origin.Scheme != "https" && origin.Scheme != "http") {
		return "", fmt.Errorf("XCODE_PUBLIC_ORIGIN must be an HTTPS or HTTP loopback origin without credentials, path, query or fragment")
	}
	if port := origin.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value <= 0 || value > 65535 {
			return "", fmt.Errorf("XCODE_PUBLIC_ORIGIN has an invalid port")
		}
	}
	hostname := strings.ToLower(origin.Hostname())
	if hostname == "" || strings.ContainsAny(hostname, " *%") || strings.HasSuffix(origin.Host, ":") {
		return "", fmt.Errorf("XCODE_PUBLIC_ORIGIN has an invalid hostname")
	}
	if origin.Scheme == "http" && hostname != "localhost" && !net.ParseIP(hostname).IsLoopback() {
		return "", fmt.Errorf("XCODE_PUBLIC_ORIGIN only permits HTTP on loopback")
	}
	return origin.Scheme + "://" + strings.ToLower(origin.Host), nil
}

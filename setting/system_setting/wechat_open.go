package system_setting

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

var ErrWeChatOpenConfiguration = errors.New("WeChat website login is not configured or enabled. Please contact your administrator.")

// ValidateWeChatOpenConfiguration binds the callback to the deployment's exact HTTPS origin.
func ValidateWeChatOpenConfiguration(appID, secret, callback, serverAddress string) error {
	if appID == "" || len(appID) > 64 || secret == "" || len(secret) > 256 || strings.TrimSpace(secret) != secret || strings.ContainsAny(secret, "\r\n\x00") {
		return ErrWeChatOpenConfiguration
	}
	for _, ch := range secret {
		if unicode.IsSpace(ch) || unicode.IsControl(ch) {
			return ErrWeChatOpenConfiguration
		}
	}
	for _, ch := range appID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return ErrWeChatOpenConfiguration
		}
	}
	origin, err := url.Parse(serverAddress)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || origin.RawFragment != "" || origin.Opaque != "" || (origin.Path != "" && origin.Path != "/") || origin.RawPath != "" || strings.Contains(serverAddress, "#") {
		return ErrWeChatOpenConfiguration
	}
	redirect, err := url.Parse(callback)
	if err != nil || redirect.User != nil || redirect.RawQuery != "" || redirect.ForceQuery || redirect.Fragment != "" || redirect.RawFragment != "" || redirect.Opaque != "" || redirect.RawPath != "" || callback != "https://"+origin.Host+"/oauth/wechat-open" || strings.Contains(callback, "#") {
		return ErrWeChatOpenConfiguration
	}
	return nil
}

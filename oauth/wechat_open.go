package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

const WeChatOpenProviderName = "wechat-open"

const (
	weChatOpenTokenURL    = "https://api.weixin.qq.com/sns/oauth2/access_token"
	weChatOpenUserInfoURL = "https://api.weixin.qq.com/sns/userinfo"
)

type WeChatOpenConfig struct {
	AppID       string
	AppSecret   string
	CallbackURL string
}

func GetWeChatOpenConfig() (WeChatOpenConfig, error) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	config := WeChatOpenConfig{common.WeChatOpenAppID, common.WeChatOpenAppSecret, common.WeChatOpenCallbackURL}
	if !common.WeChatOpenEnabled {
		return WeChatOpenConfig{}, system_setting.ErrWeChatOpenConfiguration
	}
	if err := system_setting.ValidateWeChatOpenConfiguration(config.AppID, config.AppSecret, config.CallbackURL, system_setting.ServerAddress); err != nil {
		return WeChatOpenConfig{}, err
	}
	return config, nil
}

func WeChatOpenFingerprint() (string, error) {
	config, err := GetWeChatOpenConfig()
	if err != nil {
		return "", err
	}
	encoded, err := common.Marshal(config)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func init() {
	Register(WeChatOpenProviderName, &WeChatOpenProvider{})
}

type WeChatOpenProvider struct{}

func (*WeChatOpenProvider) GetName() string { return "WeChat website" }
func (*WeChatOpenProvider) IsEnabled() bool {
	_, err := GetWeChatOpenConfig()
	return err == nil
}

func (*WeChatOpenProvider) ExchangeToken(ctx context.Context, code string, _ *gin.Context) (*OAuthToken, error) {
	if code == "" || len(code) > 512 {
		return nil, NewOAuthError(common.NewMessage(msgInvalidCode))
	}
	config, err := GetWeChatOpenConfig()
	if err != nil {
		return nil, NewOAuthError(common.NewMessage(err.Error()))
	}
	query := url.Values{"appid": {config.AppID}, "secret": {config.AppSecret}, "code": {code}, "grant_type": {"authorization_code"}}
	var response struct {
		AccessToken string `json:"access_token"`
		OpenID      string `json:"openid"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
		ErrorCode   int    `json:"errcode"`
	}
	if err := weChatOpenGET(ctx, weChatOpenTokenURL, query, &response); err != nil {
		return nil, err
	}
	if response.ErrorCode != 0 || response.AccessToken == "" || model.ValidateWeChatOpenIdentity(config.AppID+":"+response.OpenID) != nil {
		return nil, NewOAuthError(common.NewMessage(msgTokenFailed, map[string]any{"provider": "WeChat website"}))
	}
	return &OAuthToken{AccessToken: response.AccessToken, ExpiresIn: response.ExpiresIn, Scope: response.Scope, ClientID: config.AppID, ProviderUserID: response.OpenID}, nil
}

func (*WeChatOpenProvider) GetUserInfo(ctx context.Context, token *OAuthToken) (*OAuthUser, error) {
	if token == nil || token.AccessToken == "" || model.ValidateWeChatOpenIdentity(token.ClientID+":"+token.ProviderUserID) != nil {
		return nil, NewOAuthError(common.NewMessage(msgGetUserFailed))
	}
	query := url.Values{"access_token": {token.AccessToken}, "openid": {token.ProviderUserID}, "lang": {"zh_CN"}}
	var response struct {
		OpenID    string `json:"openid"`
		Nickname  string `json:"nickname"`
		ErrorCode int    `json:"errcode"`
	}
	if err := weChatOpenGET(ctx, weChatOpenUserInfoURL, query, &response); err != nil {
		return nil, err
	}
	if response.ErrorCode != 0 || response.OpenID != token.ProviderUserID {
		return nil, NewOAuthError(common.NewMessage(msgGetUserFailed))
	}
	return &OAuthUser{ProviderUserID: token.ClientID + ":" + response.OpenID, DisplayName: response.Nickname}, nil
}

// Neither upstream URLs nor response errors are logged: they may contain credentials.
func weChatOpenGET(ctx context.Context, endpoint string, query url.Values, response any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return NewOAuthError(common.NewMessage(msgGetUserFailed))
	}
	req.Header.Set("Accept", "application/json")
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return NewOAuthError(common.NewMessage(msgConnectFailed, map[string]any{"provider": "WeChat website"}))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || common.DecodeJson(io.LimitReader(res.Body, 1<<20), response) != nil {
		return NewOAuthError(common.NewMessage(msgGetUserFailed))
	}
	return nil
}

func (*WeChatOpenProvider) IsUserIDTaken(providerUserID string) bool {
	var count int64
	return model.DB.Unscoped().Model(&model.User{}).Where("wechat_open_id = ?", providerUserID).Count(&count).Error == nil && count > 0
}
func (*WeChatOpenProvider) FillUserByProviderID(user *model.User, providerUserID string) error {
	if err := model.ValidateWeChatOpenIdentity(providerUserID); err != nil {
		return err
	}
	if err := model.DB.Unscoped().Where("wechat_open_id = ?", providerUserID).First(user).Error; err != nil {
		return err
	}
	if user.DeletedAt.Valid || user.Status != common.UserStatusEnabled {
		return model.ErrWeChatOpenUserUnavailable
	}
	return nil
}
func (*WeChatOpenProvider) SetProviderUserID(user *model.User, providerUserID string) {
	user.WeChatOpenId = &providerUserID
}
func (*WeChatOpenProvider) GetProviderPrefix() string    { return "wechat_" }
func (*WeChatOpenProvider) ProviderUserIDColumn() string { return "wechat_open_id" }

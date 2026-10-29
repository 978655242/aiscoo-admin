package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const (
	XCodeClientID    = "xcode-desktop"
	XCodeRedirectURI = "zcode://oauth/callback"
	XWorkCLIClientID = "xwork-cli"
)

var (
	ErrXCodeAuthRequestInvalid = errors.New("xcode authorization request is invalid")
	ErrXCodeAuthCodeInvalid    = errors.New("xcode authorization code is invalid")
	ErrXCodeBackendUnavailable = errors.New("xcode authentication backend is unavailable")
)

type XCodeAuthorizeRequest struct {
	ClientID            string `json:"client_id"`
	ResponseType        string `json:"response_type"`
	RedirectURI         string `json:"redirect_uri"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
}

type XCodeTokenRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	RedirectURI  string `json:"redirect_uri"`
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

type XCodeRefreshRequest struct {
	SessionID    string `json:"session_id"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
}

type XCodeLogoutRequest struct {
	SessionID    string `json:"session_id"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
}

type XCodeAuthorizationRequestView struct {
	ClientID  string
	ExpiresAt int64
}

// XCodeSessionBundle stays internal; only the facade's narrow DTO may serialize it.
type XCodeSessionBundle struct {
	User             *model.User
	Session          *model.UserSession
	Token            *model.Token
	AccessToken      string
	AccessExpiresAt  int64
	RefreshToken     string
	RefreshExpiresAt int64
}

type xcodeAuthorizationState struct {
	Request          XCodeAuthorizeRequest `json:"request"`
	UserAuthVersion  int64                 `json:"auth_version,omitempty"`
	IssuedSessionSID string                `json:"issued_session_sid,omitempty"`
}

func xcodeUnreserved(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~') {
			return false
		}
	}
	return true
}

// CLI 只允许字面回环地址，避免 URL 规范化把别名或转义路径扩大为已注册回调。
func validXCodeClientRedirect(clientID, redirect string) bool {
	if clientID == XCodeClientID {
		return redirect == XCodeRedirectURI
	}
	if clientID != XWorkCLIClientID {
		return false
	}
	u, err := url.Parse(redirect)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port >= 1 && port <= 65535 && redirect == "http://127.0.0.1:"+strconv.Itoa(port)+"/oauth/callback"
}

func validateXCodeAuthorizationRequest(input XCodeAuthorizeRequest) bool {
	if !validXCodeClientRedirect(input.ClientID, input.RedirectURI) || input.ResponseType != "code" ||
		input.CodeChallengeMethod != "S256" || !xcodeUnreserved(input.State, 22, 256) || len(input.CodeChallenge) != 43 {
		return false
	}
	digest, err := base64.RawURLEncoding.Strict().DecodeString(input.CodeChallenge)
	return err == nil && len(digest) == sha256.Size && base64.RawURLEncoding.EncodeToString(digest) == input.CodeChallenge
}

func StartXCodeAuthorization(input XCodeAuthorizeRequest) (string, error) {
	if !validateXCodeAuthorizationRequest(input) {
		return "", ErrXCodeAuthRequestInvalid
	}
	payload, err := common.Marshal(xcodeAuthorizationState{Request: input})
	if err != nil {
		return "", err
	}
	token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeXCodeRequest, Payload: string(payload), ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	return token, err
}

func GetXCodeAuthorizationRequest(flowToken string) (*XCodeAuthorizationRequestView, error) {
	flow, err := model.GetAuthFlow(flowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeXCodeRequest})
	if err != nil {
		return nil, xcodeFlowError(err, ErrXCodeAuthRequestInvalid)
	}
	var state xcodeAuthorizationState
	if flow.UserId != 0 || flow.SessionId != "" || common.UnmarshalJsonStr(flow.Payload, &state) != nil || !validateXCodeAuthorizationRequest(state.Request) {
		return nil, ErrXCodeAuthRequestInvalid
	}
	return &XCodeAuthorizationRequestView{ClientID: state.Request.ClientID, ExpiresAt: flow.ExpiresAt.Unix()}, nil
}

func CompleteXCodeAuthorization(identity AuthIdentity, flowToken string) (string, error) {
	if _, ok := model.ParseAccessTokenSessionID(identity.SessionID); ok {
		return "", ErrAuthCredentialScopeDenied
	}
	if _, _, err := ValidateLoginSession(identity); err != nil {
		return "", err
	}
	var code, stateValue, redirectURI string
	_, err := model.ConsumeAuthFlowWithAction(flowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeXCodeRequest}, func(tx *gorm.DB, flow *model.AuthFlow) error {
		var state xcodeAuthorizationState
		if flow.UserId != 0 || flow.SessionId != "" || common.UnmarshalJsonStr(flow.Payload, &state) != nil || !validateXCodeAuthorizationRequest(state.Request) {
			return ErrXCodeAuthRequestInvalid
		}
		if _, _, err := model.ValidateXCodeSourceSessionWithTx(tx, identity.UserID, identity.SessionID, identity.UserAuthVersion, identity.SessionVersion, model.ClientPurposeDashboard); err != nil {
			return xcodeSourceError(err, ErrLoginSessionRevoked)
		}
		state.UserAuthVersion = identity.UserAuthVersion
		payload, err := common.Marshal(state)
		if err != nil {
			return err
		}
		code, _, err = model.CreateAuthFlowWithTx(tx, model.AuthFlowCreate{
			Purpose: model.AuthFlowPurposeXCodeCode, UserId: identity.UserID, SessionId: identity.SessionID,
			Payload: string(payload), ExpiresAt: time.Now().Add(time.Minute),
		})
		if err != nil {
			return err
		}
		stateValue = state.Request.State
		redirectURI = state.Request.RedirectURI
		_, _, err = model.ValidateXCodeSourceSessionWithTx(tx, identity.UserID, identity.SessionID, identity.UserAuthVersion, identity.SessionVersion, model.ClientPurposeDashboard)
		return xcodeSourceError(err, ErrLoginSessionRevoked)
	})
	if err != nil {
		return "", xcodeFlowError(err, ErrXCodeAuthRequestInvalid)
	}
	query := url.Values{"provider": {"new-api"}, "code": {code}, "state": {stateValue}}
	return redirectURI + "?" + query.Encode(), nil
}

func xcodeCodeProofMatches(flow *model.AuthFlow, state *xcodeAuthorizationState, input XCodeTokenRequest) bool {
	if flow.UserId <= 0 || flow.SessionId == "" || state.UserAuthVersion <= 0 || !validateXCodeAuthorizationRequest(state.Request) ||
		state.Request.ClientID != input.ClientID || state.Request.RedirectURI != input.RedirectURI {
		return false
	}
	digest := sha256.Sum256([]byte(input.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	return subtle.ConstantTimeCompare([]byte(challenge), []byte(state.Request.CodeChallenge)) == 1
}

func ExchangeXCodeAuthorizationCode(input XCodeTokenRequest, ip, userAgent string) (*XCodeSessionBundle, error) {
	if input.GrantType != "authorization_code" || !validXCodeClientRedirect(input.ClientID, input.RedirectURI) ||
		!xcodeUnreserved(input.Code, 43, 43) || !xcodeUnreserved(input.CodeVerifier, 43, 128) {
		return nil, ErrXCodeAuthCodeInvalid
	}
	cacheDeadline := model.UserSessionCacheDeadline()
	match := model.AuthFlowMatch{Purpose: model.AuthFlowPurposeXCodeCode}
	bound, err := model.GetAuthFlow(input.Code, match)
	if err != nil && !errors.Is(err, model.ErrAuthFlowConsumed) {
		return nil, xcodeFlowError(err, ErrXCodeAuthCodeInvalid)
	}
	var sourceState xcodeAuthorizationState
	if err == nil && (common.UnmarshalJsonStr(bound.Payload, &sourceState) != nil ||
		!xcodeCodeProofMatches(bound, &sourceState, input) || sourceState.IssuedSessionSID != "") {
		return nil, ErrXCodeAuthCodeInvalid
	}
	var bundle XCodeSessionBundle
	var consumed *model.AuthFlow
	if err == nil {
		err = model.DB.Transaction(func(tx *gorm.DB) error {
			// Owner -> source SID -> code, before any repeatable-read snapshot.
			user, _, err := model.ValidateXCodeSourceSessionWithTx(tx, bound.UserId, bound.SessionId, sourceState.UserAuthVersion, 0, model.ClientPurposeDashboard)
			if err != nil {
				return xcodeSourceError(err, ErrXCodeAuthCodeInvalid)
			}
			match.UserId, match.SessionId = bound.UserId, bound.SessionId
			consumed, err = model.ConsumeAuthFlowWithActionWithTx(tx, input.Code, match, func(tx *gorm.DB, flow *model.AuthFlow) error {
				var state xcodeAuthorizationState
				if flow.Id != bound.Id || flow.Payload != bound.Payload || !flow.ExpiresAt.Equal(bound.ExpiresAt) ||
					flow.Provider != bound.Provider || flow.Intent != bound.Intent ||
					common.UnmarshalJsonStr(flow.Payload, &state) != nil || !xcodeCodeProofMatches(flow, &state, input) || state.IssuedSessionSID != "" {
					return ErrXCodeAuthCodeInvalid
				}
				if err := model.CheckUserSessionIssuanceWithTx(tx, user.Id); err != nil {
					return err
				}
				if err := model.CheckUserTokenIssuanceWithTx(tx, user.Id); err != nil {
					return err
				}
				now := time.Now().Unix()
				session, refreshSecret, err := newLoginSession(user.Id, user.AuthVersion, "xcode_pkce", ip, userAgent)
				if err != nil {
					return err
				}
				session.ClientPurpose = model.ClientPurposeXCode
				session.ClientID = input.ClientID
				if err := model.CreateUserSessionWithTx(tx, session); err != nil {
					return err
				}
				key, err := common.GenerateKey()
				if err != nil {
					return err
				}
				token := &model.Token{
					UserId: user.Id, Key: key, Status: common.TokenStatusEnabled, Name: "aiscoo managed key",
					CreatedTime: now, AccessedTime: now, ExpiredTime: session.ExpiresAt, UnlimitedQuota: true,
					ClientPurpose: model.ClientPurposeXCode, ClientSessionSID: session.SID,
				}
				if err := tx.Create(token).Error; err != nil {
					return err
				}
				access, accessExpiry, err := IssueXCodeAccessToken(session)
				if err != nil {
					return err
				}
				state.IssuedSessionSID = session.SID
				payload, err := common.Marshal(state)
				if err != nil {
					return err
				}
				if err := tx.Model(flow).Update("payload", string(payload)).Error; err != nil {
					return err
				}
				flow.Payload = string(payload)
				if _, _, err := model.ValidateXCodeSourceSessionWithTx(tx, flow.UserId, flow.SessionId, state.UserAuthVersion, 0, model.ClientPurposeDashboard); err != nil {
					return xcodeSourceError(err, ErrXCodeAuthCodeInvalid)
				}
				bundle = XCodeSessionBundle{User: user, Session: session, Token: token, AccessToken: access, AccessExpiresAt: accessExpiry,
					RefreshToken: session.SID + "." + refreshSecret, RefreshExpiresAt: session.ExpiresAt}
				return nil
			})
			return err
		})
	}
	// A competing exchange can commit before source revalidation rejects this replay.
	if errors.Is(err, model.ErrAuthFlowConsumed) || errors.Is(err, ErrXCodeAuthCodeInvalid) {
		replayed, readErr := model.GetConsumedAuthFlow(input.Code, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeXCodeCode})
		if readErr != nil {
			return nil, xcodeFlowError(readErr, ErrXCodeAuthCodeInvalid)
		}
		var state xcodeAuthorizationState
		if common.UnmarshalJsonStr(replayed.Payload, &state) != nil || !xcodeCodeProofMatches(replayed, &state, input) || state.IssuedSessionSID == "" {
			return nil, ErrXCodeAuthCodeInvalid
		}
		if err := model.RevokeXCodeSessionForConsumedCode(replayed, "xcode_code_replay"); err != nil {
			return nil, err
		}
		return nil, ErrXCodeAuthCodeInvalid
	}
	if err != nil {
		return nil, xcodeFlowError(err, ErrXCodeAuthCodeInvalid)
	}
	if err := model.PublishCreatedUserSession(bundle.Session, cacheDeadline); err != nil {
		if revokeErr := model.RevokeXCodeSessionForConsumedCode(consumed, "xcode_publication_failed"); revokeErr != nil {
			return nil, revokeErr
		}
		return nil, ErrXCodeBackendUnavailable
	}
	return &bundle, nil
}

func RefreshXCodeSession(input XCodeRefreshRequest, ip, userAgent string) (*XCodeSessionBundle, error) {
	if input.SessionID == "" || input.RefreshToken == "" {
		return nil, ErrRefreshTokenInvalid
	}
	if err := validateXCodeSessionClient(input.SessionID, input.ClientID); err != nil {
		return nil, err
	}
	native, user, err := RefreshXCodeLoginSession(input.RefreshToken, input.SessionID, ip, userAgent)
	if err != nil {
		return nil, err
	}
	session, err := model.GetUserSessionBySID(input.SessionID)
	if err != nil {
		return nil, err
	}
	token, err := model.GetXCodeSessionToken(user.Id, input.SessionID)
	if err != nil {
		if errors.Is(err, model.ErrXCodeManagedTokenInvalid) {
			return nil, ErrLoginSessionRevoked
		}
		return nil, err
	}
	identity, err := ParseXCodeAccessToken(native.AccessToken)
	if err != nil {
		return nil, err
	}
	if _, _, err := ValidateXCodeLoginSession(identity); err != nil {
		return nil, err
	}
	return &XCodeSessionBundle{User: user, Session: session, Token: token, AccessToken: native.AccessToken, AccessExpiresAt: native.AccessExpiresAt,
		RefreshToken: native.RefreshToken, RefreshExpiresAt: session.ExpiresAt}, nil
}

func LogoutXCodeSession(identity *AuthIdentity, input XCodeLogoutRequest) error {
	if input.SessionID == "" {
		return ErrLoginSessionInvalid
	}
	clientID := input.ClientID
	// 兼容旧桌面 control logout 未携带 client_id；空值绝不能认领 CLI 会话。
	if clientID == "" && identity != nil {
		clientID = XCodeClientID
	}
	if err := validateXCodeSessionClient(input.SessionID, clientID); err != nil {
		return err
	}
	var hash string
	if identity != nil {
		if identity.SessionID != input.SessionID {
			return ErrLoginSessionMismatch
		}
		if _, ok := model.ParseAccessTokenSessionID(identity.SessionID); ok {
			return ErrAuthCredentialScopeDenied
		}
		if _, _, err := ValidateXCodeLoginSession(*identity); err != nil {
			return err
		}
	} else {
		if input.ClientID != XCodeClientID && input.ClientID != XWorkCLIClientID {
			return ErrRefreshTokenInvalid
		}
		sid, secret, ok := splitRefreshToken(input.RefreshToken)
		if !ok {
			return ErrRefreshTokenInvalid
		}
		if sid != input.SessionID {
			return ErrLoginSessionMismatch
		}
		hash = hashRefreshSecret(secret)
	}
	err := model.RevokeXCodeUserSession(identity, input.SessionID, hash)
	if errors.Is(err, model.ErrUserSessionRefreshInvalid) {
		return ErrRefreshTokenInvalid
	}
	if errors.Is(err, model.ErrUserSessionInvalid) {
		return ErrAuthCredentialScopeDenied
	}
	return xcodeSourceError(err, ErrLoginSessionRevoked)
}

// 会话客户端在签发后不可变，必须在任何刷新或撤销副作用之前核验。
func validateXCodeSessionClient(sid, clientID string) error {
	if clientID != XCodeClientID && clientID != XWorkCLIClientID {
		return ErrRefreshTokenInvalid
	}
	session, err := model.GetUserSessionBySID(sid)
	if err != nil {
		return xcodeSourceError(err, ErrLoginSessionRevoked)
	}
	if session.ClientPurpose != model.ClientPurposeXCode {
		return ErrAuthCredentialScopeDenied
	}
	storedClient := session.ClientID
	if storedClient == "" {
		storedClient = XCodeClientID
	}
	if storedClient != clientID {
		return ErrAuthCredentialScopeDenied
	}
	return nil
}

func xcodeFlowError(err, invalid error) error {
	if errors.Is(err, model.ErrAuthFlowInvalid) || errors.Is(err, model.ErrAuthFlowExpired) || errors.Is(err, model.ErrAuthFlowConsumed) {
		return invalid
	}
	return err
}

func xcodeSourceError(err, invalid error) error {
	if errors.Is(err, model.ErrUserSessionInactive) || errors.Is(err, model.ErrUserAuthCachePending) || errors.Is(err, gorm.ErrRecordNotFound) {
		return invalid
	}
	return err
}

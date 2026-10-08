package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	AccessTokenTTL        = 15 * time.Minute
	SecurityProofTTL      = time.Minute
	LoginSessionTTL       = 30 * 24 * time.Hour
	RefreshReplayWindow   = 30 * time.Second
	accessTokenUse        = "access"
	securityProofTokenUse = "security_proof"
	authTokenIssuer       = "new-api"
	authTokenAudience     = "new-api-dashboard"
	xcodeAccessTokenUse   = "xcode_access"
	xcodeWebTokenUse      = "xcode_web"
	xcodeTokenAudience    = "new-api-xcode"
	xcodeWebTokenAudience = "new-api-xcode-web"
)

var (
	ErrAuthTokenInvalid          = errors.New("authentication token is invalid")
	ErrAuthTokenExpired          = errors.New("authentication token has expired")
	ErrProofScope                = errors.New("security proof scope mismatch")
	ErrProofMethod               = errors.New("security proof method mismatch")
	ErrProofContext              = errors.New("security proof context mismatch")
	ErrProofConsumed             = errors.New("security proof has already been consumed")
	ErrAuthCredentialScopeDenied = errors.New("authentication credential purpose is not permitted")
)

// AuthIdentity is the server-validated identity attached to dashboard requests.
// Role, status and group are deliberately loaded from the user cache instead of JWT claims.
type AuthIdentity = model.AuthSessionIdentity

type authClaims struct {
	TokenUse        string   `json:"token_use"`
	SessionID       string   `json:"sid"`
	UserAuthVersion int64    `json:"uv"`
	SessionVersion  int64    `json:"sv"`
	Method          string   `json:"method,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`
	ContextHash     string   `json:"context_hash,omitempty"`
	jwt.RegisteredClaims
}

func authSigningKey(purpose string) []byte {
	mac := hmac.New(sha256.New, []byte(common.SessionSecret))
	_, _ = mac.Write([]byte("new-api/auth/" + purpose + "/v1"))
	return mac.Sum(nil)
}

func IssueAccessToken(identity AuthIdentity) (string, int64, error) {
	return issueAccessToken(identity, accessTokenUse, time.Now().Add(AccessTokenTTL).Unix())
}

func IssueXCodeAccessToken(session *model.UserSession) (string, int64, error) {
	return issueXCodeToken(session, xcodeAccessTokenUse)
}

func IssueXCodeWebToken(session *model.UserSession) (string, int64, error) {
	return issueXCodeToken(session, xcodeWebTokenUse)
}

func issueXCodeToken(session *model.UserSession, use string) (string, int64, error) {
	if session == nil {
		return "", 0, ErrLoginSessionInvalid
	}
	if session.ClientPurpose != model.ClientPurposeXCode {
		return "", 0, ErrAuthCredentialScopeDenied
	}
	if session.Status != model.UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() {
		return "", 0, ErrLoginSessionRevoked
	}
	identity := AuthIdentity{UserID: session.UserID, SessionID: session.SID, UserAuthVersion: session.UserAuthVersion, SessionVersion: session.Version}
	return issueAccessToken(identity, use, min(time.Now().Add(AccessTokenTTL).Unix(), session.ExpiresAt))
}

func issueAccessToken(identity AuthIdentity, use string, expiresAt int64) (string, int64, error) {
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 || identity.SessionVersion <= 0 {
		return "", 0, ErrAuthTokenInvalid
	}
	now := time.Now()
	if expiresAt <= now.Unix() {
		return "", 0, ErrLoginSessionRevoked
	}
	claims := authClaims{
		TokenUse:        use,
		SessionID:       identity.SessionID,
		UserAuthVersion: identity.UserAuthVersion,
		SessionVersion:  identity.SessionVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    authTokenIssuer,
			Subject:   strconv.Itoa(identity.UserID),
			Audience:  jwt.ClaimStrings{authAudience(use)},
			ExpiresAt: jwt.NewNumericDate(time.Unix(expiresAt, 0)),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.NewString(),
		},
	}
	if use == xcodeAccessTokenUse {
		claims.Scopes = []string{"profile:read", "wallet:read", "usage:read"}
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(authSigningKey(use))
	return signed, expiresAt, err
}

func ParseAccessToken(raw string) (AuthIdentity, error) {
	return parseAccessToken(raw, accessTokenUse)
}

func ParseXCodeAccessToken(raw string) (AuthIdentity, error) {
	return parseAccessToken(raw, xcodeAccessTokenUse)
}

func ParseXCodeWebToken(raw string) (AuthIdentity, error) {
	return parseAccessToken(raw, xcodeWebTokenUse)
}

func parseAccessToken(raw, expectedUse string) (AuthIdentity, error) {
	unverified := &authClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(strings.TrimSpace(raw), unverified); err != nil {
		return AuthIdentity{}, ErrAuthTokenInvalid
	}
	use := unverified.TokenUse
	return parseAccessTokenForUse(raw, expectedUse, use)
}

func parseAccessTokenForUse(raw, expectedUse, use string) (AuthIdentity, error) {
	if use != accessTokenUse && use != xcodeAccessTokenUse && use != xcodeWebTokenUse {
		return AuthIdentity{}, ErrAuthTokenInvalid
	}
	claims, err := parseAuthClaims(raw, use, authSigningKey(use))
	if err != nil {
		return AuthIdentity{}, err
	}
	if use == xcodeAccessTokenUse && !slices.Equal(claims.Scopes, []string{"profile:read", "wallet:read", "usage:read"}) {
		return AuthIdentity{}, ErrAuthTokenInvalid
	}
	if use == xcodeWebTokenUse && len(claims.Scopes) != 0 {
		return AuthIdentity{}, ErrAuthTokenInvalid
	}
	userID, err := strconv.Atoi(claims.Subject)
	if err != nil || userID <= 0 || claims.SessionID == "" || claims.UserAuthVersion <= 0 || claims.SessionVersion <= 0 {
		return AuthIdentity{}, ErrAuthTokenInvalid
	}
	if use != expectedUse {
		return AuthIdentity{}, ErrAuthCredentialScopeDenied
	}
	return AuthIdentity{
		UserID:          userID,
		SessionID:       claims.SessionID,
		UserAuthVersion: claims.UserAuthVersion,
		SessionVersion:  claims.SessionVersion,
	}, nil
}

// ParseDashboardAccessToken recognizes internal dashboard and XCode JWT shapes
// before opaque lookup, including invalid signatures, expiry and purpose.
func ParseDashboardAccessToken(raw string) (identity AuthIdentity, internal bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AuthIdentity{}, false, nil
	}
	claims := jwt.MapClaims{}
	parsed, _, parseErr := jwt.NewParser().ParseUnverified(raw, claims)
	if parseErr != nil || parsed == nil {
		return AuthIdentity{}, false, nil
	}
	issuer, _ := claims.GetIssuer()
	audience, _ := claims.GetAudience()
	use, _ := claims["token_use"].(string)
	audienceMatches := slices.Contains(audience, authTokenAudience)
	knownTokenUse := use == accessTokenUse || use == securityProofTokenUse
	xcodeShape := slices.Contains(audience, xcodeTokenAudience) || slices.Contains(audience, xcodeWebTokenAudience) ||
		use == xcodeAccessTokenUse || use == xcodeWebTokenUse
	if !xcodeShape && (issuer != authTokenIssuer || !audienceMatches || !knownTokenUse) {
		return AuthIdentity{}, false, nil
	}
	identity, err = parseAccessTokenForUse(raw, accessTokenUse, use)
	return identity, true, err
}

func IssueSecurityProof(identity AuthIdentity, method string, binding VerificationBinding) (string, int64, error) {
	method = strings.TrimSpace(method)
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 || identity.SessionVersion <= 0 || method == "" || binding.Scope == "" || binding.ContextHash == "" {
		return "", 0, ErrAuthTokenInvalid
	}
	now := time.Now()
	expiresAt := now.Add(SecurityProofTTL).Truncate(time.Second)
	proofID, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeSecurityProof, UserId: identity.UserID,
		SessionId: identity.SessionID, ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", 0, err
	}
	claims := authClaims{
		TokenUse:        securityProofTokenUse,
		SessionID:       identity.SessionID,
		UserAuthVersion: identity.UserAuthVersion,
		SessionVersion:  identity.SessionVersion,
		Method:          method,
		Scopes:          []string{binding.Scope},
		ContextHash:     binding.ContextHash,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    authTokenIssuer,
			Subject:   strconv.Itoa(identity.UserID),
			Audience:  jwt.ClaimStrings{authTokenAudience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        proofID,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(authSigningKey(securityProofTokenUse))
	return signed, expiresAt.Unix(), err
}

func verifySecurityProof(raw string, identity AuthIdentity, binding VerificationBinding) (*authClaims, error) {
	claims, err := parseAuthClaims(raw, securityProofTokenUse, authSigningKey(securityProofTokenUse))
	if err != nil {
		return nil, err
	}
	userID, err := strconv.Atoi(claims.Subject)
	if err != nil || userID != identity.UserID || claims.SessionID != identity.SessionID || claims.UserAuthVersion != identity.UserAuthVersion || claims.SessionVersion != identity.SessionVersion {
		return nil, ErrAuthTokenInvalid
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != binding.Scope {
		return nil, ErrProofScope
	}
	if claims.ContextHash == "" {
		return nil, ErrAuthTokenInvalid
	}
	if !hmac.Equal([]byte(claims.ContextHash), []byte(binding.ContextHash)) {
		return nil, ErrProofContext
	}
	return claims, nil
}

func authAudience(use string) string {
	switch use {
	case xcodeAccessTokenUse:
		return xcodeTokenAudience
	case xcodeWebTokenUse:
		return xcodeWebTokenAudience
	default:
		return authTokenAudience
	}
}

func parseAuthClaims(raw, expectedUse string, key []byte) (*authClaims, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrAuthTokenInvalid
	}
	claims := &authClaims{}
	leeway := 5 * time.Second
	if expectedUse == xcodeAccessTokenUse || expectedUse == xcodeWebTokenUse {
		leeway = 0
	}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("%w: unexpected signing method", ErrAuthTokenInvalid)
		}
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(authTokenIssuer), jwt.WithAudience(authAudience(expectedUse)), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(leeway))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrAuthTokenExpired
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthTokenInvalid, err)
	}
	if !parsed.Valid || claims.TokenUse != expectedUse || claims.ID == "" || claims.IssuedAt == nil || claims.NotBefore == nil {
		return nil, ErrAuthTokenInvalid
	}
	if (expectedUse == xcodeAccessTokenUse || expectedUse == xcodeWebTokenUse) &&
		(len(claims.Audience) != 1 || claims.Audience[0] != authAudience(expectedUse)) {
		return nil, ErrAuthTokenInvalid
	}
	return claims, nil
}

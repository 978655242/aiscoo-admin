package service

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useTestSessionSecret(t *testing.T) {
	t.Helper()
	previous := common.SessionSecret
	common.SessionSecret = "test-session-secret-with-sufficient-entropy"
	t.Cleanup(func() { common.SessionSecret = previous })
}

func TestAccessTokenRoundTripAndPurposeIsolation(t *testing.T) {
	setupAuthSessionTestDB(t)
	useTestSessionSecret(t)
	identity := AuthIdentity{UserID: 42, SessionID: "session-1", UserAuthVersion: 3, SessionVersion: 2}

	token, expiresAt, err := IssueAccessToken(identity)
	require.NoError(t, err)
	assert.Positive(t, expiresAt)

	parsed, err := ParseAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, identity, parsed)

	binding, err := BindVerificationOperation(VerificationOperation{Scope: "channel.key.read", Context: []byte(`{"channel_id":123}`)})
	require.NoError(t, err)
	proof, _, err := IssueSecurityProof(identity, "2fa", binding)
	require.NoError(t, err)
	_, err = ParseAccessToken(proof)
	assert.ErrorIs(t, err, ErrAuthTokenInvalid)
}

func TestAccessTokenRejectsTampering(t *testing.T) {
	useTestSessionSecret(t)
	identity := AuthIdentity{UserID: 42, SessionID: "session-1", UserAuthVersion: 1, SessionVersion: 1}
	token, _, err := IssueAccessToken(identity)
	require.NoError(t, err)

	tamperAt := len(token) - 2
	replacement := "x"
	if token[tamperAt] == 'x' {
		replacement = "y"
	}
	tampered := token[:tamperAt] + replacement + token[tamperAt+1:]
	_, err = ParseAccessToken(tampered)
	assert.ErrorIs(t, err, ErrAuthTokenInvalid)

	_, internal, err := ParseDashboardAccessToken(tampered)
	assert.True(t, internal)
	assert.ErrorIs(t, err, ErrAuthTokenInvalid)
}

func TestDashboardAccessTokenClassification(t *testing.T) {
	setupAuthSessionTestDB(t)
	useTestSessionSecret(t)

	identity, internal, err := ParseDashboardAccessToken("opaque.key.with-dots")
	require.NoError(t, err)
	assert.False(t, internal)
	assert.Empty(t, identity)

	external := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "external-issuer",
		"aud": authTokenAudience,
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	externalRaw, err := external.SignedString([]byte("external-secret"))
	require.NoError(t, err)
	_, internal, err = ParseDashboardAccessToken(externalRaw)
	require.NoError(t, err)
	assert.False(t, internal)

	unknownUse := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":       authTokenIssuer,
		"aud":       authTokenAudience,
		"token_use": "third_party",
		"exp":       time.Now().Add(time.Minute).Unix(),
	})
	unknownUseRaw, err := unknownUse.SignedString([]byte("external-secret"))
	require.NoError(t, err)
	_, internal, err = ParseDashboardAccessToken(unknownUseRaw)
	require.NoError(t, err)
	assert.False(t, internal)

	binding, err := BindVerificationOperation(VerificationOperation{Scope: "channel.key.read", Context: []byte(`{"channel_id":123}`)})
	require.NoError(t, err)
	proof, _, err := IssueSecurityProof(AuthIdentity{
		UserID: 42, SessionID: "session-1", UserAuthVersion: 1, SessionVersion: 1,
	}, "2fa", binding)
	require.NoError(t, err)
	_, internal, err = ParseDashboardAccessToken(proof)
	assert.True(t, internal)
	assert.ErrorIs(t, err, ErrAuthTokenInvalid)

	expiredClaims := authClaims{
		TokenUse:        accessTokenUse,
		SessionID:       "expired-session",
		UserAuthVersion: 1,
		SessionVersion:  1,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    authTokenIssuer,
			Subject:   "42",
			Audience:  jwt.ClaimStrings{authTokenAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-2 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Minute)),
			ID:        "expired-token",
		},
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, expiredClaims).SignedString(authSigningKey(accessTokenUse))
	require.NoError(t, err)
	_, internal, err = ParseDashboardAccessToken(expired)
	assert.True(t, internal)
	assert.ErrorIs(t, err, ErrAuthTokenExpired)
}

func TestXCodeTokenPurposesScopesAndExpiry(t *testing.T) {
	useTestSessionSecret(t)
	session := &model.UserSession{
		UserID: 42, SID: "xcode-session", UserAuthVersion: 3, Version: 2,
		ClientPurpose: model.ClientPurposeXCode, Status: model.UserSessionStatusActive,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	control, controlExpiry, err := IssueXCodeAccessToken(session)
	require.NoError(t, err)
	web, webExpiry, err := IssueXCodeWebToken(session)
	require.NoError(t, err)
	assert.Equal(t, session.ExpiresAt, controlExpiry)
	assert.Equal(t, session.ExpiresAt, webExpiry)
	identity, err := ParseXCodeAccessToken(control)
	require.NoError(t, err)
	assert.Equal(t, session.SID, identity.SessionID)
	_, err = ParseXCodeWebToken(web)
	require.NoError(t, err)
	for _, raw := range []string{control, web} {
		_, internal, err := ParseDashboardAccessToken(raw)
		assert.True(t, internal)
		assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
	}
	_, err = ParseXCodeAccessToken(web)
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
	_, err = ParseXCodeWebToken(control)
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
	native, _, err := IssueAccessToken(identity)
	require.NoError(t, err)
	_, err = ParseXCodeAccessToken(native)
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)

	claims := &authClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(control, claims)
	require.NoError(t, err)
	assert.Equal(t, jwt.ClaimStrings{"new-api-xcode"}, claims.Audience)
	assert.Equal(t, "xcode_access", claims.TokenUse)
	assert.Equal(t, []string{"profile:read", "wallet:read", "usage:read"}, claims.Scopes)
	wrongKey, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(authSigningKey("access"))
	require.NoError(t, err)
	_, err = ParseXCodeAccessToken(wrongKey)
	assert.ErrorIs(t, err, ErrAuthTokenInvalid)
	claims.Scopes = []string{"profile:read", "wallet:read", "usage:read", "admin:write"}
	wrongScopes, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(authSigningKey("xcode_access"))
	require.NoError(t, err)
	_, err = ParseXCodeAccessToken(wrongScopes)
	assert.ErrorIs(t, err, ErrAuthTokenInvalid)
	claims.Scopes = []string{"profile:read", "wallet:read", "usage:read"}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(authSigningKey("xcode_access"))
	require.NoError(t, err)
	_, internal, err := ParseDashboardAccessToken(expired)
	assert.True(t, internal)
	assert.ErrorIs(t, err, ErrAuthTokenExpired)
	session.ExpiresAt = time.Now().Unix()
	_, _, err = IssueXCodeAccessToken(session)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
	session.ExpiresAt = time.Now().Add(time.Hour).Unix()
	session.ClientPurpose = model.ClientPurposeDashboard
	_, _, err = IssueXCodeAccessToken(session)
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
}

func TestSecurityProofBindsIdentityAndOperation(t *testing.T) {
	setupAuthSessionTestDB(t)
	useTestSessionSecret(t)
	identity := AuthIdentity{UserID: 42, SessionID: "session-1", UserAuthVersion: 3, SessionVersion: 2}
	operation := VerificationOperation{Scope: "channel.key.read", Context: []byte(`{"channel_id":123}`)}
	binding, err := BindVerificationOperation(operation)
	require.NoError(t, err)
	proof, _, err := IssueSecurityProof(identity, "2fa", binding)
	require.NoError(t, err)

	claims, err := verifySecurityProof(proof, identity, binding)
	require.NoError(t, err)
	assert.Equal(t, "2fa", claims.Method)

	wrongScope, err := BindVerificationOperation(VerificationOperation{Scope: "passkey.delete"})
	require.NoError(t, err)
	_, err = verifySecurityProof(proof, identity, wrongScope)
	assert.ErrorIs(t, err, ErrProofScope)

	otherChannel, err := BindVerificationOperation(VerificationOperation{Scope: "channel.key.read", Context: []byte(`{"channel_id":456}`)})
	require.NoError(t, err)
	_, err = verifySecurityProof(proof, identity, otherChannel)
	assert.ErrorIs(t, err, ErrProofContext)

	otherSession := identity
	otherSession.SessionID = "session-2"
	_, err = verifySecurityProof(proof, otherSession, binding)
	assert.True(t, errors.Is(err, ErrAuthTokenInvalid))

	claims, err = parseAuthClaims(proof, securityProofTokenUse, authSigningKey(securityProofTokenUse))
	require.NoError(t, err)
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(authSigningKey(securityProofTokenUse))
	require.NoError(t, err)
	_, err = ConsumeOperationProof(expired, identity, operation)
	assert.ErrorIs(t, err, ErrAuthTokenExpired)
}

func TestSecurityProofBindsAccessTokenSession(t *testing.T) {
	setupAuthSessionTestDB(t)
	useTestSessionSecret(t)
	identity := AuthIdentity{UserID: 42, SessionID: model.AccessTokenSessionID(7), UserAuthVersion: 3, SessionVersion: model.AccessTokenSessionVersion}
	binding, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeTwoFADisable})
	require.NoError(t, err)
	proof, _, err := IssueSecurityProof(identity, "password", binding)
	require.NoError(t, err)

	claims, err := verifySecurityProof(proof, identity, binding)
	require.NoError(t, err)
	assert.Equal(t, identity.SessionID, claims.SessionID)

	for _, sessionID := range []string{model.AccessTokenSessionID(8), "session-1"} {
		other := identity
		other.SessionID = sessionID
		_, err = verifySecurityProof(proof, other, binding)
		assert.ErrorIs(t, err, ErrAuthTokenInvalid, sessionID)
	}
}

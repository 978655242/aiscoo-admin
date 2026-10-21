package service

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAuthSessionTestDB(t *testing.T) *model.User {
	t.Helper()
	previousDB, previousRedis := model.DB, common.RedisEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	previousRevokedRetention := common.UserSessionRevokedRetentionDays
	previousAlertThreshold := common.UserSessionHourlyAlertThreshold
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.UserAccessToken{}))
	model.DB = db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = common.DefaultUserSessionActiveLimit
	common.UserSessionIssuanceLimit = common.DefaultUserSessionIssuanceLimit
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	common.UserSessionRevokedRetentionDays = common.DefaultUserSessionRevokedRetentionDays
	common.UserSessionHourlyAlertThreshold = common.DefaultUserSessionHourlyAlertThreshold
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
		common.UserSessionRevokedRetentionDays = previousRevokedRetention
		common.UserSessionHourlyAlertThreshold = previousAlertThreshold
		_ = sqlDB.Close()
	})
	user := &model.User{
		Username:    "session-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func useIndependentAuthSessionRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	previousSyncFrequency := common.SyncFrequency
	serverA := miniredis.RunT(t)
	serverB := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: serverA.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: serverB.Addr()})
	common.RedisEnabled = true
	common.SyncFrequency = 2
	common.RDB = clientA
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
		common.SyncFrequency = previousSyncFrequency
	})
	return serverA, clientA, serverB, clientB
}

func cachedLoginSessionKey(t *testing.T, server *miniredis.Miniredis) string {
	t.Helper()
	for _, key := range server.Keys() {
		if strings.HasPrefix(key, "auth:session:") {
			return key
		}
	}
	require.FailNow(t, "login session was not cached")
	return ""
}

func TestCreateLoginSessionEnforcesActiveLimitAcrossAuthVersions(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	common.UserSessionActiveLimit = 50
	common.UserSessionIssuanceLimit = 100
	now := time.Now().Unix()
	rows := make([]model.UserSession, 0, 49)
	for i := range 49 {
		authVersion := user.AuthVersion
		if i == 0 {
			authVersion++
		}
		rows = append(rows, model.UserSession{
			SID:             fmt.Sprintf("active-limit-%02d", i),
			UserID:          user.Id,
			Version:         1,
			UserAuthVersion: authVersion,
			Status:          model.UserSessionStatusActive,
			RefreshHash:     fmt.Sprintf("hash-%02d", i),
			LoginMethod:     "password",
			CreatedAt:       now - int64(i),
			LastActiveAt:    now - int64(i),
			ExpiresAt:       now + 3600,
		})
	}
	require.NoError(t, model.DB.Create(&rows).Error)

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err, "49 active sessions must allow creation of the 50th")

	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, model.ErrUserSessionLimit)
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Equal(t, int64(50), count)
}

func TestCreateLoginSessionEnforcesIssuanceLimitAcrossAllStatuses(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	common.UserSessionActiveLimit = 10
	common.UserSessionIssuanceLimit = 3
	common.UserSessionIssuanceWindowSeconds = 60
	now := time.Now().Unix()
	rows := []model.UserSession{
		{
			SID: "issuance-limit-revoked", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion + 1,
			Status: model.UserSessionStatusRevoked, RefreshHash: "hash-revoked", LoginMethod: "password",
			CreatedAt: now - 2, LastActiveAt: now - 2, ExpiresAt: now + 3600, RevokedAt: now - 1,
		},
		{
			SID: "issuance-limit-expired", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
			Status: model.UserSessionStatusActive, RefreshHash: "hash-expired", LoginMethod: "password",
			CreatedAt: now - 1, LastActiveAt: now - 1, ExpiresAt: now - 1,
		},
		{
			SID: "issuance-outside-effective-window", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
			Status: model.UserSessionStatusRevoked, RefreshHash: "hash-outside", LoginMethod: "password",
			CreatedAt: now - 61, LastActiveAt: now - 61, ExpiresAt: now + 3600, RevokedAt: now - 60,
		},
	}
	require.NoError(t, model.DB.Create(&rows).Error)

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err, "rows outside the effective issuance window must not consume the limit")

	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, model.ErrUserSessionIssuanceLimit)
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Equal(t, int64(4), count)
}

func TestPasswordResetDoesNotClearSessionIssuanceHistory(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	common.UserSessionActiveLimit = 50
	common.UserSessionIssuanceLimit = 1
	email := "session-reset@example.com"
	require.NoError(t, model.DB.Model(user).Update("email", email).Error)

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	require.NoError(t, model.ResetUserPasswordByEmail(email, "new-password"))

	_, err = CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, model.ErrUserSessionIssuanceLimit)
}

func TestCreateLoginSessionFailsClosedWhenLimitCountFails(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	forcedErr := errors.New("forced session count failure")
	callbackName := "test:fail_user_session_limit_count"
	callbackRegistered := true
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() {
		if callbackRegistered {
			_ = model.DB.Callback().Query().Remove(callbackName)
		}
	})

	_, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, forcedErr)
	require.NoError(t, model.DB.Callback().Query().Remove(callbackName))
	callbackRegistered = false
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestCreateLoginSessionRejectsProofInvalidatedBeforeOwnerLock(t *testing.T) {
	for _, expectedVersion := range []int64{0, 1} {
		t.Run(fmt.Sprintf("expected_version_%d", expectedVersion), func(t *testing.T) {
			useTestSessionSecret(t)
			user := setupAuthSessionTestDB(t)
			changed := false
			const callback = "test:invalidate_login_proof_after_read"
			require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if changed || tx.Statement.Table != "users" {
					return
				}
				changed = true
				tx.AddError(model.DB.Model(&model.User{}).Where("id = ?", user.Id).UpdateColumn("auth_version", 2).Error)
			}))
			t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callback) })

			bundle, err := createLoginSession(user.Id, expectedVersion, "password", "", "")
			require.True(t, changed)
			assert.ErrorIs(t, err, ErrLoginSessionRevoked)
			assert.Nil(t, bundle)
			var count int64
			require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
			assert.Zero(t, count, "a pre-lock proof must not mint a session at the newer auth version")
		})
	}
}

func TestXCodeSourceGuardChecksDenyAuthorityBeforeOwnerLock(t *testing.T) {
	for _, deny := range []string{"session", "auth_version", "cache_unavailable"} {
		t.Run(deny, func(t *testing.T) {
			useTestSessionSecret(t)
			user := setupAuthSessionTestDB(t)
			server, client, _, _ := useIndependentAuthSessionRedis(t)
			bundle, err := CreateLoginSession(user.Id, "password", "", "")
			require.NoError(t, err)
			switch deny {
			case "session":
				server.HSet(cachedLoginSessionKey(t, server), "Status", model.UserSessionStatusRevoking)
			case "auth_version":
				require.NoError(t, model.SetUserAuthVersionFence(user.Id, 2))
			case "cache_unavailable":
				require.NoError(t, client.Close())
			}
			ownerQueried := false
			const callback = "test:source_guard_owner_read"
			require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "users" {
					ownerQueried = true
				}
			}))
			t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callback) })

			err = model.DB.Transaction(func(tx *gorm.DB) error {
				_, _, err := model.ValidateXCodeSourceSessionWithTx(tx, user.Id, bundle.Session.SID, 1, 0, model.ClientPurposeDashboard)
				return err
			})
			require.Error(t, err)
			if deny == "cache_unavailable" {
				assert.NotErrorIs(t, err, model.ErrUserSessionInactive, "unknown cache errors remain unavailable")
			} else {
				assert.ErrorIs(t, err, model.ErrUserSessionInactive)
			}
			assert.False(t, ownerQueried, "published denial must be honored before waiting for the owner lock")
		})
	}
}

func TestXCodeSourceGuardRechecksDenyAuthorityAfterSIDLock(t *testing.T) {
	for _, deny := range []string{"session", "auth_version"} {
		t.Run(deny, func(t *testing.T) {
			useTestSessionSecret(t)
			user := setupAuthSessionTestDB(t)
			server, _, _, _ := useIndependentAuthSessionRedis(t)
			bundle, err := CreateLoginSession(user.Id, "password", "", "")
			require.NoError(t, err)
			cacheKey := cachedLoginSessionKey(t, server)
			const callback = "test:publish_source_deny_after_sid_read"
			require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "user_sessions" {
					return
				}
				if deny == "session" {
					server.HSet(cacheKey, "Status", model.UserSessionStatusRevoking)
				} else {
					tx.AddError(model.SetUserAuthVersionFence(user.Id, 2))
				}
			}))
			t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callback) })

			err = model.DB.Transaction(func(tx *gorm.DB) error {
				_, _, err := model.ValidateXCodeSourceSessionWithTx(tx, user.Id, bundle.Session.SID, 1, 0, model.ClientPurposeDashboard)
				return err
			})
			assert.ErrorIs(t, err, model.ErrUserSessionInactive, "a denial published during authority locking must invalidate the source")
		})
	}
}

func TestCleanupAuthArtifactsAlertsBeforeDeletingHourlyIssuance(t *testing.T) {
	setupAuthSessionTestDB(t)
	common.UserSessionHourlyAlertThreshold = 2
	common.UserSessionIssuanceWindowSeconds = 1
	now := time.Now()
	boundaryRows := make([]model.UserSession, 0, 2)
	for i := range 2 {
		boundaryRows = append(boundaryRows, model.UserSession{
			SID: "hourly-boundary-" + string(rune('a'+i)), UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
			CreatedAt: now.Add(-2 * time.Second).Unix(), LastActiveAt: now.Add(-time.Hour).Unix(), ExpiresAt: now.Add(-time.Minute).Unix(),
		})
	}
	require.NoError(t, model.DB.Create(&boundaryRows).Error)

	var logBuffer bytes.Buffer
	common.LogWriterMu.Lock()
	previousErrorWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousErrorWriter
		common.LogWriterMu.Unlock()
	})

	cleanupAuthArtifacts()
	assert.Empty(t, logBuffer.String(), "the hourly alert uses a strict greater-than threshold")
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Zero(t, count)

	exceededRows := make([]model.UserSession, 0, 3)
	for i := range 3 {
		exceededRows = append(exceededRows, model.UserSession{
			SID: "hourly-exceeded-" + string(rune('a'+i)), UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
			CreatedAt: now.Add(-2 * time.Second).Unix(), LastActiveAt: now.Add(-time.Hour).Unix(), ExpiresAt: now.Add(-time.Minute).Unix(),
		})
	}
	require.NoError(t, model.DB.Create(&exceededRows).Error)
	logBuffer.Reset()
	cleanupAuthArtifacts()
	assert.Contains(t, logBuffer.String(), "hourly user session issuance exceeded alert threshold")
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Zero(t, count, "alerting must happen before expired rows are deleted")
}

func TestCleanupAuthArtifactsRemovesOnlyExpiredRecords(t *testing.T) {
	setupAuthSessionTestDB(t)
	now := time.Now()
	oldExpiry := now.Add(-25 * time.Hour)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "expired-session", UserID: 1, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
		CreatedAt: oldExpiry.Unix(), LastActiveAt: oldExpiry.Unix(), ExpiresAt: oldExpiry.Unix(),
	}).Error)
	require.NoError(t, model.DB.Create(&model.AuthFlow{
		TokenHash: "expired-flow", Purpose: model.AuthFlowPurposeTwoFALogin,
		ExpiresAt: oldExpiry,
	}).Error)
	require.NoError(t, model.DB.Create(&model.AuthFlow{
		TokenHash: "recent-flow", Purpose: model.AuthFlowPurposeTwoFALogin,
		ExpiresAt: now.Add(time.Minute),
	}).Error)

	retention := time.Duration(common.UserSessionRevokedRetentionDays) * 24 * time.Hour
	for _, token := range []model.UserAccessToken{
		{Name: "past retention", TokenHash: "past-retention", ExpiresAt: now.Add(-retention - time.Hour).Unix()},
		{Name: "within retention", TokenHash: "within-retention", ExpiresAt: now.Add(-retention + time.Hour).Unix()},
		{Name: "never expires", TokenHash: "never-expires"},
	} {
		token.UserId = 1
		require.NoError(t, model.DB.Create(&token).Error)
	}

	cleanupAuthArtifacts()

	var sessionCount int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessionCount).Error)
	assert.Zero(t, sessionCount)
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Find(&flows).Error)
	require.Len(t, flows, 1)
	assert.Equal(t, "recent-flow", flows[0].TokenHash)
	var kept []string
	require.NoError(t, model.DB.Model(&model.UserAccessToken{}).Order("id").Pluck("token_hash", &kept).Error)
	assert.Equal(t, []string{"within-retention", "never-expires"}, kept)
}

func TestCleanupAuthArtifactsContinuesWithRevokedCleanupAfterExpiredBatchFailure(t *testing.T) {
	setupAuthSessionTestDB(t)
	now := time.Now()
	oldCreatedAt := now.Add(-8 * 24 * time.Hour).Unix()
	require.NoError(t, model.DB.Create(&[]model.UserSession{
		{
			SID: "failed-expired-cleanup", UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "hash-expired", LoginMethod: "password",
			CreatedAt: oldCreatedAt, LastActiveAt: oldCreatedAt, ExpiresAt: now.Add(-time.Minute).Unix(),
		},
		{
			SID: "independent-revoked-cleanup", UserID: 1, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusRevoked, RefreshHash: "hash-revoked", LoginMethod: "password",
			CreatedAt: oldCreatedAt, LastActiveAt: oldCreatedAt, ExpiresAt: now.Add(time.Hour).Unix(),
			RevokedAt: now.Add(-8 * 24 * time.Hour).Unix(),
		},
	}).Error)

	forcedErr := errors.New("forced expired cleanup failure")
	callbackName := "test:fail_first_user_session_cleanup_batch"
	failedFirstDelete := false
	require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" && !failedFirstDelete {
			failedFirstDelete = true
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Delete().Remove(callbackName) })

	cleanupAuthArtifacts()

	var expired model.UserSession
	require.NoError(t, model.DB.First(&expired, "sid = ?", "failed-expired-cleanup").Error)
	var revoked model.UserSession
	assert.ErrorIs(t, model.DB.First(&revoked, "sid = ?", "independent-revoked-cleanup").Error, gorm.ErrRecordNotFound)
}

func TestLoginSessionCreateRefreshAndRevoke(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)

	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	assert.NotEmpty(t, bundle.RefreshToken)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	_, cachedUser, err := ValidateLoginSession(identity)
	require.NoError(t, err)
	assert.Equal(t, user.Id, cachedUser.Id)
	require.NoError(t, RevokeByRefreshToken(bundle.Session.SID+".wrong-refresh-secret", "", "logout"))
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err, "a caller that only knows sid must not be able to revoke the session")

	refreshed, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "127.0.0.2", "test-agent-2")
	require.NoError(t, err)
	assert.NotEqual(t, bundle.RefreshToken, refreshed.RefreshToken)
	recovered, _, err := RefreshLoginSession(bundle.RefreshToken, bundle.Session.SID, "127.0.0.2", "test-agent-2")
	require.NoError(t, err)
	assert.Equal(t, refreshed.RefreshToken, recovered.RefreshToken, "a concurrent refresh must recover the winner's rotated token")

	_, _, err = RefreshLoginSession(refreshed.RefreshToken, "different-session", "127.0.0.2", "test-agent-2")
	assert.ErrorIs(t, err, ErrLoginSessionMismatch)

	require.NoError(t, RevokeByRefreshToken(refreshed.RefreshToken, refreshed.Session.SID, "logout"))
	_, _, err = ValidateLoginSession(identity)
	assert.True(t, errors.Is(err, ErrLoginSessionRevoked))
}

func TestXCodeCLIRegisteredRedirects(t *testing.T) {
	challenge := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, redirect := range []string{"http://127.0.0.1:49152/oauth/callback", "http://127.0.0.1:65535/oauth/callback"} {
		input := XCodeAuthorizeRequest{ClientID: "xwork-cli", ResponseType: "code", RedirectURI: redirect, State: strings.Repeat("s", 32), CodeChallenge: challenge, CodeChallengeMethod: "S256"}
		assert.True(t, validateXCodeAuthorizationRequest(input), redirect)
	}
	for _, redirect := range []string{"http://localhost:49152/oauth/callback", "http://127.1:49152/oauth/callback", "http://2130706433:49152/oauth/callback", "http://[::1]:49152/oauth/callback", "http://127.0.0.1/oauth/callback", "http://127.0.0.1:0/oauth/callback", "http://127.0.0.1:65536/oauth/callback", "http://127.0.0.1:049152/oauth/callback", "http://user@127.0.0.1:49152/oauth/callback", "http://127.0.0.1:49152/oauth/callback?x=1", "http://127.0.0.1:49152/oauth/callback#x", "http://127.0.0.1:49152/oauth/%63allback", "http://127.0.0.1:49152/oauth/callback/", XCodeRedirectURI} {
		input := XCodeAuthorizeRequest{ClientID: "xwork-cli", ResponseType: "code", RedirectURI: redirect, State: strings.Repeat("s", 32), CodeChallenge: challenge, CodeChallengeMethod: "S256"}
		assert.False(t, validateXCodeAuthorizationRequest(input), redirect)
	}
}

func TestXCodeSessionRefreshPurposeAndManagedKeyFence(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	session, secret, err := newLoginSession(user.Id, user.AuthVersion, "xcode", "127.0.0.1", "host")
	require.NoError(t, err)
	session.ClientPurpose = model.ClientPurposeXCode
	session.ExpiresAt = time.Now().Add(time.Minute).Unix()
	require.NoError(t, model.CreateUserSession(session))
	key := &model.Token{
		UserId: user.Id, Key: "managed-refresh-key", Name: "xcode", Status: common.TokenStatusEnabled,
		UnlimitedQuota: true, ExpiredTime: session.ExpiresAt, ClientPurpose: model.ClientPurposeXCode,
		ClientSessionSID: session.SID,
	}
	require.NoError(t, model.DB.Create(key).Error)
	raw := session.SID + "." + secret
	identity := AuthIdentity{UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1}
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
	_, _, err = ValidateXCodeLoginSession(identity)
	require.NoError(t, err)
	_, _, err = RefreshLoginSession(raw, session.SID, "", "")
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
	stored, err := model.GetUserSessionBySID(session.SID)
	require.NoError(t, err)
	assert.Equal(t, session.RefreshHash, stored.RefreshHash, "wrong-purpose refresh must not rotate")
	_, err = RefreshXCodeSession(XCodeRefreshRequest{ClientID: "xwork-cli", SessionID: session.SID, RefreshToken: raw}, "", "")
	assert.Error(t, err, "CLI client must not refresh a legacy desktop SID")
	stored, err = model.GetUserSessionBySID(session.SID)
	require.NoError(t, err)
	assert.Equal(t, session.RefreshHash, stored.RefreshHash)
	assert.Error(t, LogoutXCodeSession(&identity, XCodeLogoutRequest{ClientID: "xwork-cli", SessionID: session.SID}), "wrong client must not revoke desktop SID even with control credential")
	_, _, err = ValidateXCodeLoginSession(identity)
	require.NoError(t, err)

	refreshed, _, err := RefreshXCodeLoginSession(raw, session.SID, "", "")
	require.NoError(t, err)
	assert.Equal(t, session.ExpiresAt, refreshed.AccessExpiresAt)
	_, err = ParseXCodeAccessToken(refreshed.AccessToken)
	require.NoError(t, err)
	_, _, err = RefreshLoginSession(raw, session.SID, "", "")
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied, "race recovery must never issue a dashboard token")
	recovered, _, err := RefreshXCodeLoginSession(raw, session.SID, "", "")
	require.NoError(t, err)
	assert.Equal(t, refreshed.RefreshToken, recovered.RefreshToken)
	assert.ErrorIs(t, RevokeByRefreshToken(recovered.RefreshToken, session.SID, "logout"), ErrAuthCredentialScopeDenied)
	stored, err = model.GetUserSessionBySID(session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusActive, stored.Status)
	require.NoError(t, model.DB.Model(key).Update("status", common.TokenStatusDisabled).Error)
	_, _, err = RefreshXCodeLoginSession(recovered.RefreshToken, session.SID, "", "")
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
	storedAfter, err := model.GetUserSessionBySID(session.SID)
	require.NoError(t, err)
	assert.Equal(t, stored.RefreshHash, storedAfter.RefreshHash, "invalid managed key must prevent rotation")
	native, err := CreateLoginSession(user.Id, "password", "", "")
	require.NoError(t, err)
	assert.ErrorIs(t, LogoutXCodeSession(nil, XCodeLogoutRequest{ClientID: XCodeClientID, SessionID: native.Session.SID, RefreshToken: native.RefreshToken}), ErrAuthCredentialScopeDenied)
	require.NoError(t, LogoutXCodeSession(nil, XCodeLogoutRequest{ClientID: XCodeClientID, SessionID: session.SID, RefreshToken: recovered.RefreshToken}))
	storedAfter, err = model.GetUserSessionBySID(session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, storedAfter.Status)
	nativeIdentity, err := ParseAccessToken(native.AccessToken)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(nativeIdentity)
	require.NoError(t, err, "XCode logout preserves the independent dashboard session")
}

func TestNativeLiveBundleExpiryAndXCodeRefreshFence(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	session, secret, err := newLoginSession(user.Id, user.AuthVersion, "password", "", "")
	require.NoError(t, err)
	session.ExpiresAt = time.Now().Add(time.Minute).Unix()
	require.NoError(t, model.CreateUserSession(session))
	bundle, err := issueAuthBundle(session, session.SID+"."+secret, true)
	require.NoError(t, err)
	assert.Equal(t, session.ExpiresAt, bundle.AccessExpiresAt)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	_, _, err = ValidateXCodeLoginSession(identity)
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
	_, _, err = RefreshXCodeLoginSession(bundle.RefreshToken, session.SID, "", "")
	assert.ErrorIs(t, err, ErrAuthCredentialScopeDenied)
}

func TestIndependentRedisSessionRevokeConvergesAfterCacheTTL(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	_, clientA, serverB, clientB := useIndependentAuthSessionRedis(t)

	common.RDB = clientA
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "node-a")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	common.RDB = clientB
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err)
	assert.NotEmpty(t, cachedLoginSessionKey(t, serverB), "node B must hold its own session cache entry")

	common.RDB = clientA
	require.NoError(t, RevokeByRefreshToken(bundle.RefreshToken, bundle.Session.SID, "logout"))

	serverB.FastForward(3 * time.Second)
	common.RDB = clientB
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
}

func TestIndependentRedisAuthVersionAdvanceConvergesAfterCacheTTL(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	_, clientA, serverB, clientB := useIndependentAuthSessionRedis(t)

	common.RDB = clientA
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "node-a")
	require.NoError(t, err)
	oldIdentity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	common.RDB = clientB
	_, _, err = ValidateLoginSession(oldIdentity)
	require.NoError(t, err)
	cacheKey := cachedLoginSessionKey(t, serverB)
	version := serverB.HGet(cacheKey, "Version")
	assert.Equal(t, "1", version, "node B must hold the pre-rotation session version")

	common.RDB = clientA
	rotated, err := AdvanceCurrentSessionSecurity(oldIdentity, "security_update")
	require.NoError(t, err)
	newIdentity, err := ParseAccessToken(rotated.AccessToken)
	require.NoError(t, err)
	assert.Greater(t, newIdentity.SessionVersion, oldIdentity.SessionVersion)
	assert.Greater(t, newIdentity.UserAuthVersion, oldIdentity.UserAuthVersion)

	serverB.FastForward(3 * time.Second)
	common.RDB = clientB
	_, _, err = ValidateLoginSession(newIdentity)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(oldIdentity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
}

func TestUserAuthVersionInvalidatesExistingSession(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	bundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	identity, err := ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)

	_, err = model.BumpUserAuthVersion(user.Id)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(identity)
	assert.ErrorIs(t, err, ErrLoginSessionRevoked)
	_, err = CreateLoginSessionAtAuthVersion(user.Id, identity.UserAuthVersion, "2fa", "127.0.0.1", "test-agent")
	assert.ErrorIs(t, err, ErrLoginSessionRevoked, "a pending 2FA flow must not survive an auth-version change")
}

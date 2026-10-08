package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTokenAutoGroupsRoundTripThroughRedisHashCache(t *testing.T) {
	useUserCacheMiniRedis(t)
	token := Token{
		Id:         42,
		UserId:     7,
		Key:        "token-auto-groups-cache-key",
		Name:       "auto-cache",
		Group:      "auto",
		AutoGroups: `["vip","default"]`,
	}

	require.NoError(t, cacheSetTokenForTest(token))
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, token.AutoGroups, cached.AutoGroups)
	groups, err := cached.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "default"}, groups)
}

func TestTokenUpdateSynchronouslyNarrowsPreheatedAutoGroupsCache(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := Token{
		UserId:          7,
		Key:             "token-auto-groups-update-cache-key",
		Name:            "auto-cache-update",
		Status:          common.TokenStatusEnabled,
		ExpiredTime:     -1,
		UnlimitedQuota:  true,
		Group:           "auto",
		CrossGroupRetry: true,
		AutoGroups:      `["default","vip"]`,
	}
	require.NoError(t, token.Insert())
	require.NoError(t, cacheSetTokenForTest(token))

	preheated, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.JSONEq(t, `["default","vip"]`, preheated.AutoGroups)

	require.NoError(t, token.SetAutoGroups([]string{"vip"}))
	require.NoError(t, token.Update())
	// Update 是限制性变更：写库前删除缓存并设置 fence。缓存不再提供旧的
	// 宽分组值，下一次读取必须看到收紧后的分组。
	_, cacheErr := cacheGetTokenByKey(token.Key)
	require.Error(t, cacheErr, "the pre-update cache entry must be invalidated")
	reloaded, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.JSONEq(t, `["vip"]`, reloaded.AutoGroups)
}

// cacheSetTokenForTest 以测试身份写入完整 token 缓存（含额度字段），
// 模拟“已水合”的缓存状态。
func cacheSetTokenForTest(token Token) error {
	_, err := cacheInitToken(token)
	return err
}

func TestManagedTokenCacheCannotLoseClientIdentity(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := newManagedTokenTestFixture(t)
	require.NoError(t, token.Insert())
	require.NoError(t, cacheSetTokenForTest(token))
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, "xcode", cached.ClientPurpose)
	assert.Equal(t, token.ClientSessionSID, cached.ClientSessionSID)

	for _, field := range []string{"CacheSchema", "ClientPurpose", "ClientSessionSID"} {
		t.Run("missing_"+field, func(t *testing.T) {
			require.NoError(t, common.RDB.HDel(context.Background(), getTokenCacheKey(token.Key), field).Err())
			_, err := cacheGetTokenByKey(token.Key)
			require.Error(t, err)
			reloaded, err := GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			assert.Equal(t, "xcode", reloaded.ClientPurpose)
			assert.Equal(t, "managed-session", reloaded.ClientSessionSID)
		})
	}
	require.NoError(t, common.RDB.HSet(context.Background(), getTokenCacheKey(token.Key), "ClientPurpose", "unknown").Err())
	_, err = cacheGetTokenByKey(token.Key)
	require.Error(t, err)
}

func TestTokenCacheRepairPreservesPendingQuota(t *testing.T) {
	for _, field := range []string{"CacheSchema", "ClientPurpose", "ClientSessionSID"} {
		t.Run(field, func(t *testing.T) {
			truncateTables(t)
			resetBatchUpdateTestState(t)
			useUserCacheMiniRedis(t)
			common.BatchUpdateEnabled = true
			token := createReserveTestToken(t, 9)
			reserved, err := TryReserveTokenQuota(token.Id, token.Key, 7, false)
			require.NoError(t, err)
			require.True(t, reserved)
			assert.Equal(t, 9, getTokenFromDB(t, token.Id).RemainQuota, "batch debit is not flushed yet")
			require.NoError(t, common.RDB.HDel(context.Background(), getTokenCacheKey(token.Key), field).Err())

			_, err = GetTokenByKey(token.Key, false)
			require.NoError(t, err)
			cached, err := cacheGetTokenByKey(token.Key)
			require.NoError(t, err)
			assert.Equal(t, 2, cached.RemainQuota)
			assert.Equal(t, 7, cached.UsedQuota)
			reserved, err = TryReserveTokenQuota(token.Id, token.Key, 3, false)
			require.NoError(t, err)
			assert.False(t, reserved, "repair must not restore stale database quota")
			batchUpdate()
			persisted := getTokenFromDB(t, token.Id)
			assert.Equal(t, 2, persisted.RemainQuota)
			assert.Equal(t, 7, persisted.UsedQuota)
		})
	}
}

func newManagedTokenTestFixture(t *testing.T) Token {
	t.Helper()
	session := newTestUserSession("managed-session", 7, time.Now().Unix())
	session.ClientPurpose = ClientPurposeXCode
	require.NoError(t, DB.Create(session).Error)
	return Token{
		UserId: 7, Key: "managed-client-cache", Name: "managed",
		Status: common.TokenStatusEnabled, ClientPurpose: ClientPurposeXCode,
		ClientSessionSID: session.SID, UnlimitedQuota: true, ExpiredTime: session.ExpiresAt,
	}
}

func TestManagedTokenCreationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Token)
	}{
		{"finite_quota", func(token *Token) { token.UnlimitedQuota = false }},
		{"independent_budget", func(token *Token) { token.RemainQuota = 1 }},
		{"unbounded_expiry", func(token *Token) { token.ExpiredTime = -1 }},
		{"expired", func(token *Token) { token.ExpiredTime = time.Now().Unix() }},
		{"beyond_session", func(token *Token) { token.ExpiredTime++ }},
		{"explicit_group", func(token *Token) { token.Group = "default" }},
		{"auto_groups", func(token *Token) { token.AutoGroups = `["default"]` }},
		{"cross_group_retry", func(token *Token) { token.CrossGroupRetry = true }},
		{"disabled", func(token *Token) { token.Status = common.TokenStatusDisabled }},
		{"missing_sid", func(token *Token) { token.ClientSessionSID = "" }},
		{"wrong_user", func(token *Token) { token.UserId = 8 }},
		{"unknown_purpose", func(token *Token) { token.ClientPurpose = "unknown" }},
		{"general_with_sid", func(token *Token) { token.ClientPurpose = ClientPurposeGeneral }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			token := newManagedTokenTestFixture(t)
			tc.change(&token)
			require.ErrorIs(t, token.Insert(), ErrXCodeManagedTokenInvalid)
			var count int64
			require.NoError(t, DB.Model(&Token{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestManagedTokenMutationUsesStoredAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Token)
	}{
		{"lying_purpose_and_quota", func(token *Token) { token.ClientPurpose = ""; token.UnlimitedQuota = false }},
		{"lying_sid_and_routing", func(token *Token) { token.ClientSessionSID = ""; token.Group = "auto" }},
		{"expiry", func(token *Token) { token.ExpiredTime++ }},
		{"budget", func(token *Token) { token.RemainQuota = 1 }},
		{"model_limits", func(token *Token) { token.ModelLimitsEnabled = true }},
		{"allow_ips", func(token *Token) { ip := "127.0.0.1"; token.AllowIps = &ip }},
		{"auto_groups", func(token *Token) { token.AutoGroups = `["default"]` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			token := newManagedTokenTestFixture(t)
			require.NoError(t, token.Insert())
			tc.change(&token)
			require.ErrorIs(t, token.Update(), ErrXCodeManagedTokenInvalid)
			stored, err := GetTokenById(token.Id)
			require.NoError(t, err)
			assert.Equal(t, ClientPurposeXCode, stored.ClientPurpose)
			assert.True(t, stored.UnlimitedQuota)
			assert.Empty(t, stored.Group)
			assert.Zero(t, stored.RemainQuota)
		})
	}
	t.Run("safe_rename_and_authoritative_disable", func(t *testing.T) {
		truncateTables(t)
		useUserCacheMiniRedis(t)
		token := newManagedTokenTestFixture(t)
		require.NoError(t, token.Insert())
		require.NoError(t, cacheSetTokenForTest(token))
		_, err := GetUserSessionCached(token.ClientSessionSID)
		require.NoError(t, err)
		token.Name = "renamed"
		require.NoError(t, token.Update())
		stored, err := GetTokenById(token.Id)
		require.NoError(t, err)
		assert.Equal(t, "renamed", stored.Name)
		liar := Token{Id: token.Id, ClientPurpose: ClientPurposeGeneral, Status: common.TokenStatusDisabled}
		require.NoError(t, liar.SelectUpdate())
		_, err = GetUserSessionCached(token.ClientSessionSID)
		require.ErrorIs(t, err, ErrUserSessionInactive)
		liar.Status = common.TokenStatusEnabled
		require.ErrorIs(t, liar.SelectUpdate(), ErrXCodeManagedTokenInvalid)
	})
}

func TestManagedTokenDeleteRevokesOnlyRelatedXCodeSID(t *testing.T) {
	for _, operation := range []string{"delete", "by_id", "batch"} {
		t.Run(operation, func(t *testing.T) {
			truncateTables(t)
			useUserCacheMiniRedis(t)
			token := newManagedTokenTestFixture(t)
			require.NoError(t, token.Insert())
			require.NoError(t, cacheSetTokenForTest(token))
			_, err := GetUserSessionCached(token.ClientSessionSID)
			require.NoError(t, err)
			dashboard := newTestUserSession("dashboard-preserved", 7, time.Now().Unix())
			require.NoError(t, DB.Create(dashboard).Error)
			ordinary := Token{UserId: 7, Key: "ordinary", ExpiredTime: -1, Status: common.TokenStatusEnabled}
			require.NoError(t, ordinary.Insert())
			switch operation {
			case "delete":
				liar := Token{Id: token.Id, ClientPurpose: ClientPurposeGeneral}
				require.NoError(t, liar.Delete())
			case "by_id":
				require.NoError(t, DeleteTokenById(token.Id, 7))
			case "batch":
				count, err := BatchDeleteTokens([]int{token.Id, ordinary.Id, token.Id}, 7)
				require.NoError(t, err)
				assert.Equal(t, 2, count)
			}
			_, err = GetUserSessionCached(token.ClientSessionSID)
			require.ErrorIs(t, err, ErrUserSessionInactive)
			active, err := GetUserSessionBySID(dashboard.SID)
			require.NoError(t, err)
			assert.Equal(t, UserSessionStatusActive, active.Status)
		})
	}
}

func TestManagedTokenRevocationCacheErrorsFailClosed(t *testing.T) {
	for _, operation := range []string{"delete", "disable", "batch"} {
		t.Run(operation, func(t *testing.T) {
			truncateTables(t)
			server := useUserCacheMiniRedis(t)
			token := newManagedTokenTestFixture(t)
			require.NoError(t, token.Insert())
			require.NoError(t, cacheSetTokenForTest(token))
			_, err := GetUserSessionCached(token.ClientSessionSID)
			require.NoError(t, err)
			server.SetError("ERR injected cache outage")
			switch operation {
			case "delete":
				require.Error(t, token.Delete())
			case "disable":
				token.Status = common.TokenStatusDisabled
				require.Error(t, token.SelectUpdate())
			case "batch":
				count, err := BatchDeleteTokens([]int{token.Id}, 7)
				require.Error(t, err)
				assert.Zero(t, count)
			}
			stored, err := GetTokenById(token.Id)
			require.NoError(t, err)
			assert.Equal(t, common.TokenStatusEnabled, stored.Status)
			session, err := GetUserSessionBySID(token.ClientSessionSID)
			require.NoError(t, err)
			assert.Equal(t, UserSessionStatusActive, session.Status)
			server.SetError("")
		})
	}
	t.Run("key_cache_failure_after_sid_revocation", func(t *testing.T) {
		truncateTables(t)
		useUserCacheMiniRedis(t)
		token := newManagedTokenTestFixture(t)
		require.NoError(t, token.Insert())
		require.NoError(t, cacheSetTokenForTest(token))
		_, err := GetUserSessionCached(token.ClientSessionSID)
		require.NoError(t, err)
		common.RDB.AddHook(tokenMutationCacheFailureHook{})
		require.NoError(t, token.Delete())
		_, err = GetUserSessionCached(token.ClientSessionSID)
		require.ErrorIs(t, err, ErrUserSessionInactive)
		cached, err := cacheGetTokenByKey(token.Key)
		require.NoError(t, err)
		assert.Equal(t, common.TokenStatusEnabled, cached.Status, "SID fence must deny even while the old key hash survives")
	})
}

func TestGetXCodeSessionTokenAuthoritativeLookup(t *testing.T) {
	for _, mutation := range []string{"none", "disabled", "deleted", "wrong_user", "wrong_session_purpose", "wrong_key_purpose", "sid_mismatch", "revoked", "deny_fence", "expired_session", "expired_key", "unbounded", "beyond_session", "finite_quota", "routing", "duplicate", "deleted_duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			truncateTables(t)
			useUserCacheMiniRedis(t)
			token := newManagedTokenTestFixture(t)
			require.NoError(t, token.Insert())
			require.NoError(t, cacheSetTokenForTest(token))
			switch mutation {
			case "disabled":
				require.NoError(t, DB.Model(&token).Update("status", common.TokenStatusDisabled).Error)
			case "deleted":
				require.NoError(t, DB.Delete(&token).Error)
			case "wrong_user":
				require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Update("user_id", 8).Error)
			case "wrong_session_purpose":
				require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Update("client_purpose", ClientPurposeDashboard).Error)
			case "wrong_key_purpose":
				require.NoError(t, DB.Model(&token).Update("client_purpose", ClientPurposeGeneral).Error)
			case "sid_mismatch":
				require.NoError(t, DB.Model(&token).Update("client_session_sid", "another-sid").Error)
			case "deny_fence":
				session, err := GetUserSessionBySID(token.ClientSessionSID)
				require.NoError(t, err)
				require.NoError(t, writeUserSessionDenyFence(session, UserSessionStatusRevoking, time.Now().Unix(), "failed_mutation"))
			case "revoked":
				require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Update("status", UserSessionStatusRevoked).Error)
			case "expired_session":
				require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Update("expires_at", time.Now().Unix()).Error)
			case "expired_key":
				require.NoError(t, DB.Model(&token).Update("expired_time", time.Now().Unix()).Error)
			case "unbounded":
				require.NoError(t, DB.Model(&token).Update("expired_time", -1).Error)
			case "beyond_session":
				require.NoError(t, DB.Model(&token).Update("expired_time", token.ExpiredTime+1).Error)
			case "finite_quota":
				require.NoError(t, DB.Model(&token).Update("unlimited_quota", false).Error)
			case "routing":
				require.NoError(t, DB.Model(&token).Update("group", "auto").Error)
			case "duplicate", "deleted_duplicate":
				duplicate := token
				duplicate.Id = 0
				duplicate.Key = "duplicate"
				require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Create(&duplicate).Error)
				if mutation == "deleted_duplicate" {
					require.NoError(t, DB.Delete(&token).Error)
				}
			}
			got, err := GetXCodeSessionToken(7, token.ClientSessionSID)
			if mutation == "none" {
				require.NoError(t, err)
				assert.Equal(t, token.Id, got.Id)
				return
			}
			require.ErrorIs(t, err, ErrXCodeManagedTokenInvalid)
			assert.Nil(t, got)
		})
	}
	t.Run("backend_error_is_not_invalid", func(t *testing.T) {
		original := DB
		DB = DB.Session(&gorm.Session{}).WithContext(canceledTokenTestContext())
		t.Cleanup(func() { DB = original })
		_, err := GetXCodeSessionToken(7, "managed-session")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrXCodeManagedTokenInvalid)
	})
}

func canceledTokenTestContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type tokenMutationCacheFailureHook struct{}

func (tokenMutationCacheFailureHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if cmd.Name() == "set" {
		return ctx, errors.New("injected key cache failure")
	}
	return ctx, nil
}

func (tokenMutationCacheFailureHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (tokenMutationCacheFailureHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (tokenMutationCacheFailureHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestManagedTokenCannotRecreateDeletedCredential(t *testing.T) {
	truncateTables(t)
	token := newManagedTokenTestFixture(t)
	require.NoError(t, token.Insert())
	replacement := token
	replacement.Id = 0
	replacement.Key = "replacement"
	require.ErrorIs(t, replacement.Insert(), ErrXCodeManagedTokenInvalid)
	require.NoError(t, token.Delete())
	require.ErrorIs(t, replacement.Insert(), ErrXCodeManagedTokenInvalid)
	// Even an incorrectly restored SID cannot authorize another key for that SID.
	require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Updates(map[string]any{
		"status": UserSessionStatusActive, "revoked_at": 0,
	}).Error)
	require.ErrorIs(t, replacement.Insert(), ErrXCodeManagedTokenInvalid)
}

func TestManagedTokenCreationRequiresLiveXCodeSession(t *testing.T) {
	for _, field := range []string{"missing", "purpose", "revoked", "expired"} {
		t.Run(field, func(t *testing.T) {
			truncateTables(t)
			token := newManagedTokenTestFixture(t)
			switch field {
			case "missing":
				require.NoError(t, DB.Where("sid = ?", token.ClientSessionSID).Delete(&UserSession{}).Error)
			case "purpose":
				require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Update("client_purpose", ClientPurposeDashboard).Error)
			case "revoked":
				require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Update("revoked_at", time.Now().Unix()).Error)
			case "expired":
				require.NoError(t, DB.Model(&UserSession{}).Where("sid = ?", token.ClientSessionSID).Update("expires_at", time.Now().Unix()).Error)
			}
			require.ErrorIs(t, DB.Create(&token).Error, ErrXCodeManagedTokenInvalid)
		})
	}
}

func TestManagedTokenUsageAccountingKeepsCredential(t *testing.T) {
	truncateTables(t)
	token := newManagedTokenTestFixture(t)
	require.NoError(t, token.Insert())
	reserved, err := TryReserveTokenQuota(token.Id, token.Key, 7, true)
	require.NoError(t, err)
	require.True(t, reserved)
	current, err := GetXCodeSessionToken(7, token.ClientSessionSID)
	require.NoError(t, err)
	assert.Equal(t, -7, current.RemainQuota, "native unlimited-key accounting is not an independent budget")
	assert.Equal(t, 7, current.UsedQuota)
	current.Name = "renamed-after-usage"
	require.NoError(t, current.Update())
	require.NoError(t, current.Update(), "an unchanged managed update must remain successful on MySQL")
}

func TestManagedTokenRenamePreservesConcurrentUsageAccounting(t *testing.T) {
	truncateTables(t)
	token := newManagedTokenTestFixture(t)
	require.NoError(t, token.Insert())
	injected := false
	const callback = "managed-rename-concurrent-usage"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if injected || tx.Statement.Table != "tokens" {
			return
		}
		injected = true
		err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Model(&Token{}).
			Where("id = ?", token.Id).UpdateColumns(map[string]any{
			"remain_quota": gorm.Expr("remain_quota - ?", 7),
			"used_quota":   gorm.Expr("used_quota + ?", 7),
		}).Error
		if err != nil {
			_ = tx.AddError(err)
		}
	}))
	t.Cleanup(func() { require.NoError(t, DB.Callback().Update().Remove(callback)) })
	token.Name = "renamed-during-consumption"
	require.NoError(t, token.Update())
	current, err := GetXCodeSessionToken(token.UserId, token.ClientSessionSID)
	require.NoError(t, err)
	assert.Equal(t, token.Name, current.Name)
	assert.Equal(t, -7, current.RemainQuota)
	assert.Equal(t, 7, current.UsedQuota)
}

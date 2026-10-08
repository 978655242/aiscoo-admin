package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	token := Token{
		UserId: 7, Key: "managed-client-cache", Status: common.TokenStatusEnabled,
		ClientPurpose: "xcode", ClientSessionSID: "managed-session",
		UnlimitedQuota: true, ExpiredTime: -1,
	}
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

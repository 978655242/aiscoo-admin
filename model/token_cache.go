package model

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

func getTokenCacheKey(key string) string {
	return fmt.Sprintf("token:%s", common.GenerateHMAC(key))
}

func getTokenCacheFenceKey(key string) string {
	return fmt.Sprintf("token:fence:%s", common.GenerateHMAC(key))
}

func tokenCacheTTLSeconds() int {
	ttl := common.RedisKeyCacheSeconds()
	if ttl <= 0 {
		return 60
	}
	return ttl
}

// tokenCacheFenceSeconds must outlive a token mutation's database write plus
// any in-flight reader's DB-read-to-cache-init gap. The fence is not deleted
// after commit; it expires naturally so a reader holding a pre-mutation
// snapshot cannot publish it right after the mutation cleared the cache.
// While the fence exists readers simply serve the database without caching.
const tokenCacheFenceSeconds = 10

// Bump when the cached field set changes so older hashes are repaired.
const tokenCacheSchema = 2

// invalidateTokenCacheForMutation is called before a token metadata mutation
// writes to the database: it raises the fence and drops the cached hash so no
// reader can act on (or re-publish) the pre-mutation state.
func invalidateTokenCacheForMutation(key string) error {
	if !common.RedisEnabled || key == "" {
		return nil
	}
	ctx := context.Background()
	err := common.RDB.Set(ctx, getTokenCacheFenceKey(key), 1, time.Duration(tokenCacheFenceSeconds)*time.Second).Err()
	if err != nil {
		return err
	}
	return common.RDB.Del(ctx, getTokenCacheKey(key)).Err()
}

// cacheInitToken publishes a database snapshot only when no mutation fence is
// active. A valid live hash only gets its TTL refreshed. Repairing stale identity
// metadata preserves the matching token's quota counters: Redis may already
// contain atomic reservations whose database batch has not been flushed.
// 返回值：0=被 fence 拦截，1=完成初始化/修复，2=哈希已存在，仅刷新 TTL。
func cacheInitToken(token Token) (int, error) {
	if !common.RedisEnabled {
		return 0, nil
	}
	allowIps := ""
	if token.AllowIps != nil {
		allowIps = *token.AllowIps
	}
	purpose := token.ClientPurpose
	if purpose == "" {
		purpose = ClientPurposeGeneral
	}
	const script = `
if redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
local remain = ARGV[15]
local used = ARGV[16]
if redis.call('EXISTS', KEYS[1]) == 1 then
  local purpose = redis.call('HGET', KEYS[1], 'ClientPurpose')
  local sid = redis.call('HGET', KEYS[1], 'ClientSessionSID')
  if redis.call('HGET', KEYS[1], 'Id') == ARGV[1] and
     redis.call('HGET', KEYS[1], 'UserId') == ARGV[2] and
     redis.call('HGET', KEYS[1], 'CacheSchema') == ARGV[19] and
     ((purpose == 'general' and sid == '') or (purpose == 'xcode' and sid and sid ~= '')) then
    redis.call('EXPIRE', KEYS[1], ARGV[18])
    return 2
  end
  if redis.call('HGET', KEYS[1], 'Id') == ARGV[1] then
    remain = redis.call('HGET', KEYS[1], 'RemainQuota') or remain
    used = redis.call('HGET', KEYS[1], 'UsedQuota') or used
  end
  redis.call('DEL', KEYS[1])
end
redis.call('HSET', KEYS[1],
  'Id', ARGV[1], 'UserId', ARGV[2], 'Status', ARGV[3], 'Name', ARGV[4],
  'CreatedTime', ARGV[5], 'AccessedTime', ARGV[6], 'ExpiredTime', ARGV[7],
  'UnlimitedQuota', ARGV[8], 'ModelLimitsEnabled', ARGV[9], 'ModelLimits', ARGV[10],
  'AllowIps', ARGV[11], 'Group', ARGV[12], 'CrossGroupRetry', ARGV[13],
  'AutoGroups', ARGV[14], 'RemainQuota', remain, 'UsedQuota', used,
  'AllowUserQuotaQuery', ARGV[17],
  'CacheSchema', ARGV[19], 'ClientPurpose', ARGV[20], 'ClientSessionSID', ARGV[21])
redis.call('EXPIRE', KEYS[1], ARGV[18])
return 1`

	return common.RDB.Eval(context.Background(), script, []string{
		getTokenCacheKey(token.Key), getTokenCacheFenceKey(token.Key),
	},
		token.Id, token.UserId, token.Status, token.Name,
		token.CreatedTime, token.AccessedTime, token.ExpiredTime,
		strconv.FormatBool(token.UnlimitedQuota), strconv.FormatBool(token.ModelLimitsEnabled),
		token.ModelLimits, allowIps, token.Group, strconv.FormatBool(token.CrossGroupRetry),
		token.AutoGroups, token.RemainQuota, token.UsedQuota,
		strconv.FormatBool(token.AllowUserQuotaQuery),
		tokenCacheTTLSeconds(), tokenCacheSchema, purpose, token.ClientSessionSID,
	).Int()
}

// cacheGetTokenByKey 从缓存读取 token；不完整的哈希（如仅有配额字段）会被拒绝。
func cacheGetTokenByKey(key string) (*Token, error) {
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var token Token
	if err := common.RedisHGetObj(getTokenCacheKey(key), &token, "CacheSchema", "ClientPurpose", "ClientSessionSID"); err != nil {
		return nil, err
	}
	if token.Id <= 0 || token.UserId <= 0 || token.CacheSchema != tokenCacheSchema {
		return nil, fmt.Errorf("token cache is incomplete or stale")
	}
	if token.ClientPurpose != ClientPurposeGeneral && token.ClientPurpose != ClientPurposeXCode {
		return nil, fmt.Errorf("token cache purpose is invalid")
	}
	if (token.ClientPurpose == ClientPurposeXCode) != (token.ClientSessionSID != "") {
		return nil, fmt.Errorf("token cache session binding is invalid")
	}
	token.Key = key
	return &token, nil
}

package model

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedSubscriptionResetPlan(t *testing.T, plan *SubscriptionPlan) {
	t.Helper()
	require.NoError(t, DB.Create(plan).Error)
}

func seedSubscriptionResetSub(t *testing.T, sub *UserSubscription) {
	t.Helper()
	require.NoError(t, DB.Create(sub).Error)
}

func getSubscriptionResetSub(t *testing.T, id int) UserSubscription {
	t.Helper()
	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", id).First(&sub).Error)
	return sub
}

func seedSubscriptionResetRecord(t *testing.T, record *SubscriptionPreConsumeRecord) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&SubscriptionPreConsumeRecord{}))
	require.NoError(t, DB.Create(record).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Where("request_id = ?", record.RequestId).Delete(&SubscriptionPreConsumeRecord{}).Error)
	})
}

func TestAdminResetUserSubscriptionsByPlanResetsAllActiveMatchesAndAdvancesTime(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9101,
		Title:            "Pro",
		PriceAmount:      10,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      1000,
		QuotaResetPeriod: SubscriptionResetDaily,
	}
	otherPlan := &SubscriptionPlan{
		Id:               9102,
		Title:            "Basic",
		PriceAmount:      1,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      100,
		QuotaResetPeriod: SubscriptionResetDaily,
	}
	seedSubscriptionResetPlan(t, plan)
	seedSubscriptionResetPlan(t, otherPlan)

	activeEnd := now + 30*24*3600
	expiredEnd := now - 1
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9201, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 300, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9202, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9203, UserId: 101, PlanId: otherPlan.Id, AmountTotal: 100, AmountUsed: 60, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9204, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 700, StartTime: now - 7200, EndTime: expiredEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9205, UserId: 102, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 800, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 120})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9206, UserId: 101, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 900, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 120})

	beforeReset := GetDBTimestamp()
	result, err := AdminResetUserSubscriptionsByPlan(101, plan.Id, true)
	afterReset := GetDBTimestamp()

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, plan.Id, result.PlanId)
	assert.Equal(t, 2, result.MatchedCount)
	assert.Equal(t, 2, result.ResetCount)
	assert.Equal(t, 1, result.UserCount)
	assert.Equal(t, []int{101}, result.AffectedUserIds)
	assert.True(t, result.AdvanceResetTime)

	for _, id := range []int{9201, 9202} {
		sub := getSubscriptionResetSub(t, id)
		assert.Zero(t, sub.AmountUsed)
		assert.GreaterOrEqual(t, sub.LastResetTime, beforeReset)
		assert.LessOrEqual(t, sub.LastResetTime, afterReset)
		assert.Equal(t, calcNextResetTime(time.Unix(sub.LastResetTime, 0), plan, sub.EndTime), sub.NextResetTime)
	}
	assert.EqualValues(t, 60, getSubscriptionResetSub(t, 9203).AmountUsed)
	assert.EqualValues(t, 700, getSubscriptionResetSub(t, 9204).AmountUsed)
	assert.EqualValues(t, 800, getSubscriptionResetSub(t, 9205).AmountUsed)
	assert.EqualValues(t, 900, getSubscriptionResetSub(t, 9206).AmountUsed)
}

func TestAdminResetUserSubscriptionsByPlanKeepsResetTimes(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9301,
		Title:            "Team",
		PriceAmount:      20,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      2000,
		QuotaResetPeriod: SubscriptionResetMonthly,
	}
	seedSubscriptionResetPlan(t, plan)

	lastReset := now - 86400
	nextReset := now + 86400
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9302, UserId: 201, PlanId: plan.Id, AmountTotal: 2000, AmountUsed: 1200, StartTime: now - 172800, EndTime: now + 30*24*3600, Status: "active", LastResetTime: lastReset, NextResetTime: nextReset})

	result, err := AdminResetUserSubscriptionsByPlan(201, plan.Id, false)

	require.NoError(t, err)
	assert.False(t, result.AdvanceResetTime)
	sub := getSubscriptionResetSub(t, 9302)
	assert.Zero(t, sub.AmountUsed)
	assert.Equal(t, lastReset, sub.LastResetTime)
	assert.Equal(t, nextReset, sub.NextResetTime)
}

func TestAdminResetUserSubscriptionsByPlanNoActiveMatchReturnsError(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:            9401,
		Title:         "Expired",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, plan)
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9402, UserId: 301, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500, StartTime: now - 7200, EndTime: now - 1, Status: "active"})

	result, err := AdminResetUserSubscriptionsByPlan(301, plan.Id, true)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, strings.Contains(err.Error(), "该用户没有有效的此套餐订阅"))
}

func TestAdminResetPlanSubscriptionsResetsAllActiveUsers(t *testing.T) {
	truncateTables(t)

	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               9501,
		Title:            "Business",
		PriceAmount:      30,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      3000,
		QuotaResetPeriod: SubscriptionResetNever,
	}
	seedSubscriptionResetPlan(t, plan)

	activeEnd := now + 30*24*3600
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9502, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1000, StartTime: now - 3600, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9503, UserId: 401, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1100, StartTime: now - 3500, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9504, UserId: 402, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1200, StartTime: now - 3400, EndTime: activeEnd, Status: "active", LastResetTime: now - 3600, NextResetTime: now + 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9505, UserId: 403, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1300, StartTime: now - 7200, EndTime: now - 1, Status: "active", LastResetTime: now - 3600, NextResetTime: now - 10})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9506, UserId: 404, PlanId: plan.Id, AmountTotal: 3000, AmountUsed: 1400, StartTime: now - 3600, EndTime: activeEnd, Status: "cancelled", LastResetTime: now - 3600, NextResetTime: now + 10})

	result, err := AdminResetPlanSubscriptions(plan.Id, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.MatchedCount)
	assert.Equal(t, 3, result.ResetCount)
	assert.Equal(t, 2, result.UserCount)
	assert.Equal(t, []int{401, 402}, result.AffectedUserIds)
	for _, id := range []int{9502, 9503, 9504} {
		sub := getSubscriptionResetSub(t, id)
		assert.Zero(t, sub.AmountUsed)
		assert.Zero(t, sub.LastResetTime)
		assert.Zero(t, sub.NextResetTime)
	}
	assert.EqualValues(t, 1300, getSubscriptionResetSub(t, 9505).AmountUsed)
	assert.EqualValues(t, 1400, getSubscriptionResetSub(t, 9506).AmountUsed)
}

func TestAdminResetPlanSubscriptionsNoMatchSucceeds(t *testing.T) {
	truncateTables(t)

	plan := &SubscriptionPlan{
		Id:            9601,
		Title:         "Empty",
		PriceAmount:   10,
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   1000,
	}
	seedSubscriptionResetPlan(t, plan)

	result, err := AdminResetPlanSubscriptions(plan.Id, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.MatchedCount)
	assert.Zero(t, result.ResetCount)
	assert.Zero(t, result.UserCount)
	assert.Empty(t, result.AffectedUserIds)
}

func TestXCodeRequestLedgerRejectsCorruptOccupancyAndPreservesFinalFact(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	sub := &UserSubscription{
		Id: 9701, UserId: 701, PlanId: 9799, UsageScope: SubscriptionUsageScopeXCode,
		QuotaResetPeriodSnapshot: SubscriptionResetNever, AmountTotal: 1000, AmountUsed: 100,
		StartTime: now - 10, EndTime: now + 3600, Status: "active",
	}
	seedSubscriptionResetSub(t, sub)
	record := &SubscriptionPreConsumeRecord{
		RequestId: "xcode-corrupt-occupancy", UserId: sub.UserId, UserSubscriptionId: sub.Id,
		PreConsumed: 100, FundingScope: SubscriptionUsageScopeXCode, ResetGeneration: 1,
		CycleReservedQuota: 101, Status: "consumed",
	}
	seedSubscriptionResetRecord(t, record)
	require.ErrorIs(t, RefundXCodeSubscriptionRequest(record.RequestId, sub.UserId, sub.Id), ErrSubscriptionRequestConflict)
	assert.EqualValues(t, 100, getSubscriptionResetSub(t, sub.Id).AmountUsed)
	require.NoError(t, DB.Model(record).Update("cycle_reserved_quota", 100).Error)
	require.NoError(t, SettleXCodeSubscriptionRequest(record.RequestId, sub.UserId, sub.Id, 150))
	require.NoError(t, SettleXCodeSubscriptionRequest(record.RequestId, sub.UserId, sub.Id, 150))
	require.ErrorIs(t, SettleXCodeSubscriptionRequest(record.RequestId, sub.UserId, sub.Id, 100), ErrSubscriptionRequestConflict)
	require.NoError(t, RefundXCodeSubscriptionRequest(record.RequestId, sub.UserId, sub.Id))
	assert.EqualValues(t, 150, getSubscriptionResetSub(t, sub.Id).AmountUsed)
	require.NoError(t, DB.First(record, record.Id).Error)
	assert.Equal(t, "settled", record.Status)
	assert.EqualValues(t, 150, record.FinalQuota)
}

func TestXCodeLazyResetUsesSnapshotWithoutTemplateAndStopsAtExpiry(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	sub := &UserSubscription{
		Id: 9801, UserId: 801, PlanId: 9899, UsageScope: SubscriptionUsageScopeXCode,
		QuotaResetPeriodSnapshot: SubscriptionResetCustom, QuotaResetCustomSecondsSnapshot: 60,
		AmountTotal: 1000, AmountUsed: 100, StartTime: now - 90, EndTime: now + 3600,
		Status: "active", LastResetTime: now - 90, NextResetTime: now - 30,
	}
	seedSubscriptionResetSub(t, sub)
	record := &SubscriptionPreConsumeRecord{
		RequestId: "xcode-snapshot-no-template", UserId: sub.UserId, UserSubscriptionId: sub.Id,
		PreConsumed: 100, FundingScope: SubscriptionUsageScopeXCode, ResetGeneration: 1,
		CycleReservedQuota: 100, Status: "consumed",
	}
	seedSubscriptionResetRecord(t, record)
	require.NoError(t, SettleXCodeSubscriptionRequest(record.RequestId, sub.UserId, sub.Id, 100))
	fresh := getSubscriptionResetSub(t, sub.Id)
	assert.EqualValues(t, 100, fresh.AmountUsed)
	assert.EqualValues(t, 2, fresh.QuotaResetGeneration)
	assert.EqualValues(t, now-30, fresh.LastResetTime)
	require.NoError(t, DB.Model(sub).Updates(map[string]interface{}{
		"end_time": now - 1, "last_reset_time": now - 90, "next_reset_time": now - 30,
	}).Error)
	expired := &SubscriptionPreConsumeRecord{
		RequestId: "xcode-expired-finalization", UserId: sub.UserId, UserSubscriptionId: sub.Id,
		PreConsumed: 20, FundingScope: SubscriptionUsageScopeXCode, ResetGeneration: 2,
		CycleReservedQuota: 20, Status: "consumed",
	}
	seedSubscriptionResetRecord(t, expired)
	require.NoError(t, SettleXCodeSubscriptionRequest(expired.RequestId, sub.UserId, sub.Id, 30))
	fresh = getSubscriptionResetSub(t, sub.Id)
	assert.EqualValues(t, 110, fresh.AmountUsed)
	assert.EqualValues(t, 2, fresh.QuotaResetGeneration)
}

func TestXCodeLegacyDeltaFenceAndCheckedQuota(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	sub := &UserSubscription{
		Id: 9901, UserId: 901, UsageScope: SubscriptionUsageScopeXCode,
		QuotaResetPeriodSnapshot: SubscriptionResetNever, AmountUsed: math.MaxInt64 - 1,
		StartTime: now - 10, EndTime: now + 3600, Status: "active",
	}
	seedSubscriptionResetSub(t, sub)
	record := &SubscriptionPreConsumeRecord{
		RequestId: "xcode-quota-overflow", UserId: sub.UserId, UserSubscriptionId: sub.Id,
		PreConsumed: 1, FundingScope: SubscriptionUsageScopeXCode, ResetGeneration: 1,
		CycleReservedQuota: 1, Status: "consumed",
	}
	seedSubscriptionResetRecord(t, record)
	require.ErrorIs(t, PostConsumeUserSubscriptionDelta(sub.Id, 0), ErrSubscriptionFundingTargetInvalid)
	require.ErrorIs(t, RefundSubscriptionPreConsume(record.RequestId), ErrSubscriptionFundingTargetInvalid)
	_, err := ReserveXCodeSubscriptionRequest(record.RequestId, sub.UserId, sub.Id, 3)
	require.ErrorIs(t, err, ErrSubscriptionQuotaInsufficient)
	assert.EqualValues(t, math.MaxInt64-1, getSubscriptionResetSub(t, sub.Id).AmountUsed)
	require.NoError(t, DB.Model(sub).Update("usage_scope", "unknown").Error)
	require.ErrorIs(t, PostConsumeUserSubscriptionDelta(sub.Id, 0), ErrSubscriptionFundingTargetInvalid)
	require.NoError(t, DB.Model(sub).Update("usage_scope", SubscriptionUsageScopeGeneral).Error)
	require.ErrorIs(t, PostConsumeUserSubscriptionDelta(sub.Id, 2), ErrSubscriptionQuotaInsufficient)
	require.NoError(t, PostConsumeUserSubscriptionDelta(sub.Id, math.MinInt64))
	assert.Zero(t, getSubscriptionResetSub(t, sub.Id).AmountUsed)
}

func TestXCodeAutomaticAndAdminResetKeepImmutablePolicy(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	automatic := &UserSubscription{
		Id: 9911, UserId: 911, PlanId: 9999, UsageScope: SubscriptionUsageScopeXCode,
		QuotaResetPeriodSnapshot: SubscriptionResetCustom, QuotaResetCustomSecondsSnapshot: 60,
		AmountTotal: 1000, AmountUsed: 100, StartTime: now - 90, EndTime: now + 3600,
		Status: "active", LastResetTime: now - 90, NextResetTime: now - 30,
	}
	seedSubscriptionResetSub(t, automatic)
	count, err := ResetDueSubscriptions(200)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	fresh := getSubscriptionResetSub(t, automatic.Id)
	assert.Zero(t, fresh.AmountUsed)
	assert.EqualValues(t, 2, fresh.QuotaResetGeneration)
	assert.EqualValues(t, now-30, fresh.LastResetTime)
	count, err = ResetDueSubscriptions(200)
	require.NoError(t, err)
	assert.Zero(t, count)

	plan := &SubscriptionPlan{
		Id: 9921, Title: "Edited reset policy", DurationUnit: SubscriptionDurationDay,
		DurationValue: 1, TotalAmount: 1000, QuotaResetPeriod: SubscriptionResetDaily,
	}
	seedSubscriptionResetPlan(t, plan)
	manual := &UserSubscription{
		Id: 9922, UserId: 922, PlanId: plan.Id, UsageScope: SubscriptionUsageScopeXCode,
		QuotaResetPeriodSnapshot: SubscriptionResetNever, AmountTotal: 1000, AmountUsed: 100,
		StartTime: now - 10, EndTime: now + 3600, Status: "active",
	}
	seedSubscriptionResetSub(t, manual)
	_, err = AdminResetUserSubscriptionsByPlan(manual.UserId, plan.Id, false)
	require.NoError(t, err)
	fresh = getSubscriptionResetSub(t, manual.Id)
	assert.Zero(t, fresh.AmountUsed)
	assert.EqualValues(t, 2, fresh.QuotaResetGeneration)
	_, err = AdminResetUserSubscriptionsByPlan(manual.UserId, plan.Id, true)
	require.NoError(t, err)
	fresh = getSubscriptionResetSub(t, manual.Id)
	assert.EqualValues(t, 3, fresh.QuotaResetGeneration)
	assert.Zero(t, fresh.NextResetTime)
	assert.Equal(t, SubscriptionResetNever, fresh.QuotaResetPeriodSnapshot)
}

func TestXCodeResetPreservesAnchorWhenLifetimeTruncatesNextReset(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	sub := &UserSubscription{
		Id: 9931, UserId: 931, UsageScope: SubscriptionUsageScopeXCode,
		QuotaResetPeriodSnapshot: SubscriptionResetCustom, QuotaResetCustomSecondsSnapshot: 60,
		AmountTotal: 1000, AmountUsed: 100, StartTime: now - 20, EndTime: now + 30,
		Status: "active", LastResetTime: now - 20, NextResetTime: 0,
	}
	seedSubscriptionResetSub(t, sub)
	require.NoError(t, resetUserSubscriptionTx(DB, sub, nil, now, true))
	fresh := getSubscriptionResetSub(t, sub.Id)
	assert.Zero(t, fresh.NextResetTime)
	assert.EqualValues(t, now, fresh.LastResetTime, "renew must restore the reset from the advanced anchor, not StartTime")
	assert.EqualValues(t, 2, fresh.QuotaResetGeneration)
	resetPlan, err := subscriptionResetPlan(&fresh, nil)
	require.NoError(t, err)
	assert.EqualValues(t, now+60, calcNextResetTime(time.Unix(fresh.LastResetTime, 0), resetPlan, now+3600))
}

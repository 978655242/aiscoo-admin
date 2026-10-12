package service

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func GetXCodeMe(userID int, sessionID string) (*dto.XCodeMeSnapshot, error) {
	if userID <= 0 || sessionID == "" {
		return nil, model.ErrSubscriptionFundingTargetInvalid
	}
	if model.DB == nil {
		return nil, errors.New("profile database is unavailable")
	}
	var user model.User
	if err := model.DB.Select("id", "username", "display_name").First(&user, userID).Error; err != nil {
		return nil, err
	}
	state, err := model.GetXCodeAuthorizationState(userID)
	if err != nil {
		return nil, err
	}
	if state == nil || state.Now <= 0 || state.Now > math.MaxInt64/1000 {
		return nil, errors.New("invalid authorization state clock")
	}
	result := &dto.XCodeMeSnapshot{
		SchemaVersion: 1, ServerTime: state.Now * 1000, SessionID: sessionID, AuthorizationRevision: "0",
		User: dto.XCodeUser{ID: strconv.Itoa(user.Id), Username: user.Username, DisplayName: user.DisplayName},
	}
	if state.Authorization != nil {
		if state.Authorization.UserId != userID || state.Authorization.Revision <= 0 {
			return nil, errors.New("invalid authorization relationship")
		}
		result.AuthorizationRevision = strconv.FormatInt(state.Authorization.Revision, 10)
	}
	return result, nil
}

func loadXCodePlanBatch() ([]model.SubscriptionPlan, error) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return []model.SubscriptionPlan{}, nil
	}
	if model.DB == nil {
		return nil, errors.New("plan database is unavailable")
	}
	var plans []model.SubscriptionPlan
	// XCODE: Web plans and presentation sidecars consume this one ordered native batch.
	if err := model.DB.Select("id", "title", "subtitle", "price_amount", "currency", "duration_unit", "duration_value",
		"custom_seconds", "total_amount", "quota_reset_period", "quota_reset_custom_seconds", "enabled", "sort_order", "max_purchase_per_user", "xcode_display").
		Where("enabled = ?", true).Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		return nil, err
	}
	return plans, nil
}

func projectXCodePlans(plans []model.SubscriptionPlan) (*dto.XCodePlansSnapshot, error) {
	result := &dto.XCodePlansSnapshot{
		SchemaVersion: 1, ServerTime: time.Now().UnixMilli(), Items: []dto.XCodePlan{}, PurchaseMode: "manual_admin",
	}
	result.Items = make([]dto.XCodePlan, 0, len(plans))
	for _, plan := range plans {
		if plan.Id <= 0 || plan.PriceAmount < 0 || math.IsNaN(plan.PriceAmount) || math.IsInf(plan.PriceAmount, 0) ||
			strings.TrimSpace(plan.Currency) == "" || plan.TotalAmount < 0 || plan.DurationValue < 0 ||
			plan.CustomSeconds < 0 || plan.QuotaResetCustomSeconds < 0 {
			return nil, errors.New("invalid native plan amounts")
		}
		switch plan.DurationUnit {
		case model.SubscriptionDurationYear:
			if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/1000/(366*86400) {
				return nil, errors.New("invalid native plan duration")
			}
		case model.SubscriptionDurationMonth:
			if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/1000/(31*86400) {
				return nil, errors.New("invalid native plan duration")
			}
		case model.SubscriptionDurationDay:
			if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/int64(24*time.Hour) {
				return nil, errors.New("invalid native plan duration")
			}
		case model.SubscriptionDurationHour:
			if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/int64(time.Hour) {
				return nil, errors.New("invalid native plan duration")
			}
		case model.SubscriptionDurationCustom:
			if plan.CustomSeconds <= 0 || plan.CustomSeconds > math.MaxInt64/int64(time.Second) {
				return nil, errors.New("invalid native plan duration")
			}
		default:
			return nil, errors.New("invalid native plan duration")
		}
		if plan.QuotaResetPeriod != model.NormalizeResetPeriod(plan.QuotaResetPeriod) ||
			(plan.QuotaResetPeriod == model.SubscriptionResetCustom &&
				(plan.QuotaResetCustomSeconds <= 0 || plan.QuotaResetCustomSeconds > math.MaxInt64/int64(time.Second))) {
			return nil, errors.New("invalid native plan reset period")
		}
		result.Items = append(result.Items, dto.XCodePlan{
			ID: strconv.Itoa(plan.Id), Title: plan.Title, Subtitle: plan.Subtitle,
			PriceAmount: strconv.FormatFloat(plan.PriceAmount, 'f', -1, 64), Currency: plan.Currency,
			DurationUnit: plan.DurationUnit, DurationValue: strconv.Itoa(plan.DurationValue),
			DurationCustomSeconds: strconv.FormatInt(plan.CustomSeconds, 10), TotalQuota: strconv.FormatInt(plan.TotalAmount, 10),
			ResetPeriod: plan.QuotaResetPeriod, ResetCustomSeconds: strconv.FormatInt(plan.QuotaResetCustomSeconds, 10), Enabled: plan.Enabled,
		})
	}
	return result, nil
}

func GetXCodePlans() (*dto.XCodePlansSnapshot, error) {
	plans, err := loadXCodePlanBatch()
	if err != nil {
		return nil, err
	}
	return projectXCodePlans(plans)
}

func GetXCodeEntitlements(userID int) (*dto.XCodeAuthorizationSnapshot, error) {
	if userID <= 0 {
		return nil, model.ErrSubscriptionFundingTargetInvalid
	}
	if model.DB == nil {
		return nil, errors.New("authorization database is unavailable")
	}
	state, err := model.GetXCodeAuthorizationState(userID)
	if err != nil {
		return nil, err
	}
	return XCodeAuthorizationSnapshot(state)
}

func GetXCodeClientConfig() (*dto.XCodeClientConfigSnapshot, error) {
	display, err := GetXCodeQuotaDisplay()
	if err != nil {
		return nil, err
	}
	return &dto.XCodeClientConfigSnapshot{
		SchemaVersion: 1, ServerTime: time.Now().UnixMilli(), PurchaseMode: "manual_admin",
		ManualPlanEntryPath: "/xcode/plans", Display: display,
	}, nil
}

func GetXCodeQuotaDisplay() (dto.XCodeQuotaDisplay, error) {
	if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return dto.XCodeQuotaDisplay{}, errors.New("invalid native quota display unit")
	}
	result := dto.XCodeQuotaDisplay{
		QuotaPerUnit: strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64),
		DisplayType:  operation_setting.GetQuotaDisplayType(), CurrencySymbol: operation_setting.GetCurrencySymbol(),
	}
	switch result.DisplayType {
	case operation_setting.QuotaDisplayTypeUSD, operation_setting.QuotaDisplayTypeCNY:
		currency := result.DisplayType
		result.Currency = &currency
	case operation_setting.QuotaDisplayTypeCustom:
		rate := operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
		if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return dto.XCodeQuotaDisplay{}, errors.New("invalid native quota display exchange rate")
		}
	case operation_setting.QuotaDisplayTypeTokens:
		return result, nil
	default:
		return dto.XCodeQuotaDisplay{}, errors.New("invalid native quota display type")
	}
	rate := operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate)
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return dto.XCodeQuotaDisplay{}, errors.New("invalid native quota display exchange rate")
	}
	value := strconv.FormatFloat(rate, 'f', -1, 64)
	result.USDToCurrencyRate = &value
	return result, nil
}

func xcodeTimestampMillis(seconds int64) (*int64, error) {
	if seconds == 0 {
		return nil, nil
	}
	if seconds < 0 || seconds > math.MaxInt64/1000 {
		return nil, errors.New("invalid subscription timestamp")
	}
	value := seconds * 1000
	return &value, nil
}

// XCodeAuthorizationSnapshot serves administrators and read-only control clients.
func XCodeAuthorizationSnapshot(state *model.XCodeAuthorizationState) (*dto.XCodeAuthorizationSnapshot, error) {
	if state == nil || state.Now <= 0 || state.Now > math.MaxInt64/1000 {
		return nil, errors.New("invalid authorization state clock")
	}
	result := &dto.XCodeAuthorizationSnapshot{
		SchemaVersion: 1, ServerTime: state.Now * 1000, Status: "unassigned", AuthorizationRevision: "0",
		FundingPolicy: dto.XCodeFundingPolicy{SubscriptionOnly: true}, PurchaseMode: "manual_admin",
	}
	if state.Authorization == nil {
		if state.Subscription != nil {
			return nil, errors.New("subscription has no authorization relationship")
		}
		return result, nil
	}
	if state.Authorization.Revision <= 0 || state.Subscription == nil {
		return nil, errors.New("invalid designated subscription relationship")
	}
	sub := state.Subscription
	if sub.Id <= 0 || sub.UserId <= 0 || sub.PlanId <= 0 ||
		sub.Id != state.Authorization.SubscriptionId || sub.UserId != state.Authorization.UserId ||
		sub.UsageScope != model.SubscriptionUsageScopeXCode || sub.Source != "admin" || sub.AmountTotal < 0 || sub.AmountUsed < 0 ||
		sub.QuotaResetCustomSecondsSnapshot < 0 || sub.QuotaResetPeriodSnapshot != model.NormalizeResetPeriod(sub.QuotaResetPeriodSnapshot) ||
		(sub.QuotaResetPeriodSnapshot == model.SubscriptionResetCustom &&
			(sub.QuotaResetCustomSecondsSnapshot <= 0 || sub.QuotaResetCustomSecondsSnapshot > math.MaxInt64/int64(time.Second))) {
		return nil, errors.New("invalid designated subscription facts")
	}
	result.AuthorizationRevision = strconv.FormatInt(state.Authorization.Revision, 10)
	result.Status = "active"
	switch {
	case sub.Status == "cancelled":
		result.Status = "revoked"
	case sub.Status == "expired" || sub.EndTime <= state.Now:
		result.Status = "expired"
	case sub.Status != "active" || sub.StartTime > state.Now:
		return nil, errors.New("invalid designated subscription lifetime")
	case sub.AmountTotal > 0 && sub.AmountUsed >= sub.AmountTotal:
		result.Status = "exhausted"
	}
	start, err := xcodeTimestampMillis(sub.StartTime)
	if err != nil {
		return nil, err
	}
	end, err := xcodeTimestampMillis(sub.EndTime)
	if err != nil {
		return nil, err
	}
	last, err := xcodeTimestampMillis(sub.LastResetTime)
	if err != nil {
		return nil, err
	}
	next, err := xcodeTimestampMillis(sub.NextResetTime)
	if err != nil {
		return nil, err
	}
	zone := time.Local.String()
	if zone == "Local" || zone == "" {
		zone = "server-local"
	} else if _, err := time.LoadLocation(zone); err != nil {
		zone = "server-local"
	}
	_, offset := time.Unix(state.Now, 0).In(time.Local).Zone()
	result.Subscription = &dto.XCodeSubscription{
		ID: strconv.Itoa(sub.Id), PlanID: strconv.Itoa(sub.PlanId), PlanTitle: state.PlanTitle,
		Source: "admin", UsageScope: model.SubscriptionUsageScopeXCode, StartTime: start, EndTime: end,
	}
	result.Quota = &dto.XCodeSubscriptionQuota{
		Scope: "xcode-subscription", Unit: "quota", Unlimited: sub.AmountTotal == 0, Used: strconv.FormatInt(sub.AmountUsed, 10),
		ResetPeriod: sub.QuotaResetPeriodSnapshot, ResetCustomSeconds: strconv.FormatInt(sub.QuotaResetCustomSecondsSnapshot, 10),
		LastResetTime: last, NextResetTime: next, ResetTimeZone: zone, ResetUTCOffsetSeconds: offset,
	}
	if sub.AmountTotal > 0 {
		total := strconv.FormatInt(sub.AmountTotal, 10)
		remaining := strconv.FormatInt(max(sub.AmountTotal-sub.AmountUsed, 0), 10)
		percentage := min(float64(sub.AmountUsed)/float64(sub.AmountTotal)*100, 100)
		result.Quota.Total, result.Quota.Remaining, result.Quota.UsedPercentage = &total, &remaining, &percentage
	}
	return result, nil
}

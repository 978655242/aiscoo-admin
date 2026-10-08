package controller

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func xcodeAPIError(c *gin.Context, status int, code, message string) {
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(status, gin.H{
		"success": false, "code": code, "message": message,
		"request_id": c.GetString(common.RequestIdKey),
	})
}

func xcodeAuthorizationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrXCodeAuthorizationConflict), errors.Is(err, model.ErrSubscriptionRequestConflict):
		xcodeAPIError(c, http.StatusConflict, "XCODE_AUTHORIZATION_CONFLICT", "Authorization changed or this action is not valid for its current state")
	case errors.Is(err, model.ErrXCodePlanConfigInvalid):
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_PLAN_CONFIG_INVALID", "The enabled plan must allow unlimited historical purchases and have a valid duration and quota configuration")
	case errors.Is(err, model.ErrXCodePaymentComplianceRequired):
		xcodeAPIError(c, http.StatusForbidden, "XCODE_PLAN_CONFIG_INVALID", "An administrator must confirm the existing payment compliance declaration first")
	default:
		logger.LogError(c, "XCode authorization unavailable: "+err.Error())
		xcodeAPIError(c, http.StatusServiceUnavailable, "XCODE_BACKEND_UNAVAILABLE", "Authorization is temporarily unavailable")
	}
}

func AdminGetXCodeAuthorization(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Invalid target user ID")
		return
	}
	state, err := model.GetXCodeAuthorizationState(userID)
	if err != nil {
		xcodeAuthorizationError(c, err)
		return
	}
	snapshot, err := xcodeAuthorizationSnapshot(state)
	if err != nil {
		xcodeAuthorizationError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, snapshot)
}

func AdminMutateXCodeAuthorization(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Invalid target user ID")
		return
	}
	var input dto.XCodeAuthorizationMutation
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Invalid authorization request")
		return
	}
	revision, err := strconv.ParseInt(input.ExpectedRevision, 10, 64)
	if err != nil || revision < 0 || strconv.FormatInt(revision, 10) != input.ExpectedRevision {
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "expected_revision must be a nonnegative decimal string")
		return
	}
	planID := 0
	switch input.Action {
	case "grant", "renew", "replace":
		planID, err = strconv.Atoi(input.PlanID)
		if err != nil || planID <= 0 || strconv.Itoa(planID) != input.PlanID || input.AdvanceResetTime != nil {
			xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "This action requires a valid plan_id decimal string")
			return
		}
	case "reset":
		if input.AdvanceResetTime == nil || input.PlanID != "" {
			xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "reset requires an explicit advance_reset_time boolean and no plan_id")
			return
		}
	case "revoke":
		if input.PlanID != "" || input.AdvanceResetTime != nil {
			xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "revoke does not accept plan or reset parameters")
			return
		}
	default:
		xcodeAPIError(c, http.StatusBadRequest, "XCODE_AUTH_REQUEST_INVALID", "Unknown authorization action")
		return
	}
	advance := input.AdvanceResetTime != nil && *input.AdvanceResetTime
	state, mutationErr := model.MutateXCodeAuthorization(userID, input.Action, revision, planID, advance)
	if state != nil && state.Audit != nil {
		params := map[string]any{
			"action": input.Action, "old_subscription_id": state.Audit.OldSubscriptionID,
			"new_subscription_id":    state.Authorization.SubscriptionId,
			"authorization_revision": strconv.FormatInt(state.Authorization.Revision, 10),
			"old_end_time":           state.Audit.OldEndTime, "new_end_time": state.Subscription.EndTime,
			"old_last_reset_time": state.Audit.OldLastResetTime, "new_last_reset_time": state.Subscription.LastResetTime,
			"old_next_reset_time": state.Audit.OldNextResetTime, "new_next_reset_time": state.Subscription.NextResetTime,
			"old_reset_generation": state.Audit.OldResetGeneration, "new_reset_generation": state.Subscription.QuotaResetGeneration,
			"advance_reset_time": advance,
		}
		recordManageAuditFor(c, userID, "xcode.authorization."+input.Action, params)
	}
	if mutationErr != nil {
		xcodeAuthorizationError(c, mutationErr)
		return
	}
	snapshot, err := xcodeAuthorizationSnapshot(state)
	if err != nil {
		xcodeAuthorizationError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, snapshot)
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

// The same authoritative snapshot serves administrator and later read-only clients.
func xcodeAuthorizationSnapshot(state *model.XCodeAuthorizationState) (*dto.XCodeAuthorizationSnapshot, error) {
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
	if sub.Id != state.Authorization.SubscriptionId || sub.UserId != state.Authorization.UserId ||
		sub.UsageScope != model.SubscriptionUsageScopeXCode || sub.Source != "admin" || sub.AmountTotal < 0 || sub.AmountUsed < 0 {
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

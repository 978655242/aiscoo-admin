package service

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"
)

// PreConsumeBilling 根据用户计费偏好创建 BillingSession 并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo != nil && relayInfo.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(
			relayInfo.QuotaClamp,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if preConsumedQuota < 0 {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("pre-consume quota cannot be negative: %d", preConsumedQuota),
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if relayInfo.Billing != nil && (relayInfo.ClientPurpose == model.ClientPurposeXCode || relayInfo.FundingScope == model.SubscriptionUsageScopeXCode) {
		session, ok := relayInfo.Billing.(*BillingSession)
		if !ok || !session.isManaged() {
			return xcodeFundingError(model.ErrSubscriptionFundingTargetInvalid)
		}
		if err := session.Reserve(preConsumedQuota); err != nil {
			return xcodeFundingError(err)
		}
		return nil
	}
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

func xcodeFundingError(err error) *types.NewAPIError {
	code := types.ErrorCode("XCODE_BACKEND_UNAVAILABLE")
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, model.ErrSubscriptionQuotaInsufficient):
		code, status = "XCODE_QUOTA_EXHAUSTED", http.StatusTooManyRequests
	case errors.Is(err, model.ErrXCodePlanUnassigned), errors.Is(err, model.ErrNoActiveSubscription):
		code, status = "XCODE_PLAN_UNASSIGNED", http.StatusForbidden
	case errors.Is(err, model.ErrXCodePlanExpired):
		code, status = "XCODE_PLAN_EXPIRED", http.StatusForbidden
	case errors.Is(err, model.ErrXCodePlanRevoked):
		code, status = "XCODE_PLAN_REVOKED", http.StatusForbidden
	case errors.Is(err, model.ErrXCodeAuthorizationConflict), errors.Is(err, model.ErrSubscriptionRequestConflict):
		code, status = "XCODE_AUTHORIZATION_CONFLICT", http.StatusConflict
	case errors.Is(err, model.ErrSubscriptionFundingTargetInvalid):
		code, status = "XCODE_CREDENTIAL_SCOPE_DENIED", http.StatusForbidden
	}
	return types.NewErrorWithStatusCode(err, code, status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
}

// ---------------------------------------------------------------------------
// SettleBilling — 后结算辅助函数
// ---------------------------------------------------------------------------

// SettleBilling 执行计费结算。如果 RelayInfo 上有 BillingSession 则通过 session 结算，
// 否则回退到旧的 PostConsumeQuota 路径（兼容按次计费等场景）。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) (err error) {
	if relayInfo == nil {
		return errors.New("relayInfo is nil")
	}
	fundingApplied := false
	defer func() {
		if err == nil {
			relayInfo.BillingSettlementFailure = nil
			return
		}
		code := "billing_settlement_failed"
		switch {
		case errors.Is(err, model.ErrSubscriptionQuotaInsufficient):
			code = "subscription_quota_insufficient"
		case errors.Is(err, model.ErrSubscriptionRequestConflict):
			code = "subscription_request_conflict"
		case errors.Is(err, model.ErrSubscriptionFundingTargetInvalid):
			code = "subscription_funding_target_invalid"
		}
		relayInfo.BillingSettlementFailure = &relaycommon.BillingSettlementFailure{
			ActualQuota: actualQuota, Code: code, FundingApplied: fundingApplied,
		}
		logger.LogError(ctx, fmt.Sprintf("billing settlement failed request=%s user=%d subscription=%d actual=%d funding_applied=%t code=%s: %v",
			relayInfo.RequestId, relayInfo.UserId, relayInfo.SubscriptionId, actualQuota, fundingApplied, code, err))
	}()
	if actualQuota < 0 {
		return errors.New("actual quota cannot be negative")
	}
	if relayInfo.ClientPurpose == model.ClientPurposeXCode || relayInfo.FundingScope == model.SubscriptionUsageScopeXCode {
		session, ok := relayInfo.Billing.(*BillingSession)
		if !ok || !session.isManaged() {
			return model.ErrSubscriptionFundingTargetInvalid
		}
	}
	if relayInfo.Billing != nil {
		preConsumed := relayInfo.Billing.GetPreConsumedQuota()
		delta, deltaErr := billingQuotaDelta(actualQuota, preConsumed)
		if deltaErr != nil {
			return deltaErr
		}

		if delta > 0 {
			logger.LogInfo(ctx, common.LogText("charging the difference after pre-consume: %s (actual %s, pre-consumed %s)",
				logger.FormatQuota(delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else if delta < 0 {
			logger.LogInfo(ctx, common.LogText("refunding the difference after pre-consume: %s (actual %s, pre-consumed %s)",
				logger.FormatQuota(-delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else {
			logger.LogInfo(ctx, common.LogText("pre-consumed quota matches the actual quota, funding still settles: %s (per-request billing)",
				logger.FormatQuota(actualQuota),
			))
		}

		err = relayInfo.Billing.Settle(actualQuota)
		if session, ok := relayInfo.Billing.(*BillingSession); ok {
			session.mu.Lock()
			fundingApplied = session.fundingSettled && session.fundingFinalQuota == actualQuota
			session.mu.Unlock()
		} else {
			fundingApplied = err == nil
		}
		if err != nil {
			return err
		}

		// 发送额度通知（订阅计费使用订阅剩余额度）
		if actualQuota != 0 {
			if relayInfo.BillingSource == BillingSourceSubscription {
				checkAndSendSubscriptionQuotaNotify(relayInfo)
			} else {
				checkAndSendQuotaNotify(relayInfo, actualQuota-preConsumed, preConsumed)
			}
		}
		return nil
	}

	quotaDelta, deltaErr := billingQuotaDelta(actualQuota, relayInfo.FinalPreConsumedQuota)
	if deltaErr != nil {
		return deltaErr
	}
	if quotaDelta != 0 || relayInfo.BillingSource == BillingSourceSubscription {
		result, consumeErr := postConsumeQuotaWithResult(relayInfo, quotaDelta, relayInfo.FinalPreConsumedQuota, true)
		fundingApplied = result.FundingApplied
		return consumeErr
	}
	return nil
}

package model

import (
	"errors"

	"gorm.io/gorm"
)

const (
	ClientPurposeGeneral   = "general"
	ClientPurposeDashboard = "dashboard"
	ClientPurposeXCode     = "xcode"

	SubscriptionUsageScopeGeneral = "general"
	SubscriptionUsageScopeXCode   = "xcode"
)

// XCodeAuthorization identifies the designated subscription, not a second balance.
// A cancelled or expired subscription remains referenced for history and settlement.
type XCodeAuthorization struct {
	Id             int   `json:"-"`
	UserId         int   `json:"user_id" gorm:"not null;uniqueIndex"`
	SubscriptionId int   `json:"subscription_id" gorm:"not null;index"`
	Revision       int64 `json:"revision" gorm:"type:bigint;not null"`
	UpdatedAt      int64 `json:"updated_at" gorm:"type:bigint"`
}

var (
	ErrXCodePlanUnassigned              = errors.New("XCode plan is unassigned")
	ErrXCodePlanExpired                 = errors.New("XCode plan is expired")
	ErrXCodePlanRevoked                 = errors.New("XCode plan is revoked")
	ErrXCodeAuthorizationConflict       = errors.New("XCode authorization changed")
	ErrNoActiveSubscription             = errors.New("no active subscription")
	ErrSubscriptionQuotaInsufficient    = errors.New("subscription quota insufficient")
	ErrSubscriptionFundingTargetInvalid = errors.New("invalid subscription funding target")
	ErrSubscriptionRequestConflict      = errors.New("subscription request conflicts with original funding")
)

// GetXCodeFundingTarget resolves the server-owned designation without selecting
// another subscription or consulting the user's native billing preference.
func GetXCodeFundingTarget(userID int) (SubscriptionFundingTarget, error) {
	if userID <= 0 {
		return SubscriptionFundingTarget{}, ErrSubscriptionFundingTargetInvalid
	}
	var authorization XCodeAuthorization
	if err := DB.Where("user_id = ?", userID).First(&authorization).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SubscriptionFundingTarget{}, ErrXCodePlanUnassigned
		}
		return SubscriptionFundingTarget{}, err
	}
	if authorization.SubscriptionId <= 0 || authorization.Revision <= 0 {
		return SubscriptionFundingTarget{}, ErrXCodePlanRevoked
	}
	var subscription UserSubscription
	if err := DB.First(&subscription, authorization.SubscriptionId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SubscriptionFundingTarget{}, ErrXCodePlanRevoked
		}
		return SubscriptionFundingTarget{}, err
	}
	now, err := getDBTimestamp(DB)
	if err != nil {
		return SubscriptionFundingTarget{}, err
	}
	if err := validateXCodeFundingSubscription(&subscription, userID, now); err != nil {
		return SubscriptionFundingTarget{}, err
	}
	return SubscriptionFundingTarget{
		Scope:                 SubscriptionUsageScopeXCode,
		SubscriptionID:        subscription.Id,
		AuthorizationRevision: authorization.Revision,
	}, nil
}

func validateXCodeFundingSubscription(subscription *UserSubscription, userID int, now int64) error {
	if subscription.UserId != userID || subscription.UsageScope != SubscriptionUsageScopeXCode {
		return ErrXCodePlanRevoked
	}
	if subscription.Status == "expired" {
		return ErrXCodePlanExpired
	}
	if subscription.Status != "active" {
		return ErrXCodePlanRevoked
	}
	if subscription.EndTime <= now {
		return ErrXCodePlanExpired
	}
	if subscription.StartTime > now {
		return ErrXCodePlanUnassigned
	}
	return nil
}

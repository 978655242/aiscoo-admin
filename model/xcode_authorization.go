package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"

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
	ErrXCodePlanConfigInvalid           = errors.New("XCode plan configuration is invalid")
	ErrXCodePaymentComplianceRequired   = errors.New("payment compliance confirmation is required")
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

// XCodeAuthorizationState is the locked, persisted state, not a virtual balance.
type XCodeAuthorizationState struct {
	Authorization *XCodeAuthorization
	Subscription  *UserSubscription
	PlanTitle     string
	Now           int64
	Audit         *XCodeAuthorizationChange
}

type XCodeAuthorizationChange struct {
	OldSubscriptionID  int
	OldEndTime         int64
	OldResetGeneration int64
	OldLastResetTime   int64
	OldNextResetTime   int64
}

func lockXCodeAuthorizationTx(tx *gorm.DB, userID int) (*XCodeAuthorization, error) {
	var user User
	if err := lockForUpdate(tx).Select("id").First(&user, userID).Error; err != nil {
		return nil, err
	}
	var authorization XCodeAuthorization
	err := lockForUpdate(tx).Where("user_id = ?", userID).First(&authorization).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if authorization.Revision <= 0 || authorization.SubscriptionId <= 0 {
		return nil, ErrXCodeAuthorizationConflict
	}
	return &authorization, nil
}

func loadXCodeAuthorizationStateTx(tx *gorm.DB, userID int) (*XCodeAuthorizationState, error) {
	authorization, err := lockXCodeAuthorizationTx(tx, userID)
	if err != nil {
		return nil, err
	}
	state := &XCodeAuthorizationState{Authorization: authorization}
	if authorization != nil {
		var sub UserSubscription
		if err := lockForUpdate(tx).First(&sub, authorization.SubscriptionId).Error; err != nil {
			return nil, err
		}
		if sub.UserId != userID || sub.UsageScope != SubscriptionUsageScopeXCode {
			return nil, ErrSubscriptionFundingTargetInvalid
		}
		state.Subscription = &sub
	}
	state.Now, err = getDBTimestamp(tx)
	return state, err
}

func completeXCodeAuthorizationStateTx(tx *gorm.DB, state *XCodeAuthorizationState, applyReset bool) error {
	sub := state.Subscription
	if sub == nil {
		return nil
	}
	if applyReset && sub.Status == "active" && sub.StartTime <= state.Now && sub.EndTime > state.Now {
		if err := maybeResetUserSubscriptionWithPlanTx(tx, sub, nil, state.Now); err != nil {
			return err
		}
	}
	var plan SubscriptionPlan
	err := tx.Select("id", "title").First(&plan, sub.PlanId).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	state.PlanTitle = plan.Title
	return nil
}

// GetXCodeAuthorizationState applies a due reset under the designation's instance lock.
func GetXCodeAuthorizationState(userID int) (*XCodeAuthorizationState, error) {
	if userID <= 0 {
		return nil, ErrSubscriptionFundingTargetInvalid
	}
	var state *XCodeAuthorizationState
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		state, err = loadXCodeAuthorizationStateTx(tx, userID)
		if err != nil {
			return err
		}
		return completeXCodeAuthorizationStateTx(tx, state, true)
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

func validateXCodePlan(plan *SubscriptionPlan, start int64) error {
	if plan == nil || plan.Id <= 0 || !plan.Enabled || plan.MaxPurchasePerUser != 0 || plan.TotalAmount < 0 ||
		plan.CustomSeconds < 0 || plan.QuotaResetCustomSeconds < 0 {
		return ErrXCodePlanConfigInvalid
	}
	switch plan.DurationUnit {
	case SubscriptionDurationYear:
		if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/1000/(366*86400) {
			return ErrXCodePlanConfigInvalid
		}
	case SubscriptionDurationMonth:
		if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/1000/(31*86400) {
			return ErrXCodePlanConfigInvalid
		}
	case SubscriptionDurationDay:
		if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/int64(24*time.Hour) {
			return ErrXCodePlanConfigInvalid
		}
	case SubscriptionDurationHour:
		if plan.DurationValue <= 0 || int64(plan.DurationValue) > math.MaxInt64/int64(time.Hour) {
			return ErrXCodePlanConfigInvalid
		}
	case SubscriptionDurationCustom:
		if plan.CustomSeconds <= 0 || plan.CustomSeconds > math.MaxInt64/int64(time.Second) {
			return ErrXCodePlanConfigInvalid
		}
	default:
		return ErrXCodePlanConfigInvalid
	}
	if plan.QuotaResetPeriod != NormalizeResetPeriod(plan.QuotaResetPeriod) ||
		(plan.QuotaResetPeriod == SubscriptionResetCustom &&
			(plan.QuotaResetCustomSeconds <= 0 || plan.QuotaResetCustomSeconds > math.MaxInt64/int64(time.Second))) {
		return ErrXCodePlanConfigInvalid
	}
	end, err := calcPlanEndTime(time.Unix(start, 0), plan)
	if err != nil || end <= start || end > math.MaxInt64/1000 {
		return ErrXCodePlanConfigInvalid
	}
	return nil
}

func advanceXCodeAuthorizationRevisionTx(tx *gorm.DB, authorization *XCodeAuthorization, now int64) error {
	if authorization == nil || authorization.Revision <= 0 || authorization.Revision == math.MaxInt64 {
		return ErrXCodeAuthorizationConflict
	}
	result := tx.Model(&XCodeAuthorization{}).Where("id = ? AND revision = ?", authorization.Id, authorization.Revision).
		Updates(map[string]any{"revision": authorization.Revision + 1, "subscription_id": authorization.SubscriptionId, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrXCodeAuthorizationConflict
	}
	authorization.Revision++
	authorization.UpdatedAt = now
	return nil
}

func cancelXCodeSubscriptionTx(tx *gorm.DB, sub *UserSubscription, now int64) (bool, error) {
	if err := maybeResetUserSubscriptionWithPlanTx(tx, sub, nil, now); err != nil {
		return false, err
	}
	if err := tx.Model(sub).Updates(map[string]any{"status": "cancelled", "end_time": now, "updated_at": now}).Error; err != nil {
		return false, err
	}
	sub.Status, sub.EndTime = "cancelled", now
	target, err := downgradeUserGroupForSubscriptionTx(tx, sub, now)
	return target != "", err
}

// MutateXCodeAuthorization serializes administrator transitions by user and revision.
func MutateXCodeAuthorization(userID int, action string, expectedRevision int64, planID int, advanceResetTime bool) (*XCodeAuthorizationState, error) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return nil, ErrXCodePaymentComplianceRequired
	}
	if userID <= 0 || expectedRevision < 0 || expectedRevision == math.MaxInt64 {
		return nil, ErrXCodeAuthorizationConflict
	}
	var state *XCodeAuthorizationState
	groupChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		state, err = loadXCodeAuthorizationStateTx(tx, userID)
		if err != nil {
			return err
		}
		revision := int64(0)
		if state.Authorization != nil {
			revision = state.Authorization.Revision
		}
		if revision != expectedRevision {
			return ErrXCodeAuthorizationConflict
		}
		sub := state.Subscription
		state.Audit = &XCodeAuthorizationChange{}
		if sub != nil {
			state.Audit.OldSubscriptionID = sub.Id
			state.Audit.OldEndTime = sub.EndTime
			state.Audit.OldResetGeneration = sub.QuotaResetGeneration
			state.Audit.OldLastResetTime = sub.LastResetTime
			state.Audit.OldNextResetTime = sub.NextResetTime
		}
		active := sub != nil && sub.Status == "active" && sub.StartTime <= state.Now && sub.EndTime > state.Now
		create := false
		switch action {
		case "grant":
			if active {
				return ErrXCodeAuthorizationConflict
			}
			create = true
		case "renew":
			if sub == nil || sub.PlanId != planID || (sub.Status != "active" && sub.Status != "expired") {
				return ErrXCodeAuthorizationConflict
			}
			if !active {
				if sub.EndTime > state.Now {
					return ErrXCodeAuthorizationConflict
				}
				create = true
			}
		case "replace":
			if !active || sub.PlanId == planID {
				return ErrXCodeAuthorizationConflict
			}
			create = true
		case "revoke":
			if sub == nil || (sub.Status != "active" && sub.Status != "expired") {
				return ErrXCodeAuthorizationConflict
			}
		case "reset":
			if !active {
				return ErrXCodeAuthorizationConflict
			}
		default:
			return ErrXCodeAuthorizationConflict
		}
		var plan SubscriptionPlan
		if action == "grant" || action == "renew" || action == "replace" {
			if planID <= 0 {
				return ErrXCodePlanConfigInvalid
			}
			if err := lockForUpdate(tx).First(&plan, planID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrXCodePlanConfigInvalid
				}
				return err
			}
			start := state.Now
			if action == "renew" && active {
				start = sub.EndTime
			}
			if err := validateXCodePlan(&plan, start); err != nil {
				return err
			}
		}
		if action == "replace" || action == "revoke" {
			groupChanged, err = cancelXCodeSubscriptionTx(tx, sub, state.Now)
			if err != nil {
				return err
			}
		}
		if create {
			// Expired upgrades must be unwound before the new instance snapshots the user's group.
			if sub != nil && sub.Status != "cancelled" {
				target, err := downgradeUserGroupForSubscriptionTx(tx, sub, state.Now)
				if err != nil {
					return err
				}
				groupChanged = groupChanged || target != ""
			}
			state.Subscription, err = CreateUserSubscriptionFromPlanTx(tx, userID, &plan, "admin", SubscriptionUsageScopeXCode)
			if err != nil {
				return err
			}
			state.Now = state.Subscription.StartTime
			groupChanged = groupChanged || state.Subscription.PrevUserGroup != ""
		} else if action == "renew" {
			// Reset any elapsed original period before extending the lifetime.
			if err := maybeResetUserSubscriptionWithPlanTx(tx, sub, nil, state.Now); err != nil {
				return err
			}
			end, err := calcPlanEndTime(time.Unix(sub.EndTime, 0), &plan)
			if err != nil {
				return err
			}
			sub.EndTime = end
			resetPlan, err := subscriptionResetPlan(sub, nil)
			if err != nil {
				return err
			}
			if sub.NextResetTime == 0 {
				anchor := sub.LastResetTime
				if anchor <= 0 {
					anchor = sub.StartTime
				}
				sub.NextResetTime = calcNextResetTime(time.Unix(anchor, 0), resetPlan, end)
			}
			if err := tx.Save(sub).Error; err != nil {
				return err
			}
		} else if action == "reset" {
			if err := resetUserSubscriptionTx(tx, sub, nil, state.Now, advanceResetTime); err != nil {
				return err
			}
		}
		if state.Authorization == nil {
			state.Authorization = &XCodeAuthorization{UserId: userID, SubscriptionId: state.Subscription.Id, Revision: 1, UpdatedAt: state.Now}
			if err := tx.Create(state.Authorization).Error; err != nil {
				return err
			}
		} else {
			state.Authorization.SubscriptionId = state.Subscription.Id
			if err := advanceXCodeAuthorizationRevisionTx(tx, state.Authorization, state.Now); err != nil {
				return err
			}
		}
		return completeXCodeAuthorizationStateTx(tx, state, action != "reset")
	})
	if err != nil {
		// SQLite readers can race the sole writer; the losing transition never commits.
		if strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "database table is locked") {
			return nil, ErrXCodeAuthorizationConflict
		}
		return nil, err
	}
	if groupChanged {
		if err := RefreshUserGroupCache(userID); err != nil {
			return state, fmt.Errorf("XCode authorization committed but group cache refresh failed: %w", err)
		}
	}
	return state, nil
}

func lockAdminSubscriptionTx(tx *gorm.DB, hint *UserSubscription) (*UserSubscription, *XCodeAuthorization, int64, error) {
	var authorization *XCodeAuthorization
	var err error
	if hint.UsageScope == SubscriptionUsageScopeXCode {
		authorization, err = lockXCodeAuthorizationTx(tx, hint.UserId)
		if err != nil {
			return nil, nil, 0, err
		}
	} else {
		var user User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", hint.UserId).Find(&user).Error; err != nil {
			return nil, nil, 0, err
		}
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).First(&sub, hint.Id).Error; err != nil {
		return nil, nil, 0, err
	}
	if sub.UserId != hint.UserId || sub.UsageScope != hint.UsageScope {
		return nil, nil, 0, ErrXCodeAuthorizationConflict
	}
	now := time.Now().Unix()
	if sub.UsageScope == SubscriptionUsageScopeXCode {
		now, err = getDBTimestamp(tx)
	}
	return &sub, authorization, now, err
}

func preventReferencedXCodeSubscriptionDeleteTx(tx *gorm.DB, sub *UserSubscription) error {
	var count int64
	if err := tx.Model(&XCodeAuthorization{}).Where("subscription_id = ?", sub.Id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrXCodeAuthorizationConflict
	}
	if err := tx.Model(&SubscriptionPreConsumeRecord{}).Where("user_subscription_id = ?", sub.Id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrXCodeAuthorizationConflict
	}
	var tasks []Task
	if err := tx.Select("private_data").Where("user_id = ? AND (status NOT IN ? OR status IS NULL)",
		sub.UserId, []TaskStatus{TaskStatusSuccess, TaskStatusFailure}).Find(&tasks).Error; err != nil {
		return err
	}
	for _, task := range tasks {
		if task.PrivateData.SubscriptionId == sub.Id {
			return ErrXCodeAuthorizationConflict
		}
	}
	return nil
}

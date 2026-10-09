package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/samber/hot"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Subscription duration units
const (
	SubscriptionDurationYear   = "year"
	SubscriptionDurationMonth  = "month"
	SubscriptionDurationDay    = "day"
	SubscriptionDurationHour   = "hour"
	SubscriptionDurationCustom = "custom"
)

// Subscription quota reset period
const (
	SubscriptionResetNever   = "never"
	SubscriptionResetDaily   = "daily"
	SubscriptionResetWeekly  = "weekly"
	SubscriptionResetMonthly = "monthly"
	SubscriptionResetCustom  = "custom"
)

var (
	ErrSubscriptionOrderNotFound      = errors.New("subscription order not found")
	ErrSubscriptionOrderStatusInvalid = errors.New("subscription order status invalid")
)

const (
	subscriptionPlanCacheNamespace     = "new-api:subscription_plan:v1"
	subscriptionPlanInfoCacheNamespace = "new-api:subscription_plan_info:v1"
)

var (
	subscriptionPlanCacheOnce     sync.Once
	subscriptionPlanInfoCacheOnce sync.Once

	subscriptionPlanCache     *cachex.HybridCache[SubscriptionPlan]
	subscriptionPlanInfoCache *cachex.HybridCache[SubscriptionPlanInfo]
)

func subscriptionPlanCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_TTL", 300)
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanInfoCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_TTL", 120)
	if ttlSeconds <= 0 {
		ttlSeconds = 120
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_CAP", 5000)
	if capacity <= 0 {
		capacity = 5000
	}
	return capacity
}

func subscriptionPlanInfoCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_CAP", 10000)
	if capacity <= 0 {
		capacity = 10000
	}
	return capacity
}

func getSubscriptionPlanCache() *cachex.HybridCache[SubscriptionPlan] {
	subscriptionPlanCacheOnce.Do(func() {
		ttl := subscriptionPlanCacheTTL()
		subscriptionPlanCache = cachex.NewHybridCache[SubscriptionPlan](cachex.HybridCacheConfig[SubscriptionPlan]{
			Namespace: cachex.Namespace(subscriptionPlanCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlan]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlan] {
				return hot.NewHotCache[string, SubscriptionPlan](hot.LRU, subscriptionPlanCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanCache
}

func getSubscriptionPlanInfoCache() *cachex.HybridCache[SubscriptionPlanInfo] {
	subscriptionPlanInfoCacheOnce.Do(func() {
		ttl := subscriptionPlanInfoCacheTTL()
		subscriptionPlanInfoCache = cachex.NewHybridCache[SubscriptionPlanInfo](cachex.HybridCacheConfig[SubscriptionPlanInfo]{
			Namespace: cachex.Namespace(subscriptionPlanInfoCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlanInfo]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlanInfo] {
				return hot.NewHotCache[string, SubscriptionPlanInfo](hot.LRU, subscriptionPlanInfoCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanInfoCache
}

func subscriptionPlanCacheKey(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func InvalidateSubscriptionPlanCache(planId int) {
	if planId <= 0 {
		return
	}
	cache := getSubscriptionPlanCache()
	_, _ = cache.DeleteMany([]string{subscriptionPlanCacheKey(planId)})
	infoCache := getSubscriptionPlanInfoCache()
	_ = infoCache.Purge()
}

// Subscription plan
type SubscriptionPlan struct {
	Id int `json:"id"`

	Title    string `json:"title" gorm:"type:varchar(128);not null"`
	Subtitle string `json:"subtitle" gorm:"type:varchar(255);default:''"`

	// Display money amount (follow existing code style: float64 for money)
	PriceAmount float64 `json:"price_amount" gorm:"type:decimal(10,6);not null;default:0"`
	Currency    string  `json:"currency" gorm:"type:varchar(8);not null;default:'USD'"`

	DurationUnit  string `json:"duration_unit" gorm:"type:varchar(16);not null;default:'month'"`
	DurationValue int    `json:"duration_value" gorm:"type:int;not null;default:1"`
	CustomSeconds int64  `json:"custom_seconds" gorm:"type:bigint;not null;default:0"`

	Enabled   bool `json:"enabled" gorm:"default:true"`
	SortOrder int  `json:"sort_order" gorm:"type:int;default:0"`

	AllowBalancePay *bool `json:"allow_balance_pay"`

	// Allow falling back to wallet balance after subscription quota is exhausted (empty = true)
	AllowWalletOverflow *bool `json:"allow_wallet_overflow"`

	StripePriceId         string `json:"stripe_price_id" gorm:"type:varchar(128);default:''"`
	CreemProductId        string `json:"creem_product_id" gorm:"type:varchar(128);default:''"`
	WaffoPancakeProductId string `json:"waffo_pancake_product_id" gorm:"type:varchar(128);default:''"`

	// Max purchases per user (0 = unlimited)
	MaxPurchasePerUser int `json:"max_purchase_per_user" gorm:"type:int;default:0"`

	// Upgrade user group after purchase (empty = no change)
	UpgradeGroup string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`

	// Downgrade user group on expiry (empty = revert to the group held before purchase)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Total quota (amount in quota units, 0 = unlimited)
	TotalAmount int64 `json:"total_amount" gorm:"type:bigint;not null;default:0"`

	// Quota reset period for plan
	QuotaResetPeriod        string `json:"quota_reset_period" gorm:"type:varchar(16);default:'never'"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds" gorm:"type:bigint;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (p *SubscriptionPlan) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

func (p *SubscriptionPlan) BeforeUpdate(tx *gorm.DB) error {
	p.UpdatedAt = common.GetTimestamp()
	return nil
}

func (p *SubscriptionPlan) NormalizeDefaults() {
	if p.AllowBalancePay == nil {
		p.AllowBalancePay = common.GetPointer(true)
	}
	if p.AllowWalletOverflow == nil {
		p.AllowWalletOverflow = common.GetPointer(true)
	}
}

// Subscription order (payment -> webhook -> create UserSubscription)
type SubscriptionOrder struct {
	Id     int     `json:"id"`
	UserId int     `json:"user_id" gorm:"index"`
	PlanId int     `json:"plan_id" gorm:"index"`
	Money  float64 `json:"money"`

	TradeNo         string `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	Status          string `json:"status"`
	CreateTime      int64  `json:"create_time"`
	CompleteTime    int64  `json:"complete_time"`

	ProviderPayload string `json:"provider_payload" gorm:"type:text"`
}

func (o *SubscriptionOrder) Insert() error {
	if o.CreateTime == 0 {
		o.CreateTime = common.GetTimestamp()
	}
	return DB.Create(o).Error
}

func (o *SubscriptionOrder) Update() error {
	return DB.Save(o).Error
}

func GetSubscriptionOrderByTradeNo(tradeNo string) *SubscriptionOrder {
	if tradeNo == "" {
		return nil
	}
	var order SubscriptionOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		return nil
	}
	return &order
}

// User subscription instance
type UserSubscription struct {
	Id     int `json:"id"`
	UserId int `json:"user_id" gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId int `json:"plan_id" gorm:"index"`

	AmountTotal int64 `json:"amount_total" gorm:"type:bigint;not null;default:0"`
	AmountUsed  int64 `json:"amount_used" gorm:"type:bigint;not null;default:0"`

	StartTime int64  `json:"start_time" gorm:"bigint"`
	EndTime   int64  `json:"end_time" gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status    string `json:"status" gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"` // active/expired/cancelled

	Source                          string `json:"source" gorm:"type:varchar(32);default:'order'"` // order/admin
	UsageScope                      string `json:"usage_scope" gorm:"type:varchar(16);index"`
	QuotaResetPeriodSnapshot        string `json:"-" gorm:"type:varchar(16)"`
	QuotaResetCustomSecondsSnapshot int64  `json:"-" gorm:"type:bigint"`
	QuotaResetGeneration            int64  `json:"-" gorm:"type:bigint"`

	LastResetTime int64 `json:"last_reset_time" gorm:"type:bigint;default:0"`
	NextResetTime int64 `json:"next_reset_time" gorm:"type:bigint;default:0;index"`

	UpgradeGroup  string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`
	PrevUserGroup string `json:"prev_user_group" gorm:"type:varchar(64);default:''"`

	// Downgrade target group on expiry (snapshot from plan; empty = revert to PrevUserGroup)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Whether wallet fallback is allowed after this subscription's quota is exhausted (snapshot from plan)
	AllowWalletOverflow bool `json:"allow_wallet_overflow"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (s *UserSubscription) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	if s.UsageScope == SubscriptionUsageScopeXCode && s.QuotaResetGeneration == 0 {
		s.QuotaResetGeneration = 1
	}
	s.CreatedAt = now
	s.UpdatedAt = now
	return nil
}

func (s *UserSubscription) BeforeUpdate(tx *gorm.DB) error {
	s.UpdatedAt = common.GetTimestamp()
	return nil
}

type SubscriptionSummary struct {
	Subscription *UserSubscription `json:"subscription"`
}

type SubscriptionResetResult struct {
	PlanId           int    `json:"plan_id"`
	MatchedCount     int    `json:"matched_count"`
	ResetCount       int    `json:"reset_count"`
	UserCount        int    `json:"user_count"`
	AdvanceResetTime bool   `json:"advance_reset_time"`
	PlanTitle        string `json:"-"`
	AffectedUserIds  []int  `json:"-"`
}

func calcPlanEndTime(start time.Time, plan *SubscriptionPlan) (int64, error) {
	if plan == nil {
		return 0, errors.New("plan is nil")
	}
	if plan.DurationValue <= 0 && plan.DurationUnit != SubscriptionDurationCustom {
		return 0, errors.New("duration_value must be > 0")
	}
	switch plan.DurationUnit {
	case SubscriptionDurationYear:
		return start.AddDate(plan.DurationValue, 0, 0).Unix(), nil
	case SubscriptionDurationMonth:
		return start.AddDate(0, plan.DurationValue, 0).Unix(), nil
	case SubscriptionDurationDay:
		return start.Add(time.Duration(plan.DurationValue) * 24 * time.Hour).Unix(), nil
	case SubscriptionDurationHour:
		return start.Add(time.Duration(plan.DurationValue) * time.Hour).Unix(), nil
	case SubscriptionDurationCustom:
		if plan.CustomSeconds <= 0 {
			return 0, errors.New("custom_seconds must be > 0")
		}
		return start.Add(time.Duration(plan.CustomSeconds) * time.Second).Unix(), nil
	default:
		return 0, fmt.Errorf("invalid duration_unit: %s", plan.DurationUnit)
	}
}

func NormalizeResetPeriod(period string) string {
	switch strings.TrimSpace(period) {
	case SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly, SubscriptionResetCustom:
		return strings.TrimSpace(period)
	default:
		return SubscriptionResetNever
	}
}

func calcNextResetTime(base time.Time, plan *SubscriptionPlan, endUnix int64) int64 {
	if plan == nil {
		return 0
	}
	period := NormalizeResetPeriod(plan.QuotaResetPeriod)
	if period == SubscriptionResetNever {
		return 0
	}
	var next time.Time
	switch period {
	case SubscriptionResetDaily:
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, 1)
	case SubscriptionResetWeekly:
		// Align to next Monday 00:00
		weekday := int(base.Weekday()) // Sunday=0
		// Convert to Monday=1..Sunday=7
		if weekday == 0 {
			weekday = 7
		}
		daysUntil := 8 - weekday
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, daysUntil)
	case SubscriptionResetMonthly:
		// Align to first day of next month 00:00
		next = time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, base.Location()).
			AddDate(0, 1, 0)
	case SubscriptionResetCustom:
		if plan.QuotaResetCustomSeconds <= 0 {
			return 0
		}
		next = base.Add(time.Duration(plan.QuotaResetCustomSeconds) * time.Second)
	default:
		return 0
	}
	if endUnix > 0 && next.Unix() > endUnix {
		return 0
	}
	return next.Unix()
}

func GetSubscriptionPlanById(id int) (*SubscriptionPlan, error) {
	return getSubscriptionPlanByIdTx(nil, id)
}

func getSubscriptionPlanByIdTx(tx *gorm.DB, id int) (*SubscriptionPlan, error) {
	if id <= 0 {
		return nil, errors.New("invalid plan id")
	}
	key := subscriptionPlanCacheKey(id)
	if key != "" {
		if cached, found, err := getSubscriptionPlanCache().Get(key); err == nil && found {
			cached.NormalizeDefaults()
			return &cached, nil
		}
	}
	var plan SubscriptionPlan
	query := DB
	if tx != nil {
		query = tx
	}
	if err := query.Where("id = ?", id).First(&plan).Error; err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	_ = getSubscriptionPlanCache().SetWithTTL(key, plan, subscriptionPlanCacheTTL())
	return &plan, nil
}

func CountUserSubscriptionsByPlan(userId int, planId int) (int64, error) {
	if userId <= 0 || planId <= 0 {
		return 0, errors.New("invalid userId or planId")
	}
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func getUserGroupByIdTx(tx *gorm.DB, userId int) (string, error) {
	if userId <= 0 {
		return "", errors.New("invalid userId")
	}
	if tx == nil {
		tx = DB
	}
	var group string
	if err := lockForUpdate(tx).Model(&User{}).Where("id = ?", userId).Select(commonGroupCol).Find(&group).Error; err != nil {
		return "", err
	}
	return group, nil
}

func downgradeUserGroupForSubscriptionTx(tx *gorm.DB, sub *UserSubscription, now int64) (string, error) {
	if tx == nil || sub == nil {
		return "", errors.New("invalid downgrade args")
	}
	downgradeGroup := strings.TrimSpace(sub.DowngradeGroup)
	upgradeGroup := strings.TrimSpace(sub.UpgradeGroup)
	// Nothing to do if neither an explicit downgrade target nor an upgrade snapshot exists.
	if downgradeGroup == "" && upgradeGroup == "" {
		return "", nil
	}
	currentGroup, err := getUserGroupByIdTx(tx, sub.UserId)
	if err != nil {
		return "", err
	}
	// If another active upgraded subscription exists, keep the current group.
	var activeSub UserSubscription
	activeQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND id <> ? AND upgrade_group <> ''",
		sub.UserId, "active", now, sub.Id).
		Order("end_time desc, id desc").
		Limit(1).
		Find(&activeSub)
	if activeQuery.Error != nil {
		return "", activeQuery.Error
	}
	if activeQuery.RowsAffected > 0 {
		return "", nil
	}
	// Determine the downgrade target: an explicit downgrade group takes precedence,
	// otherwise revert to the group held before purchase (legacy behavior).
	target := downgradeGroup
	if target == "" {
		// Legacy behavior: only revert when the subscription actually elevated the user.
		if currentGroup != upgradeGroup {
			return "", nil
		}
		target = strings.TrimSpace(sub.PrevUserGroup)
	}
	if target == "" || target == currentGroup {
		return "", nil
	}
	if err := tx.Model(&User{}).Where("id = ?", sub.UserId).
		Update("group", target).Error; err != nil {
		return "", err
	}
	return target, nil
}

// CreateUserSubscriptionFromPlanTx creates the complete funding-domain snapshot at insertion.
func CreateUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string, usageScope string) (*UserSubscription, error) {
	if tx == nil {
		return nil, errors.New("tx is nil")
	}
	if plan == nil || plan.Id == 0 {
		return nil, errors.New("invalid plan")
	}
	if userId <= 0 {
		return nil, errors.New("invalid user id")
	}
	if usageScope != SubscriptionUsageScopeGeneral && usageScope != SubscriptionUsageScopeXCode {
		return nil, ErrSubscriptionFundingTargetInvalid
	}
	if plan.MaxPurchasePerUser > 0 {
		var count int64
		if err := tx.Model(&UserSubscription{}).
			Where("user_id = ? AND plan_id = ?", userId, plan.Id).
			Count(&count).Error; err != nil {
			return nil, err
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return nil, errors.New("已达到该套餐购买上限")
		}
	}
	nowUnix, err := getDBTimestamp(tx)
	if err != nil {
		if usageScope == SubscriptionUsageScopeXCode {
			return nil, err
		}
		nowUnix = common.GetTimestamp()
	}
	if usageScope == SubscriptionUsageScopeXCode {
		if source != "admin" {
			return nil, ErrSubscriptionFundingTargetInvalid
		}
		if err := validateXCodePlan(plan, nowUnix); err != nil {
			return nil, err
		}
	}
	now := time.Unix(nowUnix, 0)
	endUnix, err := calcPlanEndTime(now, plan)
	if err != nil {
		return nil, err
	}
	resetBase := now
	nextReset := calcNextResetTime(resetBase, plan, endUnix)
	lastReset := int64(0)
	if nextReset > 0 {
		lastReset = now.Unix()
	}
	if usageScope == SubscriptionUsageScopeXCode && plan.QuotaResetPeriod != SubscriptionResetNever {
		lastReset = nowUnix
	}
	upgradeGroup := strings.TrimSpace(plan.UpgradeGroup)
	prevGroup := ""
	if upgradeGroup != "" {
		currentGroup, err := getUserGroupByIdTx(tx, userId)
		if err != nil {
			return nil, err
		}
		if currentGroup != upgradeGroup {
			prevGroup = currentGroup
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", upgradeGroup).Error; err != nil {
				return nil, err
			}
		}
	}
	allowWalletOverflow := true
	if plan.AllowWalletOverflow != nil {
		allowWalletOverflow = *plan.AllowWalletOverflow
	}
	if usageScope == SubscriptionUsageScopeXCode {
		allowWalletOverflow = false
	}
	sub := &UserSubscription{
		UserId:              userId,
		PlanId:              plan.Id,
		AmountTotal:         plan.TotalAmount,
		AmountUsed:          0,
		StartTime:           now.Unix(),
		EndTime:             endUnix,
		Status:              "active",
		Source:              source,
		UsageScope:          usageScope,
		LastResetTime:       lastReset,
		NextResetTime:       nextReset,
		UpgradeGroup:        upgradeGroup,
		PrevUserGroup:       prevGroup,
		DowngradeGroup:      strings.TrimSpace(plan.DowngradeGroup),
		AllowWalletOverflow: allowWalletOverflow,
		CreatedAt:           common.GetTimestamp(),
		UpdatedAt:           common.GetTimestamp(),
	}
	if usageScope == SubscriptionUsageScopeXCode {
		sub.QuotaResetPeriodSnapshot = plan.QuotaResetPeriod
		sub.QuotaResetCustomSecondsSnapshot = plan.QuotaResetCustomSeconds
		sub.QuotaResetGeneration = 1
	}
	if err := tx.Create(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

func refreshSubscriptionUserGroupCache(userId int, operation string) {
	if err := RefreshUserGroupCache(userId); err != nil {
		common.SysError(fmt.Sprintf("failed to refresh user group cache after %s for user %d: %v", operation, userId, err))
	}
}

// Complete a subscription order (idempotent). Creates a UserSubscription snapshot from the plan.
// expectedPaymentProvider guards against cross-gateway callback attacks (empty skips the check).
// actualPaymentMethod updates the order's PaymentMethod to reflect the real payment type used (empty skips update).
func CompleteSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	var logUserId int
	var logPlanTitle string
	var logMoney float64
	var logPaymentMethod string
	var upgradeGroup string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status == common.TopUpStatusSuccess {
			return nil
		}
		if order.Status != common.TopUpStatusPending {
			return ErrSubscriptionOrderStatusInvalid
		}
		plan, err := GetSubscriptionPlanById(order.PlanId)
		if err != nil {
			return err
		}
		if !plan.Enabled {
			// still allow completion for already purchased orders
		}
		// 锁定用户行：并发完成同一用户的不同订单（包括多实例部署下）时，
		// 使 CreateUserSubscriptionFromPlanTx 的 MaxPurchasePerUser 检查按用户串行。
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", order.UserId).First(&userRow).Error; err != nil {
			return err
		}
		subscription, err := CreateUserSubscriptionFromPlanTx(tx, order.UserId, plan, "order", SubscriptionUsageScopeGeneral)
		if err != nil {
			return err
		}
		if subscription.PrevUserGroup != "" {
			upgradeGroup = strings.TrimSpace(subscription.UpgradeGroup)
		}
		if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
			return err
		}
		order.Status = common.TopUpStatusSuccess
		order.CompleteTime = common.GetTimestamp()
		if providerPayload != "" {
			order.ProviderPayload = providerPayload
		}
		if actualPaymentMethod != "" && order.PaymentMethod != actualPaymentMethod {
			order.PaymentMethod = actualPaymentMethod
		}
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		logUserId = order.UserId
		logPlanTitle = plan.Title
		logMoney = order.Money
		logPaymentMethod = order.PaymentMethod
		return nil
	})
	if err != nil {
		return err
	}
	if upgradeGroup != "" && logUserId > 0 {
		refreshSubscriptionUserGroupCache(logUserId, "subscription payment completion")
	}
	if logUserId > 0 {
		msg := fmt.Sprintf("订阅购买成功，套餐: %s，支付金额: %.2f，支付方式: %s", logPlanTitle, logMoney, logPaymentMethod)
		RecordLog(logUserId, LogTypeTopup, msg)
	}
	return nil
}

func upsertSubscriptionTopUpTx(tx *gorm.DB, order *SubscriptionOrder) error {
	if tx == nil || order == nil {
		return errors.New("invalid subscription order")
	}
	now := common.GetTimestamp()
	var topup TopUp
	if err := tx.Where("trade_no = ?", order.TradeNo).First(&topup).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			topup = TopUp{
				UserId:        order.UserId,
				Amount:        0,
				Money:         order.Money,
				TradeNo:       order.TradeNo,
				PaymentMethod: order.PaymentMethod,
				CreateTime:    order.CreateTime,
				CompleteTime:  now,
				Status:        common.TopUpStatusSuccess,
			}
			return tx.Create(&topup).Error
		}
		return err
	}
	topup.Money = order.Money
	if topup.PaymentMethod == "" {
		topup.PaymentMethod = order.PaymentMethod
	} else if topup.PaymentMethod != order.PaymentMethod {
		return ErrPaymentMethodMismatch
	}
	if topup.CreateTime == 0 {
		topup.CreateTime = order.CreateTime
	}
	topup.CompleteTime = now
	topup.Status = common.TopUpStatusSuccess
	return tx.Save(&topup).Error
}

func ExpireSubscriptionOrder(tradeNo string, expectedPaymentProvider string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != common.TopUpStatusPending {
			return nil
		}
		order.Status = common.TopUpStatusExpired
		order.CompleteTime = common.GetTimestamp()
		return tx.Save(&order).Error
	})
}

// Admin bind (no payment). Creates a UserSubscription from a plan.
func AdminBindSubscription(userId int, planId int, sourceNote string) (string, error) {
	if userId <= 0 || planId <= 0 {
		return "", errors.New("invalid userId or planId")
	}
	plan, err := GetSubscriptionPlanById(planId)
	if err != nil {
		return "", err
	}
	groupChanged := false
	err = DB.Transaction(func(tx *gorm.DB) error {
		// 与 CompleteSubscriptionOrder 一致：先锁用户行，再做购买次数检查。
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&userRow).Error; err != nil {
			return err
		}
		subscription, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, "admin", SubscriptionUsageScopeGeneral)
		if err == nil {
			groupChanged = subscription.PrevUserGroup != ""
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if groupChanged {
		refreshSubscriptionUserGroupCache(userId, "admin subscription creation")
		return fmt.Sprintf("用户分组将升级到 %s", plan.UpgradeGroup), nil
	}
	return "", nil
}

func calcSubscriptionBalanceQuota(priceAmount float64) (int, error) {
	if priceAmount <= 0 {
		return 0, nil
	}
	if common.QuotaPerUnit <= 0 {
		return 0, errors.New("额度单位配置错误")
	}
	quota := decimal.NewFromFloat(priceAmount).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Ceil()
	return common.WalletQuotaFromDecimalStrict(quota)
}

// PurchaseSubscriptionWithBalance creates a subscription by deducting the user's wallet quota.
func PurchaseSubscriptionWithBalance(userId int, planId int) error {
	if userId <= 0 || planId <= 0 {
		return errors.New("invalid userId or planId")
	}

	var logPlanTitle string
	var logMoney float64
	var chargedQuota int
	var upgradeGroup string
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		if plan.PriceAmount < 0 {
			return errors.New("套餐价格不能为负数")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}

		requiredQuota, err := calcSubscriptionBalanceQuota(plan.PriceAmount)
		if err != nil {
			return err
		}

		var user User
		if err := lockForUpdate(tx).Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		if requiredQuota > 0 && user.Quota < requiredQuota {
			return errors.New("余额不足")
		}
		if requiredQuota > 0 {
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("quota", gorm.Expr("quota - ?", requiredQuota)).Error; err != nil {
				return err
			}
		}

		subscription, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, PaymentMethodBalance, SubscriptionUsageScopeGeneral)
		if err != nil {
			return err
		}

		now := common.GetTimestamp()
		tradeNo := fmt.Sprintf("SUBBALUSR%dNO%s%d", userId, common.GetRandomString(6), time.Now().UnixNano())
		order := &SubscriptionOrder{
			UserId:          userId,
			PlanId:          plan.Id,
			Money:           plan.PriceAmount,
			TradeNo:         tradeNo,
			PaymentMethod:   PaymentMethodBalance,
			PaymentProvider: PaymentProviderBalance,
			Status:          common.TopUpStatusSuccess,
			CreateTime:      now,
			CompleteTime:    now,
			ProviderPayload: fmt.Sprintf("charged_quota=%d", requiredQuota),
		}
		if err := tx.Create(order).Error; err != nil {
			return err
		}

		logPlanTitle = plan.Title
		logMoney = plan.PriceAmount
		chargedQuota = requiredQuota
		if subscription.PrevUserGroup != "" {
			upgradeGroup = strings.TrimSpace(subscription.UpgradeGroup)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if chargedQuota > 0 {
		if err := cacheDecrUserQuota(userId, int64(chargedQuota)); err != nil {
			common.SysLog("failed to decrease user quota cache after subscription balance purchase: " + err.Error())
		}
	}
	if upgradeGroup != "" {
		refreshSubscriptionUserGroupCache(userId, "subscription balance purchase")
	}
	msg := fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", logPlanTitle, logMoney, chargedQuota)
	RecordLog(userId, LogTypeTopup, msg)
	return nil
}

// GetAllActiveUserSubscriptions returns all active subscriptions for a user.
func GetAllActiveUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var subs []UserSubscription
	err := DB.Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

// HasActiveUserSubscription reports active native/general funding, never XCode funding.
// This is a lightweight existence check to avoid heavy pre-consume transactions.
func HasActiveUserSubscription(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Where("usage_scope IS NULL OR usage_scope = '' OR usage_scope = ?", SubscriptionUsageScopeGeneral).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UserActiveSubscriptionsAllowWalletOverflow returns whether wallet balance may be used
// after the user's subscription quota is exhausted. A single active subscription that
// disallows wallet overflow (allow_wallet_overflow = false) blocks the fallback.
func UserActiveSubscriptionsAllowWalletOverflow(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var strictCount int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ? AND allow_wallet_overflow = ?",
			userId, "active", now, false).
		Where("usage_scope IS NULL OR usage_scope = '' OR usage_scope = ?", SubscriptionUsageScopeGeneral).
		Count(&strictCount).Error; err != nil {
		return false, err
	}
	return strictCount == 0, nil
}

// GetAllUserSubscriptions returns all subscriptions (active and expired) for a user.
func GetAllUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	var subs []UserSubscription
	err := DB.Where("user_id = ?", userId).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

func buildSubscriptionSummaries(subs []UserSubscription) []SubscriptionSummary {
	if len(subs) == 0 {
		return []SubscriptionSummary{}
	}
	result := make([]SubscriptionSummary, 0, len(subs))
	for _, sub := range subs {
		subCopy := sub
		result = append(result, SubscriptionSummary{
			Subscription: &subCopy,
		})
	}
	return result
}

type SubscriptionAdminMutationResult struct {
	UserID                int
	SubscriptionID        int
	UsageScope            string
	AuthorizationRevision int64
	Message               string
}

// AdminInvalidateUserSubscription marks a user subscription as cancelled and ends it immediately.
func AdminInvalidateUserSubscription(userSubscriptionId int) (*SubscriptionAdminMutationResult, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	var hint UserSubscription
	if err := DB.Select("id", "user_id", "usage_scope").First(&hint, userSubscriptionId).Error; err != nil {
		return nil, err
	}
	var result *SubscriptionAdminMutationResult
	err := DB.Transaction(func(tx *gorm.DB) error {
		sub, authorization, now, err := lockAdminSubscriptionTx(tx, &hint)
		if err != nil {
			return err
		}
		result = &SubscriptionAdminMutationResult{UserID: sub.UserId, SubscriptionID: sub.Id, UsageScope: sub.UsageScope}
		if sub.UsageScope == SubscriptionUsageScopeXCode {
			if err := maybeResetUserSubscriptionWithPlanTx(tx, sub, nil, now); err != nil {
				return err
			}
		}
		if err := tx.Model(sub).Updates(map[string]any{
			"status": "cancelled", "end_time": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if authorization != nil {
			if authorization.SubscriptionId == sub.Id {
				if err := advanceXCodeAuthorizationRevisionTx(tx, authorization, now); err != nil {
					return err
				}
			}
			result.AuthorizationRevision = authorization.Revision
		}
		target, err := downgradeUserGroupForSubscriptionTx(tx, sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			result.Message = fmt.Sprintf("用户分组将回退到 %s", target)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.Message != "" {
		if result.UsageScope == SubscriptionUsageScopeXCode {
			if err := RefreshUserGroupCache(result.UserID); err != nil {
				return result, fmt.Errorf("subscription cancellation committed but group cache refresh failed: %w", err)
			}
		} else {
			refreshSubscriptionUserGroupCache(result.UserID, "admin subscription update")
		}
	}
	return result, nil
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(userSubscriptionId int) (*SubscriptionAdminMutationResult, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	var hint UserSubscription
	if err := DB.Select("id", "user_id", "usage_scope").First(&hint, userSubscriptionId).Error; err != nil {
		return nil, err
	}
	var result *SubscriptionAdminMutationResult
	err := DB.Transaction(func(tx *gorm.DB) error {
		sub, authorization, now, err := lockAdminSubscriptionTx(tx, &hint)
		if err != nil {
			return err
		}
		result = &SubscriptionAdminMutationResult{UserID: sub.UserId, SubscriptionID: sub.Id, UsageScope: sub.UsageScope}
		if authorization != nil {
			result.AuthorizationRevision = authorization.Revision
		}
		if sub.UsageScope == SubscriptionUsageScopeXCode {
			if err := preventReferencedXCodeSubscriptionDeleteTx(tx, sub); err != nil {
				return err
			}
		}
		target, err := downgradeUserGroupForSubscriptionTx(tx, sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			result.Message = fmt.Sprintf("用户分组将回退到 %s", target)
		}
		return tx.Where("id = ?", userSubscriptionId).Delete(&UserSubscription{}).Error
	})
	if err != nil {
		return nil, err
	}
	if result.Message != "" {
		if result.UsageScope == SubscriptionUsageScopeXCode {
			if err := RefreshUserGroupCache(result.UserID); err != nil {
				return result, fmt.Errorf("subscription deletion committed but group cache refresh failed: %w", err)
			}
		} else {
			refreshSubscriptionUserGroupCache(result.UserID, "admin subscription deletion")
		}
	}
	return result, nil
}

func subscriptionResetPlan(sub *UserSubscription, plan *SubscriptionPlan) (*SubscriptionPlan, error) {
	if sub == nil {
		return nil, ErrSubscriptionFundingTargetInvalid
	}
	if sub.UsageScope == SubscriptionUsageScopeXCode {
		period := sub.QuotaResetPeriodSnapshot
		if sub.QuotaResetGeneration <= 0 || period != NormalizeResetPeriod(period) ||
			(period == SubscriptionResetCustom && (sub.QuotaResetCustomSecondsSnapshot <= 0 ||
				sub.QuotaResetCustomSecondsSnapshot > math.MaxInt64/int64(time.Second))) {
			return nil, ErrSubscriptionFundingTargetInvalid
		}
		return &SubscriptionPlan{
			QuotaResetPeriod: period, QuotaResetCustomSeconds: sub.QuotaResetCustomSecondsSnapshot,
		}, nil
	}
	if (sub.UsageScope != "" && sub.UsageScope != SubscriptionUsageScopeGeneral) || plan == nil {
		return nil, ErrSubscriptionFundingTargetInvalid
	}
	return plan, nil
}

func resetUserSubscriptionTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64, advanceResetTime bool) error {
	if tx == nil {
		return errors.New("invalid reset args")
	}
	plan, err := subscriptionResetPlan(sub, plan)
	if err != nil {
		return err
	}
	if sub.UsageScope == SubscriptionUsageScopeXCode {
		if sub.QuotaResetGeneration == math.MaxInt64 {
			return ErrSubscriptionFundingTargetInvalid
		}
		sub.QuotaResetGeneration++
	}
	sub.AmountUsed = 0
	if advanceResetTime {
		nextReset := calcNextResetTime(time.Unix(now, 0), plan, sub.EndTime)
		sub.NextResetTime = nextReset
		if nextReset > 0 || (sub.UsageScope == SubscriptionUsageScopeXCode && plan.QuotaResetPeriod != SubscriptionResetNever) {
			sub.LastResetTime = now
		} else {
			sub.LastResetTime = 0
		}
	}
	return tx.Save(sub).Error
}

func buildSubscriptionResetResult(plan *SubscriptionPlan, subs []UserSubscription, advanceResetTime bool) *SubscriptionResetResult {
	userIds := make([]int, 0, len(subs))
	seenUsers := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if _, ok := seenUsers[sub.UserId]; ok {
			continue
		}
		seenUsers[sub.UserId] = struct{}{}
		userIds = append(userIds, sub.UserId)
	}
	return &SubscriptionResetResult{
		PlanId:           plan.Id,
		MatchedCount:     len(subs),
		ResetCount:       len(subs),
		UserCount:        len(userIds),
		AdvanceResetTime: advanceResetTime,
		PlanTitle:        plan.Title,
		AffectedUserIds:  userIds,
	}
}

func adminResetUserSubscriptionsByPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("user_id = ? AND plan_id = ? AND status = ? AND end_time > ?", userId, plan.Id, "active", now).
		Order("end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, errors.New("该用户没有有效的此套餐订阅")
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func adminResetPlanSubscriptionsTx(tx *gorm.DB, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("plan_id = ? AND status = ? AND end_time > ?", plan.Id, "active", now).
		Order("user_id asc, end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func AdminResetUserSubscriptionsByPlan(userId int, planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if userId <= 0 || planId <= 0 {
		return nil, errors.New("invalid userId or planId")
	}
	var result *SubscriptionResetResult
	err := DB.Transaction(func(tx *gorm.DB) error {
		now, err := getDBTimestamp(tx)
		if err != nil {
			return err
		}
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetUserSubscriptionsByPlanTx(tx, userId, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func AdminResetPlanSubscriptions(planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if planId <= 0 {
		return nil, errors.New("invalid planId")
	}
	var result *SubscriptionResetResult
	err := DB.Transaction(func(tx *gorm.DB) error {
		now, err := getDBTimestamp(tx)
		if err != nil {
			return err
		}
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetPlanSubscriptionsTx(tx, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type SubscriptionPreConsumeResult struct {
	UserSubscriptionId int
	PreConsumed        int64
	AmountTotal        int64
	AmountUsedBefore   int64
	AmountUsedAfter    int64
}

// ExpireDueSubscriptions marks expired subscriptions and handles group downgrade.
func ExpireDueSubscriptions(limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("status = ? AND end_time > 0 AND end_time <= ?", "active", now).
		Order("end_time asc, id asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	expiredCount := 0
	userIds := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if sub.UserId > 0 {
			userIds[sub.UserId] = struct{}{}
		}
	}
	for userId := range userIds {
		cacheGroup := ""
		err := DB.Transaction(func(tx *gorm.DB) error {
			// Administrator designation transitions also lock the owner before instances.
			var user User
			if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).Find(&user).Error; err != nil {
				return err
			}
			res := tx.Model(&UserSubscription{}).
				Where("user_id = ? AND status = ? AND end_time > 0 AND end_time <= ?", userId, "active", now).
				Updates(map[string]any{
					"status":     "expired",
					"updated_at": common.GetTimestamp(),
				})
			if res.Error != nil {
				return res.Error
			}
			expiredCount += int(res.RowsAffected)

			// If there's an active upgraded subscription, keep current group.
			var activeSub UserSubscription
			activeQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND upgrade_group <> ''",
				userId, "active", now).
				Order("end_time desc, id desc").
				Limit(1).
				Find(&activeSub)
			if activeQuery.Error == nil && activeQuery.RowsAffected > 0 {
				return nil
			}

			// Find the most recently expired subscription that defines a group transition
			// (an explicit downgrade target or an upgrade snapshot to revert).
			var lastExpired UserSubscription
			expiredQuery := tx.Where("user_id = ? AND status = ? AND (downgrade_group <> '' OR upgrade_group <> '')",
				userId, "expired").
				Order("end_time desc, id desc").
				Limit(1).
				Find(&lastExpired)
			if expiredQuery.Error != nil || expiredQuery.RowsAffected == 0 {
				return nil
			}
			currentGroup, err := getUserGroupByIdTx(tx, userId)
			if err != nil {
				return err
			}
			// An explicit downgrade group takes precedence; otherwise revert to the
			// group held before purchase (legacy behavior, only when the subscription
			// actually elevated the user).
			target := strings.TrimSpace(lastExpired.DowngradeGroup)
			if target == "" {
				upgradeGroup := strings.TrimSpace(lastExpired.UpgradeGroup)
				prevGroup := strings.TrimSpace(lastExpired.PrevUserGroup)
				if upgradeGroup == "" || prevGroup == "" {
					return nil
				}
				if currentGroup != upgradeGroup {
					return nil
				}
				target = prevGroup
			}
			if target == "" || target == currentGroup {
				return nil
			}
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", target).Error; err != nil {
				return err
			}
			cacheGroup = target
			return nil
		})
		if err != nil {
			return expiredCount, err
		}
		if cacheGroup != "" {
			refreshSubscriptionUserGroupCache(userId, "subscription expiration")
		}
	}
	return expiredCount, nil
}

// SubscriptionFundingTarget is an explicit server-owned funding domain.
// General requests have no designated instance or authorization revision.
type SubscriptionFundingTarget struct {
	Scope                 string
	SubscriptionID        int
	AuthorizationRevision int64
}

// SubscriptionPreConsumeRecord stores idempotent pre-consume operations per request.
type SubscriptionPreConsumeRecord struct {
	Id                 int    `json:"id"`
	RequestId          string `json:"request_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId             int    `json:"user_id" gorm:"index"`
	UserSubscriptionId int    `json:"user_subscription_id" gorm:"index"`
	PreConsumed        int64  `json:"pre_consumed" gorm:"type:bigint;not null;default:0"`
	FundingScope       string `json:"funding_scope" gorm:"type:varchar(16);index"`
	ResetGeneration    int64  `json:"reset_generation" gorm:"type:bigint"`
	CycleReservedQuota int64  `json:"cycle_reserved_quota" gorm:"type:bigint"`
	FinalQuota         int64  `json:"final_quota" gorm:"type:bigint"`
	Status             string `json:"status" gorm:"type:varchar(32);index"` // consumed/settled/refunded
	CreatedAt          int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt          int64  `json:"updated_at" gorm:"bigint;index"`
}

func (r *SubscriptionPreConsumeRecord) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	r.CreatedAt = now
	r.UpdatedAt = now
	return nil
}

func (r *SubscriptionPreConsumeRecord) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedAt = common.GetTimestamp()
	return nil
}

func maybeResetUserSubscriptionWithPlanTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64) error {
	if tx == nil {
		return errors.New("invalid reset args")
	}
	plan, err := subscriptionResetPlan(sub, plan)
	if err != nil {
		return err
	}
	if sub.UsageScope == SubscriptionUsageScopeXCode && (sub.Status != "active" || sub.EndTime <= now) {
		return nil
	}
	if sub.NextResetTime > 0 && sub.NextResetTime > now {
		return nil
	}
	if NormalizeResetPeriod(plan.QuotaResetPeriod) == SubscriptionResetNever {
		return nil
	}
	baseUnix := sub.LastResetTime
	if baseUnix <= 0 {
		baseUnix = sub.StartTime
	}
	base := time.Unix(baseUnix, 0)
	next := calcNextResetTime(base, plan, sub.EndTime)
	advanced := false
	for next > 0 && next <= now {
		advanced = true
		base = time.Unix(next, 0)
		next = calcNextResetTime(base, plan, sub.EndTime)
	}
	if !advanced {
		if sub.NextResetTime == 0 && next > 0 {
			sub.NextResetTime = next
			sub.LastResetTime = base.Unix()
			return tx.Save(sub).Error
		}
		return nil
	}
	if sub.UsageScope == SubscriptionUsageScopeXCode {
		if sub.QuotaResetGeneration == math.MaxInt64 {
			return ErrSubscriptionFundingTargetInvalid
		}
		sub.QuotaResetGeneration++
	}
	sub.AmountUsed = 0
	sub.LastResetTime = base.Unix()
	sub.NextResetTime = next
	return tx.Save(sub).Error
}

// PreConsumeUserSubscription reserves only the explicitly admitted funding domain.
func PreConsumeUserSubscription(requestId string, userId int, modelName string, quotaType int, amount int64, target SubscriptionFundingTarget) (*SubscriptionPreConsumeResult, error) {
	switch target.Scope {
	case SubscriptionUsageScopeGeneral:
		if target.SubscriptionID != 0 || target.AuthorizationRevision != 0 {
			return nil, ErrSubscriptionFundingTargetInvalid
		}
	case SubscriptionUsageScopeXCode:
		if target.SubscriptionID <= 0 || target.AuthorizationRevision <= 0 {
			return nil, ErrSubscriptionFundingTargetInvalid
		}
	default:
		return nil, ErrSubscriptionFundingTargetInvalid
	}
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	if amount < 0 || (amount == 0 && target.Scope != SubscriptionUsageScopeXCode) {
		return nil, errors.New("amount must be positive for native funding and nonnegative for XCode funding")
	}

	now := int64(0)
	if target.Scope == SubscriptionUsageScopeGeneral {
		// Preserve the native clock fallback outside the funding transaction.
		now = GetDBTimestamp()
	}

	var result *SubscriptionPreConsumeResult
	recordCreateFailed := false
	admit := func(tx *gorm.DB) error {
		recordCreateFailed = false
		if target.Scope == SubscriptionUsageScopeXCode {
			var authorization XCodeAuthorization
			if err := lockForUpdate(tx).Where("user_id = ?", userId).First(&authorization).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrXCodePlanUnassigned
				}
				return err
			}
			if authorization.SubscriptionId != target.SubscriptionID || authorization.Revision != target.AuthorizationRevision {
				return ErrXCodeAuthorizationConflict
			}
		}

		var existing SubscriptionPreConsumeRecord
		query := tx.Where("request_id = ?", requestId).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			var err error
			result, err = replaySubscriptionPreConsumeTx(tx, &existing, userId, target)
			return err
		}

		var subs []UserSubscription
		query = lockForUpdate(tx)
		if target.Scope == SubscriptionUsageScopeXCode {
			query = query.Where("id = ?", target.SubscriptionID)
		} else {
			query = query.Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
				Where("usage_scope IS NULL OR usage_scope = '' OR usage_scope = ?", SubscriptionUsageScopeGeneral).
				Order("end_time asc, id asc")
		}
		if err := query.Find(&subs).Error; err != nil {
			return err
		}
		if len(subs) == 0 {
			if target.Scope == SubscriptionUsageScopeXCode {
				return ErrXCodePlanRevoked
			}
			return ErrNoActiveSubscription
		}
		if target.Scope == SubscriptionUsageScopeXCode {
			var err error
			now, err = getDBTimestamp(tx)
			if err != nil {
				return err
			}
		}
		for i := range subs {
			sub := &subs[i]
			if target.Scope == SubscriptionUsageScopeXCode {
				if err := validateXCodeFundingSubscription(sub, userId, now); err != nil {
					return err
				}
			}
			var plan *SubscriptionPlan
			if sub.UsageScope != SubscriptionUsageScopeXCode {
				var err error
				plan, err = getSubscriptionPlanByIdTx(tx, sub.PlanId)
				if err != nil {
					return err
				}
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, sub, plan, now); err != nil {
				return err
			}
			usedBefore := sub.AmountUsed
			if usedBefore < 0 || sub.AmountTotal < 0 {
				return errors.New("invalid subscription quota")
			}
			if amount > math.MaxInt64-usedBefore || (sub.AmountTotal > 0 && (sub.AmountTotal-usedBefore < amount || usedBefore >= sub.AmountTotal)) {
				continue
			}
			record := &SubscriptionPreConsumeRecord{
				RequestId: requestId, UserId: userId, UserSubscriptionId: sub.Id,
				PreConsumed: amount, FundingScope: target.Scope, Status: "consumed",
			}
			if target.Scope == SubscriptionUsageScopeXCode {
				record.ResetGeneration = sub.QuotaResetGeneration
				record.CycleReservedQuota = amount
			}
			if err := tx.Create(record).Error; err != nil {
				recordCreateFailed = true
				return err
			}
			if amount > 0 {
				sub.AmountUsed += amount
				if err := tx.Save(sub).Error; err != nil {
					return err
				}
			}
			result = &SubscriptionPreConsumeResult{
				UserSubscriptionId: sub.Id, PreConsumed: amount,
				AmountTotal: sub.AmountTotal, AmountUsedBefore: usedBefore, AmountUsedAfter: sub.AmountUsed,
			}
			return nil
		}
		return ErrSubscriptionQuotaInsufficient
	}
	err := DB.Transaction(admit)
	if err != nil && recordCreateFailed {
		// PostgreSQL aborts a transaction after a unique-key conflict. Resolve
		// a concurrent committed request only after rollback, in a fresh transaction.
		var committed SubscriptionPreConsumeRecord
		query := DB.Where("request_id = ?", requestId).Limit(1).Find(&committed)
		if query.Error != nil {
			return nil, query.Error
		}
		if query.RowsAffected > 0 {
			err = DB.Transaction(admit)
		}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (record *SubscriptionPreConsumeRecord) matchesFundingTarget(userId int, target SubscriptionFundingTarget) bool {
	if record.UserId != userId {
		return false
	}
	if target.Scope == SubscriptionUsageScopeGeneral {
		return record.FundingScope == "" || record.FundingScope == SubscriptionUsageScopeGeneral
	}
	return record.FundingScope == SubscriptionUsageScopeXCode && record.UserSubscriptionId == target.SubscriptionID
}

// Replay locks the original instance before its request record. It cannot select
// another user's balance or reinterpret a legacy native record as XCode funding.
func replaySubscriptionPreConsumeTx(tx *gorm.DB, hint *SubscriptionPreConsumeRecord, userId int, target SubscriptionFundingTarget) (*SubscriptionPreConsumeResult, error) {
	if !hint.matchesFundingTarget(userId, target) {
		return nil, ErrSubscriptionRequestConflict
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).First(&sub, hint.UserSubscriptionId).Error; err != nil {
		return nil, err
	}
	var record SubscriptionPreConsumeRecord
	if err := lockForUpdate(tx).First(&record, hint.Id).Error; err != nil {
		return nil, err
	}
	if !record.matchesFundingTarget(userId, target) || record.UserSubscriptionId != sub.Id ||
		sub.UserId != userId || record.Status != "consumed" {
		return nil, ErrSubscriptionRequestConflict
	}
	if target.Scope == SubscriptionUsageScopeXCode {
		now, err := getDBTimestamp(tx)
		if err != nil {
			return nil, err
		}
		if err := validateXCodeFundingSubscription(&sub, userId, now); err != nil {
			return nil, err
		}
	} else if sub.UsageScope != "" && sub.UsageScope != SubscriptionUsageScopeGeneral {
		return nil, ErrSubscriptionRequestConflict
	}
	return &SubscriptionPreConsumeResult{
		UserSubscriptionId: sub.Id, PreConsumed: record.PreConsumed,
		AmountTotal: sub.AmountTotal, AmountUsedBefore: sub.AmountUsed, AmountUsedAfter: sub.AmountUsed,
	}, nil
}

type SubscriptionReserveMutation struct {
	Generation            int64
	PreviousReservedQuota int64
	ReservedQuota         int64
}

// An admitted request always finishes against its original instance, not the current authorization.
func lockXCodeSubscriptionRequestTx(tx *gorm.DB, requestID string, userID, subscriptionID int) (*UserSubscription, *SubscriptionPreConsumeRecord, error) {
	if strings.TrimSpace(requestID) == "" || userID <= 0 || subscriptionID <= 0 {
		return nil, nil, ErrSubscriptionFundingTargetInvalid
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).First(&sub, subscriptionID).Error; err != nil {
		return nil, nil, err
	}
	if sub.UserId != userID || sub.UsageScope != SubscriptionUsageScopeXCode {
		return nil, nil, ErrSubscriptionRequestConflict
	}
	now, err := getDBTimestamp(tx)
	if err != nil {
		return nil, nil, err
	}
	if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, nil, now); err != nil {
		return nil, nil, err
	}
	var record SubscriptionPreConsumeRecord
	if err := lockForUpdate(tx).Where("request_id = ?", requestID).First(&record).Error; err != nil {
		return nil, nil, err
	}
	if record.UserId != userID || record.UserSubscriptionId != sub.Id || record.FundingScope != SubscriptionUsageScopeXCode ||
		record.ResetGeneration <= 0 || record.ResetGeneration > sub.QuotaResetGeneration ||
		record.PreConsumed < 0 || record.CycleReservedQuota < 0 || record.FinalQuota < 0 ||
		sub.AmountUsed < 0 || sub.AmountTotal < 0 {
		return nil, nil, ErrSubscriptionRequestConflict
	}
	switch record.Status {
	case "consumed", "settled", "refunded":
	default:
		return nil, nil, ErrSubscriptionRequestConflict
	}
	return &sub, &record, nil
}

func (record *SubscriptionPreConsumeRecord) currentXCodeReservedQuota(sub *UserSubscription) int64 {
	if record.ResetGeneration == sub.QuotaResetGeneration {
		return record.CycleReservedQuota
	}
	return 0
}

func saveXCodeRequestOccupancyTx(tx *gorm.DB, sub *UserSubscription, record *SubscriptionPreConsumeRecord, targetQuota int64) error {
	current := record.currentXCodeReservedQuota(sub)
	if current > sub.AmountUsed {
		return ErrSubscriptionRequestConflict
	}
	otherUsed := sub.AmountUsed - current
	if targetQuota < 0 || targetQuota > math.MaxInt64-otherUsed ||
		(sub.AmountTotal > 0 && targetQuota > sub.AmountTotal-otherUsed) {
		return ErrSubscriptionQuotaInsufficient
	}
	sub.AmountUsed = otherUsed + targetQuota
	record.ResetGeneration = sub.QuotaResetGeneration
	record.CycleReservedQuota = targetQuota
	if err := tx.Save(sub).Error; err != nil {
		return err
	}
	return tx.Save(record).Error
}

func ReserveXCodeSubscriptionRequest(requestID string, userID, subscriptionID int, targetQuota int64) (SubscriptionReserveMutation, error) {
	if targetQuota < 0 {
		return SubscriptionReserveMutation{}, ErrSubscriptionFundingTargetInvalid
	}
	var mutation SubscriptionReserveMutation
	err := DB.Transaction(func(tx *gorm.DB) error {
		sub, record, err := lockXCodeSubscriptionRequestTx(tx, requestID, userID, subscriptionID)
		if err != nil {
			return err
		}
		if record.Status != "consumed" {
			return ErrSubscriptionRequestConflict
		}
		mutation = SubscriptionReserveMutation{
			Generation:            sub.QuotaResetGeneration,
			PreviousReservedQuota: record.currentXCodeReservedQuota(sub), ReservedQuota: targetQuota,
		}
		return saveXCodeRequestOccupancyTx(tx, sub, record, targetQuota)
	})
	if err != nil {
		return SubscriptionReserveMutation{}, err
	}
	return mutation, nil
}

func RollbackXCodeSubscriptionReserve(requestID string, userID, subscriptionID int, mutation SubscriptionReserveMutation) error {
	if mutation.Generation <= 0 || mutation.PreviousReservedQuota < 0 || mutation.ReservedQuota < 0 {
		return ErrSubscriptionFundingTargetInvalid
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		sub, record, err := lockXCodeSubscriptionRequestTx(tx, requestID, userID, subscriptionID)
		if err != nil {
			return err
		}
		if mutation.Generation > sub.QuotaResetGeneration {
			return ErrSubscriptionRequestConflict
		}
		if record.Status != "consumed" || mutation.Generation != sub.QuotaResetGeneration {
			return nil
		}
		if record.ResetGeneration != mutation.Generation || record.CycleReservedQuota != mutation.ReservedQuota {
			return ErrSubscriptionRequestConflict
		}
		return saveXCodeRequestOccupancyTx(tx, sub, record, mutation.PreviousReservedQuota)
	})
}

func SettleXCodeSubscriptionRequest(requestID string, userID, subscriptionID int, actualQuota int64) error {
	if actualQuota < 0 {
		return ErrSubscriptionFundingTargetInvalid
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		sub, record, err := lockXCodeSubscriptionRequestTx(tx, requestID, userID, subscriptionID)
		if err != nil {
			return err
		}
		if record.Status == "settled" {
			if record.FinalQuota == actualQuota {
				return nil
			}
			return ErrSubscriptionRequestConflict
		}
		if record.Status != "consumed" {
			return ErrSubscriptionRequestConflict
		}
		record.Status = "settled"
		record.FinalQuota = actualQuota
		return saveXCodeRequestOccupancyTx(tx, sub, record, actualQuota)
	})
}

func RefundXCodeSubscriptionRequest(requestID string, userID, subscriptionID int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		sub, record, err := lockXCodeSubscriptionRequestTx(tx, requestID, userID, subscriptionID)
		if err != nil {
			return err
		}
		if record.Status == "settled" || record.Status == "refunded" {
			return nil
		}
		record.Status = "refunded"
		record.FinalQuota = 0
		return saveXCodeRequestOccupancyTx(tx, sub, record, 0)
	})
}

// RefundSubscriptionPreConsume is idempotent and refunds pre-consumed subscription quota by requestId.
func RefundSubscriptionPreConsume(requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var hint SubscriptionPreConsumeRecord
		if err := tx.Where("request_id = ?", requestId).First(&hint).Error; err != nil {
			return err
		}
		var sub UserSubscription
		if err := lockForUpdate(tx).First(&sub, hint.UserSubscriptionId).Error; err != nil {
			return err
		}
		var record SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).First(&record, hint.Id).Error; err != nil {
			return err
		}
		if record.UserSubscriptionId != sub.Id || record.UserId != sub.UserId ||
			(record.FundingScope != "" && record.FundingScope != SubscriptionUsageScopeGeneral) ||
			(sub.UsageScope != "" && sub.UsageScope != SubscriptionUsageScopeGeneral) {
			return ErrSubscriptionFundingTargetInvalid
		}
		if record.Status == "refunded" {
			return nil
		}
		if record.PreConsumed <= 0 {
			record.Status = "refunded"
			return tx.Save(&record).Error
		}
		if sub.AmountUsed < 0 || sub.AmountTotal < 0 {
			return ErrSubscriptionFundingTargetInvalid
		}
		sub.AmountUsed = max(sub.AmountUsed-record.PreConsumed, 0)
		if sub.AmountTotal > 0 && sub.AmountUsed > sub.AmountTotal {
			return ErrSubscriptionQuotaInsufficient
		}
		if err := tx.Save(&sub).Error; err != nil {
			return err
		}
		record.Status = "refunded"
		return tx.Save(&record).Error
	})
}

// ResetDueSubscriptions resets subscriptions whose next_reset_time has passed.
func ResetDueSubscriptions(limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("next_reset_time > 0 AND next_reset_time <= ? AND status = ?", now, "active").
		Order("next_reset_time asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	resetCount := 0
	for _, sub := range subs {
		subCopy := sub
		didReset := false
		err := DB.Transaction(func(tx *gorm.DB) error {
			var locked UserSubscription
			if err := lockForUpdate(tx).
				Where("id = ? AND next_reset_time > 0 AND next_reset_time <= ? AND status = ?", subCopy.Id, now, "active").
				First(&locked).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			resetNow := now
			var plan *SubscriptionPlan
			var err error
			if locked.UsageScope == SubscriptionUsageScopeXCode {
				resetNow, err = getDBTimestamp(tx)
				if err != nil {
					return err
				}
				if locked.EndTime <= resetNow {
					return nil
				}
			} else {
				plan, err = getSubscriptionPlanByIdTx(tx, locked.PlanId)
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				if err != nil {
					return err
				}
			}
			previousReset := locked.LastResetTime
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &locked, plan, resetNow); err != nil {
				return err
			}
			didReset = locked.LastResetTime != previousReset
			return nil
		})
		if err != nil {
			return resetCount, err
		}
		if didReset {
			resetCount++
		}
	}
	return resetCount, nil
}

// CleanupSubscriptionPreConsumeRecords removes old idempotency records to keep table small.
func CleanupSubscriptionPreConsumeRecords(olderThanSeconds int64) (int64, error) {
	if olderThanSeconds <= 0 {
		olderThanSeconds = 7 * 24 * 3600
	}
	cutoff := GetDBTimestamp() - olderThanSeconds
	res := DB.Where("updated_at < ?", cutoff).
		Where("funding_scope IS NULL OR funding_scope <> ? OR status IN ?", SubscriptionUsageScopeXCode, []string{"settled", "refunded"}).
		Delete(&SubscriptionPreConsumeRecord{})
	return res.RowsAffected, res.Error
}

type SubscriptionPlanInfo struct {
	PlanId    int
	PlanTitle string
}

func GetSubscriptionPlanInfoByUserSubscriptionId(userSubscriptionId int) (*SubscriptionPlanInfo, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	cacheKey := fmt.Sprintf("sub:%d", userSubscriptionId)
	if cached, found, err := getSubscriptionPlanInfoCache().Get(cacheKey); err == nil && found {
		return &cached, nil
	}
	var sub UserSubscription
	if err := DB.Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
		return nil, err
	}
	plan, err := getSubscriptionPlanByIdTx(nil, sub.PlanId)
	if err != nil {
		return nil, err
	}
	info := &SubscriptionPlanInfo{
		PlanId:    sub.PlanId,
		PlanTitle: plan.Title,
	}
	_ = getSubscriptionPlanInfoCache().SetWithTTL(cacheKey, *info, subscriptionPlanInfoCacheTTL())
	return info, nil
}

// Update subscription used amount by delta (positive consume more, negative refund).
func PostConsumeUserSubscriptionDelta(userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).
			First(&sub).Error; err != nil {
			return err
		}
		if sub.UsageScope != "" && sub.UsageScope != SubscriptionUsageScopeGeneral {
			return ErrSubscriptionFundingTargetInvalid
		}
		if sub.AmountUsed < 0 || sub.AmountTotal < 0 {
			return ErrSubscriptionFundingTargetInvalid
		}
		if delta == 0 {
			return nil
		}
		if delta > 0 && delta > math.MaxInt64-sub.AmountUsed {
			return ErrSubscriptionQuotaInsufficient
		}
		newUsed := int64(0)
		if delta >= -sub.AmountUsed {
			newUsed = sub.AmountUsed + delta
		}
		if sub.AmountTotal > 0 && newUsed > sub.AmountTotal {
			return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, sub.AmountTotal)
		}
		sub.AmountUsed = newUsed
		return tx.Save(&sub).Error
	})
}

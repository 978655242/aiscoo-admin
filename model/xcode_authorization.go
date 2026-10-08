package model

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

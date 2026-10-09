package dto

// XCodeAuthorizationSnapshot uses decimal strings for IDs and quota facts;
// timestamps are Unix milliseconds, never the native subscription seconds.
type XCodeAuthorizationSnapshot struct {
	SchemaVersion         int                     `json:"schema_version"`
	ServerTime            int64                   `json:"server_time"`
	Status                string                  `json:"status"`
	AuthorizationRevision string                  `json:"authorization_revision"`
	Subscription          *XCodeSubscription      `json:"subscription"`
	Quota                 *XCodeSubscriptionQuota `json:"quota"`
	FundingPolicy         XCodeFundingPolicy      `json:"funding_policy"`
	SelfServiceReset      bool                    `json:"self_service_reset"`
	PurchaseMode          string                  `json:"purchase_mode"`
}

type XCodeSubscription struct {
	ID         string `json:"id"`
	PlanID     string `json:"plan_id"`
	PlanTitle  string `json:"plan_title"`
	Source     string `json:"source"`
	UsageScope string `json:"usage_scope"`
	StartTime  *int64 `json:"start_time"`
	EndTime    *int64 `json:"end_time"`
}

type XCodeSubscriptionQuota struct {
	Scope                 string   `json:"scope"`
	Unit                  string   `json:"unit"`
	Unlimited             bool     `json:"unlimited"`
	Total                 *string  `json:"total"`
	Used                  string   `json:"used"`
	Remaining             *string  `json:"remaining"`
	UsedPercentage        *float64 `json:"used_percentage"`
	ResetPeriod           string   `json:"reset_period"`
	ResetCustomSeconds    string   `json:"reset_custom_seconds"`
	LastResetTime         *int64   `json:"last_reset_time"`
	NextResetTime         *int64   `json:"next_reset_time"`
	ResetTimeZone         string   `json:"reset_time_zone"`
	ResetUTCOffsetSeconds int      `json:"reset_utc_offset_seconds"`
}

type XCodeFundingPolicy struct {
	SubscriptionOnly          bool `json:"subscription_only"`
	WalletFallback            bool `json:"wallet_fallback"`
	OtherSubscriptionFallback bool `json:"other_subscription_fallback"`
}

type XCodeAuthorizationMutation struct {
	Action           string `json:"action"`
	ExpectedRevision string `json:"expected_revision"`
	PlanID           string `json:"plan_id"`
	AdvanceResetTime *bool  `json:"advance_reset_time"`
}

type XCodeAuthorizationRequestView struct {
	SchemaVersion int    `json:"schema_version"`
	ServerTime    int64  `json:"server_time"`
	ClientID      string `json:"client_id"`
	ExpiresAt     int64  `json:"expires_at"`
}

type XCodeAuthorizationCompleteResponse struct {
	SchemaVersion int    `json:"schema_version"`
	ServerTime    int64  `json:"server_time"`
	RedirectURL   string `json:"redirect_url"`
}

type XCodeSessionBundle struct {
	SchemaVersion    int                  `json:"schema_version"`
	ServerTime       int64                `json:"server_time"`
	AccessToken      string               `json:"access_token"`
	AccessExpiresAt  int64                `json:"access_expires_at"`
	RefreshToken     string               `json:"refresh_token"`
	RefreshExpiresAt int64                `json:"refresh_expires_at"`
	TokenType        string               `json:"token_type"`
	SessionID        string               `json:"session_id"`
	User             XCodeUser            `json:"user"`
	RelayCredential  XCodeRelayCredential `json:"relay_credential"`
}

type XCodeUser struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

type XCodeRelayCredential struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

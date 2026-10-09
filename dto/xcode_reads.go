package dto

type XCodeMeSnapshot struct {
	SchemaVersion         int       `json:"schema_version"`
	ServerTime            int64     `json:"server_time"`
	User                  XCodeUser `json:"user"`
	SessionID             string    `json:"session_id"`
	AuthorizationRevision string    `json:"authorization_revision"`
}

type XCodePlansSnapshot struct {
	SchemaVersion       int         `json:"schema_version"`
	ServerTime          int64       `json:"server_time"`
	Items               []XCodePlan `json:"items"`
	PurchaseMode        string      `json:"purchase_mode"`
	SelfServicePurchase bool        `json:"self_service_purchase"`
}

type XCodePlan struct {
	ID                    string `json:"id"`
	Title                 string `json:"title"`
	Subtitle              string `json:"subtitle"`
	PriceAmount           string `json:"price_amount"`
	Currency              string `json:"currency"`
	DurationUnit          string `json:"duration_unit"`
	DurationValue         string `json:"duration_value"`
	DurationCustomSeconds string `json:"duration_custom_seconds"`
	TotalQuota            string `json:"total_quota"`
	ResetPeriod           string `json:"reset_period"`
	ResetCustomSeconds    string `json:"reset_custom_seconds"`
	Enabled               bool   `json:"enabled"`
}

type XCodeQuotaDisplay struct {
	QuotaPerUnit      string  `json:"quota_per_unit"`
	DisplayType       string  `json:"display_type"`
	Currency          *string `json:"currency"`
	CurrencySymbol    string  `json:"currency_symbol"`
	USDToCurrencyRate *string `json:"usd_to_currency_rate"`
}

type XCodeClientConfigSnapshot struct {
	SchemaVersion       int               `json:"schema_version"`
	ServerTime          int64             `json:"server_time"`
	PurchaseMode        string            `json:"purchase_mode"`
	SelfServicePurchase bool              `json:"self_service_purchase"`
	SelfServiceReset    bool              `json:"self_service_reset"`
	ManualPlanEntryPath string            `json:"manual_plan_entry_path"`
	Display             XCodeQuotaDisplay `json:"display"`
}

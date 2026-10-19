package dto

type XCodeUsageQuery struct {
	Range     string `form:"range"`
	TimeZone  string `form:"time_zone"`
	StartDate string `form:"start_date"`
	EndDate   string `form:"end_date"`
}

type XCodeUsageSummary struct {
	ConsumeRecordCount string `json:"consume_record_count"`
	PromptTokens       string `json:"prompt_tokens"`
	CompletionTokens   string `json:"completion_tokens"`
	TotalTokens        string `json:"total_tokens"`
	ChargedQuota       string `json:"charged_quota"`
	RefundedQuota      string `json:"refunded_quota"`
	NetQuota           string `json:"net_quota"`
}

type XCodeUsageBucket struct {
	StartTime int64  `json:"start_time"`
	EndTime   int64  `json:"end_time"`
	Label     string `json:"label"`
	XCodeUsageSummary
}

type XCodeModelUsage struct {
	ModelID string `json:"model_id"`
	XCodeUsageSummary
	Buckets []XCodeUsageBucket `json:"buckets"`
}

type XCodeAccountUsageSnapshot struct {
	SchemaVersion      int                `json:"schema_version"`
	ServerTime         int64              `json:"server_time"`
	AsOf               int64              `json:"as_of"`
	Scope              string             `json:"scope"`
	Source             string             `json:"source"`
	RetentionBasis     string             `json:"retention_basis"`
	TimeZone           string             `json:"time_zone"`
	RangeStartDate     string             `json:"range_start_date"`
	RangeEndDate       string             `json:"range_end_date"`
	Granularity        string             `json:"granularity"`
	Display            XCodeQuotaDisplay  `json:"display"`
	Summary            XCodeUsageSummary  `json:"summary"`
	Buckets            []XCodeUsageBucket `json:"buckets"`
	Models             []XCodeModelUsage  `json:"models"`
	UnsupportedMetrics []string           `json:"unsupported_metrics"`
}

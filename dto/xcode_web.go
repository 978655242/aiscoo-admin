package dto

type XCodeWebHandoffResponse struct {
	SchemaVersion int    `json:"schema_version"`
	ServerTime    int64  `json:"server_time"`
	URL           string `json:"url"`
	ExpiresAt     int64  `json:"expires_at"`
}

type XCodeWebView struct {
	SchemaVersion int                         `json:"schema_version"`
	ServerTime    int64                       `json:"server_time"`
	Plans         *XCodePlansSnapshot         `json:"plans"`
	Entitlements  *XCodeAuthorizationSnapshot `json:"entitlements"`
	Display       XCodeQuotaDisplay           `json:"display"`
}

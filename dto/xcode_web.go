package dto

type XCodeWebHandoffResponse struct {
	SchemaVersion int    `json:"schema_version"`
	ServerTime    int64  `json:"server_time"`
	URL           string `json:"url"`
	ExpiresAt     int64  `json:"expires_at"`
}

type XCodePlanDisplay struct {
	GroupKey   string   `json:"group_key"`
	GroupTitle string   `json:"group_title"`
	OfferKind  string   `json:"offer_kind"`
	Highlights []string `json:"highlights"`
	Features   []string `json:"features"`
}

type XCodePlanPresentation struct {
	PlanID           string            `json:"plan_id"`
	Metadata         *XCodePlanDisplay `json:"metadata"`
	Assignable       bool              `json:"assignable"`
	AssignmentReason *string           `json:"assignment_reason"`
	DisplayIssue     *string           `json:"display_issue"`
}

type XCodeWebView struct {
	SchemaVersion int                         `json:"schema_version"`
	ServerTime    int64                       `json:"server_time"`
	Plans         *XCodePlansSnapshot         `json:"plans"`
	Entitlements  *XCodeAuthorizationSnapshot `json:"entitlements"`
	Display       XCodeQuotaDisplay           `json:"display"`
	// XCODE: presentation metadata is isolated from the strict public plan contract.
	PlanPresentations []XCodePlanPresentation `json:"plan_presentations"`
}

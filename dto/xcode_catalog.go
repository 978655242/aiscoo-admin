package dto

// XCodeCatalogSnapshot contains only user-visible capabilities, never native rows.
type XCodeCatalogSnapshot struct {
	SchemaVersion         int                 `json:"schema_version"`
	ServerTime            int64               `json:"server_time"`
	CatalogRevision       string              `json:"catalog_revision"`
	AuthorizationRevision string              `json:"authorization_revision"`
	Models                []XCodeCatalogModel `json:"models"`
}

type XCodeCatalogModel struct {
	ModelID        string            `json:"model_id"`
	DisplayName    string            `json:"display_name"`
	Icon           *string           `json:"icon"`
	APIType        *string           `json:"api_type"`
	Enabled        bool              `json:"enabled"`
	DisabledReason *string           `json:"disabled_reason"`
	ModelConfig    *XCodeModelConfig `json:"model_config"`
}

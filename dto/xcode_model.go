package dto

const (
	XCodeModelSchemaVersion                  = 1
	XCodeAPITypeAnthropicMessages            = "anthropic-messages"
	XCodeAPITypeOpenAIChatCompletions        = "openai-chat-completions"
	XCodeAPITypeOpenAIResponses              = "openai-responses"
	XCodeModelConfigMaxBytes                 = 64 * 1024
	XCodeOptionMapMaxBytes                   = 8 * 1024
	XCodeMaxSafeInteger               uint64 = 9007199254740991
)

type XCodeModelMetadata struct {
	SchemaVersion int              `json:"schema_version"`
	APIType       string           `json:"api_type"`
	ModelConfig   XCodeModelConfig `json:"model_config"`
}

type XCodeModelConfig struct {
	Enabled     bool                  `json:"enabled"`
	Properties  XCodeModelProperties  `json:"properties"`
	OptionSpecs XCodeModelOptionSpecs `json:"optionSpecs"`
}

type XCodeModelProperties struct {
	RequiresMfjsToolSchema        bool                   `json:"requiresMfjsToolSchema"`
	ContextWindow                 uint64                 `json:"contextWindow"`
	InputFormat                   XCodeModelInputFormat  `json:"inputFormat"`
	OutputFormat                  XCodeModelOutputFormat `json:"outputFormat"`
	SupportsToolCall              bool                   `json:"supportsToolCall"`
	SupportsJsonSchemaOutput      bool                   `json:"supportsJsonSchemaOutput"`
	SupportsNativeWebSearch       bool                   `json:"supportsNativeWebSearch"`
	SupportsMidConversationSystem bool                   `json:"supportsMidConversationSystem"`
}

type XCodeModelInputFormat struct {
	SupportsText  bool `json:"supportsText"`
	SupportsImage bool `json:"supportsImage"`
	SupportsVideo bool `json:"supportsVideo"`
	SupportsAudio bool `json:"supportsAudio"`
	SupportsPdf   bool `json:"supportsPdf"`
}

type XCodeModelOutputFormat struct {
	SupportsText bool `json:"supportsText"`
}

type XCodeModelOptionSpecs struct {
	ReasoningLevel  XCodeModelReasoningLevel  `json:"reasoningLevel"`
	MaxOutputTokens XCodeModelMaxOutputTokens `json:"maxOutputTokens"`
}

type XCodeModelReasoningLevel struct {
	Values []string `json:"values"`
	Map    string   `json:"map"`
}

type XCodeModelMaxOutputTokens struct {
	Max uint64 `json:"max"`
	Map string `json:"map"`
}

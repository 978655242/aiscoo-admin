package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// ParseXCodeModelMetadata validates the complete Host configuration contract.
// CEL semantics remain the responsibility of XCode's restricted compiler.
func ParseXCodeModelMetadata(raw string) (*dto.XCodeModelMetadata, error) {
	if len(raw) == 0 || len(raw) > dto.XCodeModelConfigMaxBytes {
		return nil, errors.New("xcode_config must contain a JSON object of at most 64 KiB")
	}
	var root map[string]common.RawMessage
	if err := common.UnmarshalJsonStr(raw, &root); err != nil {
		return nil, fmt.Errorf("invalid xcode_config: %w", err)
	}
	var metadata dto.XCodeModelMetadata
	var config, properties, inputFormat, outputFormat, optionSpecs, reasoningLevel, maxOutputTokens common.RawMessage
	if err := decodeXCodeFields(root, map[string]any{
		"schema_version": &metadata.SchemaVersion, "api_type": &metadata.APIType, "model_config": &config,
	}); err != nil {
		return nil, err
	}
	if metadata.SchemaVersion != dto.XCodeModelSchemaVersion {
		return nil, errors.New("unsupported xcode_config schema_version")
	}
	switch metadata.APIType {
	case dto.XCodeAPITypeAnthropicMessages, dto.XCodeAPITypeOpenAIChatCompletions, dto.XCodeAPITypeOpenAIResponses:
	default:
		return nil, errors.New("invalid xcode_config api_type")
	}
	if err := decodeXCodeObject(config, map[string]any{
		"enabled": &metadata.ModelConfig.Enabled, "properties": &properties, "optionSpecs": &optionSpecs,
	}); err != nil {
		return nil, err
	}
	p := &metadata.ModelConfig.Properties
	if err := decodeXCodeObject(properties, map[string]any{
		"requiresMfjsToolSchema": &p.RequiresMfjsToolSchema, "contextWindow": &p.ContextWindow,
		"inputFormat": &inputFormat, "outputFormat": &outputFormat,
		"supportsToolCall": &p.SupportsToolCall, "supportsJsonSchemaOutput": &p.SupportsJsonSchemaOutput,
		"supportsNativeWebSearch": &p.SupportsNativeWebSearch, "supportsMidConversationSystem": &p.SupportsMidConversationSystem,
	}); err != nil {
		return nil, err
	}
	if p.ContextWindow == 0 || p.ContextWindow > dto.XCodeMaxSafeInteger {
		return nil, errors.New("contextWindow must be a positive JavaScript-safe integer")
	}
	if err := decodeXCodeObject(inputFormat, map[string]any{
		"supportsText": &p.InputFormat.SupportsText, "supportsImage": &p.InputFormat.SupportsImage,
		"supportsVideo": &p.InputFormat.SupportsVideo, "supportsAudio": &p.InputFormat.SupportsAudio,
		"supportsPdf": &p.InputFormat.SupportsPdf,
	}); err != nil {
		return nil, err
	}
	if err := decodeXCodeObject(outputFormat, map[string]any{"supportsText": &p.OutputFormat.SupportsText}); err != nil {
		return nil, err
	}
	if err := decodeXCodeObject(optionSpecs, map[string]any{"reasoningLevel": &reasoningLevel, "maxOutputTokens": &maxOutputTokens}); err != nil {
		return nil, err
	}
	o := &metadata.ModelConfig.OptionSpecs
	if err := decodeXCodeObject(reasoningLevel, map[string]any{"values": &o.ReasoningLevel.Values, "map": &o.ReasoningLevel.Map}); err != nil {
		return nil, err
	}
	if err := decodeXCodeObject(maxOutputTokens, map[string]any{"max": &o.MaxOutputTokens.Max, "map": &o.MaxOutputTokens.Map}); err != nil {
		return nil, err
	}
	if o.MaxOutputTokens.Max == 0 || o.MaxOutputTokens.Max > dto.XCodeMaxSafeInteger {
		return nil, errors.New("maxOutputTokens.max must be a positive JavaScript-safe integer")
	}
	if len(o.ReasoningLevel.Values) == 0 {
		return nil, errors.New("reasoningLevel.values must not be empty")
	}
	seen := make(map[string]bool, len(o.ReasoningLevel.Values))
	for _, value := range o.ReasoningLevel.Values {
		if strings.TrimSpace(value) == "" || seen[value] {
			return nil, errors.New("reasoningLevel.values must be unique nonblank strings")
		}
		seen[value] = true
	}
	for _, source := range []string{o.ReasoningLevel.Map, o.MaxOutputTokens.Map} {
		if strings.TrimSpace(source) == "" || len(source) > dto.XCodeOptionMapMaxBytes {
			return nil, errors.New("option maps must be nonblank strings of at most 8 KiB")
		}
	}
	return &metadata, nil
}

// Every field is required, including false booleans; null and unknown keys are invalid at every nesting level.
func decodeXCodeObject(raw common.RawMessage, fields map[string]any) error {
	if common.GetJsonType(raw) != "object" {
		return errors.New("xcode_config fields must contain complete JSON objects")
	}
	var object map[string]common.RawMessage
	if err := common.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("invalid xcode_config: %w", err)
	}
	return decodeXCodeFields(object, fields)
}

func decodeXCodeFields(object map[string]common.RawMessage, fields map[string]any) error {
	if object == nil {
		return errors.New("xcode_config must be a complete JSON object")
	}
	for name := range object {
		if _, known := fields[name]; !known {
			return fmt.Errorf("unknown xcode_config field: %s", name)
		}
	}
	for name, target := range fields {
		value, exists := object[name]
		if !exists || common.GetJsonType(value) == "null" {
			return fmt.Errorf("xcode_config field is required: %s", name)
		}
		if nested, ok := target.(*common.RawMessage); ok {
			*nested = value
			continue
		}
		if err := common.Unmarshal(value, target); err != nil {
			return fmt.Errorf("invalid xcode_config field %s: %w", name, err)
		}
	}
	return nil
}

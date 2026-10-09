package service

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

var ErrXCodeModelUnavailable = errors.New("XCode model is unavailable for this protocol")

// xcodeCatalogModel is shared by the catalog and targeted admission. Native
// prefix/contains/suffix description rules are not capability declarations.
func xcodeCatalogModel(modelID string, metadata *model.Model) dto.XCodeCatalogModel {
	entry := dto.XCodeCatalogModel{ModelID: modelID, DisplayName: modelID}
	reason := "model_metadata_missing"
	if metadata == nil || metadata.XCodeConfig == "" {
		entry.DisabledReason = &reason
		return entry
	}
	parsed, err := model.ParseXCodeModelMetadata(string(metadata.XCodeConfig))
	if err != nil {
		reason = "model_metadata_invalid"
		entry.DisabledReason = &reason
		return entry
	}
	entry.APIType, entry.ModelConfig = &parsed.APIType, &parsed.ModelConfig
	switch {
	case metadata.Status != 1:
		reason = "model_disabled"
	case !parsed.ModelConfig.Enabled:
		reason = "model_config_disabled"
	default:
		entry.Enabled = true
		return entry
	}
	entry.DisabledReason = &reason
	return entry
}

func xcodeRequestPath(apiType string) string {
	switch apiType {
	case dto.XCodeAPITypeAnthropicMessages:
		return "/v1/messages"
	case dto.XCodeAPITypeOpenAIChatCompletions:
		return "/v1/chat/completions"
	case dto.XCodeAPITypeOpenAIResponses:
		return "/v1/responses"
	default:
		return ""
	}
}

func GetXCodeModelCatalog(userID int) (*dto.XCodeCatalogSnapshot, error) {
	if userID <= 0 {
		return nil, model.ErrSubscriptionFundingTargetInvalid
	}
	var user model.User
	if err := model.DB.Select("id", "group").First(&user, userID).Error; err != nil {
		return nil, err
	}
	state, err := model.GetXCodeAuthorizationState(userID)
	if err != nil {
		return nil, err
	}
	abilities, err := model.GetXCodeAvailableAbilities(user.Group)
	if err != nil {
		return nil, err
	}
	// SQL collations differ across supported databases; authority is exact and
	// digest ordering must not depend on the database's collation.
	abilities = slices.DeleteFunc(abilities, func(ability model.XCodeAvailableAbility) bool { return ability.Group != user.Group })
	slices.SortFunc(abilities, func(a, b model.XCodeAvailableAbility) int {
		if order := cmp.Compare(a.Model, b.Model); order != 0 {
			return order
		}
		return cmp.Compare(a.ChannelId, b.ChannelId)
	})
	modelIDs := make([]string, 0, len(abilities))
	for _, ability := range abilities {
		if len(modelIDs) == 0 || modelIDs[len(modelIDs)-1] != ability.Model {
			modelIDs = append(modelIDs, ability.Model)
		}
	}
	var metadata []model.Model
	if len(modelIDs) > 0 {
		if err := model.DB.Select("model_name", "name_rule", "status", "x_code_config").
			Where("model_name IN ? AND name_rule = ?", modelIDs, model.NameRuleExact).Find(&metadata).Error; err != nil {
			return nil, err
		}
	}
	byName := make(map[string]*model.Model, len(metadata))
	for i := range metadata {
		byName[metadata[i].ModelName] = &metadata[i]
	}
	snapshot := &dto.XCodeCatalogSnapshot{
		SchemaVersion: dto.XCodeModelSchemaVersion, ServerTime: time.Now().UnixMilli(), AuthorizationRevision: "0",
		Models: make([]dto.XCodeCatalogModel, 0, len(modelIDs)),
	}
	invalidMetadata := make(map[string]string)
	nativeMetadataStatus := make(map[string]int, len(metadata))
	for _, modelID := range modelIDs {
		row := byName[modelID]
		if row != nil {
			nativeMetadataStatus[modelID] = row.Status
		}
		entry := xcodeCatalogModel(modelID, row)
		snapshot.Models = append(snapshot.Models, entry)
		if row != nil && entry.DisabledReason != nil && *entry.DisabledReason == "model_metadata_invalid" {
			digest := sha256.Sum256([]byte(row.XCodeConfig))
			invalidMetadata[modelID] = hex.EncodeToString(digest[:])
		}
	}
	// Hash normalized effective facts, not secrets, metadata prose or clocks.
	type routeFact struct {
		RequestPath, UpstreamPath, Converter, AuthType, AuthName string
		Eligible, PassThroughBody                                bool
	}
	type settingsFact struct {
		TaskPluginBindings                                                     []string
		ForceFormat, ThinkingToContent, PassThroughBody, SystemPromptOverride  bool
		AzureResponsesVersion, VertexKeyType, AwsKeyType, ToolLossPolicy       string
		OpenRouterEnterprise, ClaudeBetaQuery, OllamaOpenAIChat                bool
		AllowServiceTier, AllowInferenceGeo, AllowSpeed, AllowSafetyIdentifier bool
		DisableStore, AllowIncludeObfuscation                                  bool
	}
	type routingFact struct {
		Model                  string
		ChannelID, ChannelType int
		Priority               int64
		Weight                 uint
		Mapping                any
		Routes                 []routeFact
		Settings               settingsFact
	}
	facts := make([]routingFact, 0, len(abilities))
	eligibleModels := make(map[string]bool, len(modelIDs))
	modelIndex := 0
	for _, ability := range abilities {
		fact := routingFact{Model: ability.Model, ChannelID: ability.ChannelId, ChannelType: ability.ChannelType, Weight: ability.Weight, Routes: make([]routeFact, 0, 3)}
		channel := ability.RoutingChannel()
		setting, other := channel.GetSetting(), channel.GetOtherSettings()
		lossPolicy := strings.TrimSpace(other.ToolLossPolicy)
		if lossPolicy == "" {
			lossPolicy = "allow"
		}
		fact.Settings = settingsFact{
			TaskPluginBindings: setting.TaskPluginBindings(), ForceFormat: setting.ForceFormat, ThinkingToContent: setting.ThinkingToContent,
			PassThroughBody: setting.PassThroughBodyEnabled, SystemPromptOverride: setting.SystemPromptOverride,
			AzureResponsesVersion: other.AzureResponsesVersion, VertexKeyType: string(other.VertexKeyType), AwsKeyType: string(other.AwsKeyType), ToolLossPolicy: lossPolicy,
			OpenRouterEnterprise: other.IsOpenRouterEnterprise(), ClaudeBetaQuery: other.ClaudeBetaQuery, OllamaOpenAIChat: other.OllamaOpenAIChat,
			AllowServiceTier: other.AllowServiceTier, AllowInferenceGeo: other.AllowInferenceGeo, AllowSpeed: other.AllowSpeed,
			AllowSafetyIdentifier: other.AllowSafetyIdentifier, DisableStore: other.DisableStore, AllowIncludeObfuscation: other.AllowIncludeObfuscation,
		}
		for snapshot.Models[modelIndex].ModelID != ability.Model {
			modelIndex++
		}
		entry := snapshot.Models[modelIndex]
		for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
			eligible, _ := model.ChannelSatisfiesFilters(channel, ability.Model, []dto.ChannelFilter{{Kind: dto.FilterRequestPath, RequestPath: path}})
			route := routeFact{RequestPath: path, Eligible: eligible}
			if configured, matched := other.AdvancedCustom.MatchPathForModel(path, ability.Model); matched {
				// Route auth values and URL userinfo/query values can contain credentials.
				if target, err := url.Parse(strings.TrimSpace(configured.UpstreamPath)); err == nil {
					target.User, target.RawQuery, target.Fragment = nil, "", ""
					route.UpstreamPath = target.String()
				}
				route.Converter = strings.TrimSpace(configured.Converter)
				if route.Converter == "" {
					route.Converter = "none"
				}
				route.PassThroughBody = configured.PassThroughBodyEnabled
				if configured.Auth != nil {
					route.AuthType, route.AuthName = strings.TrimSpace(configured.Auth.Type), strings.TrimSpace(configured.Auth.Name)
				} else {
					route.AuthType, route.AuthName = "header", "Authorization"
				}
			}
			fact.Routes = append(fact.Routes, route)
			if eligible && entry.APIType != nil && xcodeRequestPath(*entry.APIType) == path {
				eligibleModels[ability.Model] = true
			}
		}
		if ability.Priority != nil {
			fact.Priority = *ability.Priority
		}
		if ability.ChannelModelMapping != nil && *ability.ChannelModelMapping != "" {
			var mapping map[string]string
			if common.UnmarshalJsonStr(*ability.ChannelModelMapping, &mapping) == nil {
				if len(mapping) > 0 {
					fact.Mapping = mapping
				}
			} else {
				fact.Mapping = *ability.ChannelModelMapping
			}
		}
		facts = append(facts, fact)
	}
	for i := range snapshot.Models {
		entry := &snapshot.Models[i]
		if entry.Enabled && !eligibleModels[entry.ModelID] {
			entry.Enabled = false
			reason := "model_route_unavailable"
			entry.DisabledReason = &reason
		}
	}
	type authorizationFact struct {
		Revision           int64
		SubscriptionID     int
		Status             string
		StartTime, EndTime int64
		AmountTotal        int64
		Exhausted          bool
	}
	authorization := authorizationFact{}
	if state.Authorization != nil {
		if state.Subscription == nil || state.Authorization.UserId != user.Id || state.Authorization.Revision <= 0 ||
			state.Subscription.Id != state.Authorization.SubscriptionId || state.Subscription.UserId != user.Id ||
			state.Subscription.UsageScope != model.SubscriptionUsageScopeXCode || state.Subscription.Source != "admin" ||
			state.Subscription.AmountTotal < 0 || state.Subscription.AmountUsed < 0 ||
			(state.Subscription.Status != "active" && state.Subscription.Status != "cancelled" && state.Subscription.Status != "expired") {
			return nil, model.ErrSubscriptionFundingTargetInvalid
		}
		sub := state.Subscription
		snapshot.AuthorizationRevision = strconv.FormatInt(state.Authorization.Revision, 10)
		authorization = authorizationFact{Revision: state.Authorization.Revision, SubscriptionID: sub.Id, Status: sub.Status, StartTime: sub.StartTime, EndTime: sub.EndTime, AmountTotal: sub.AmountTotal,
			Exhausted: sub.AmountTotal > 0 && sub.AmountUsed >= sub.AmountTotal}
		if sub.Status == "active" && sub.EndTime <= state.Now {
			authorization.Status = "expired"
		}
		if sub.Status == "active" && sub.StartTime > state.Now {
			authorization.Status = "not_started"
		}
	}
	normalized, err := common.Marshal(struct {
		UserID               int
		Group                string
		Abilities            []routingFact
		Models               []dto.XCodeCatalogModel
		InvalidMetadata      map[string]string
		NativeMetadataStatus map[string]int
		Authorization        authorizationFact
	}{user.Id, user.Group, facts, snapshot.Models, invalidMetadata, nativeMetadataStatus, authorization})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(normalized)
	snapshot.CatalogRevision = hex.EncodeToString(digest[:])
	return snapshot, nil
}

// ValidateXCodeModelRequest reads only the requested model's live authority. It
// deliberately leaves plan funding admission to the existing managed-key path.
// Success returns that authority's live group for managed distribution.
// For anthropic-messages the first pointer is max_tokens; a supplied legacy
// max_tokens_to_sample is additionally bounded but cannot replace that field.
func ValidateXCodeModelRequest(userID int, modelID, apiType string, maxTokens ...*uint) (string, error) {
	if userID <= 0 {
		return "", types.NewErrorWithStatusCode(ErrXCodeModelUnavailable, "XCODE_MODEL_UNAVAILABLE", http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	var user model.User
	if err := model.DB.Select("id", "group").First(&user, userID).Error; err != nil {
		return "", types.NewErrorWithStatusCode(ErrXCodeBackendUnavailable, "XCODE_BACKEND_UNAVAILABLE", http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	requestPath := xcodeRequestPath(apiType)
	if requestPath == "" {
		return "", types.NewErrorWithStatusCode(ErrXCodeModelUnavailable, "XCODE_MODEL_UNAVAILABLE", http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	available, err := model.HasXCodeAvailableModel(user.Group, modelID, requestPath)
	if err != nil {
		return "", types.NewErrorWithStatusCode(ErrXCodeBackendUnavailable, "XCODE_BACKEND_UNAVAILABLE", http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	if !available {
		return "", types.NewErrorWithStatusCode(ErrXCodeModelUnavailable, "XCODE_MODEL_UNAVAILABLE", http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	var candidates []model.Model
	err = model.DB.Select("model_name", "name_rule", "status", "x_code_config").
		Where("model_name = ? AND name_rule = ?", modelID, model.NameRuleExact).Find(&candidates).Error
	if err != nil {
		return "", types.NewErrorWithStatusCode(ErrXCodeBackendUnavailable, "XCODE_BACKEND_UNAVAILABLE", http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	var metadata *model.Model
	for i := range candidates {
		if candidates[i].ModelName == modelID {
			metadata = &candidates[i]
			break
		}
	}
	entry := xcodeCatalogModel(modelID, metadata)
	if !entry.Enabled || entry.APIType == nil || *entry.APIType != apiType {
		return "", types.NewErrorWithStatusCode(ErrXCodeModelUnavailable, "XCODE_MODEL_UNAVAILABLE", http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	supplied := false
	for _, value := range maxTokens {
		if value == nil {
			continue
		}
		supplied = true
		if uint64(*value) > entry.ModelConfig.OptionSpecs.MaxOutputTokens.Max {
			return "", types.NewErrorWithStatusCode(errors.New("output token limit exceeds this model's configured maximum"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	if !supplied || (apiType == dto.XCodeAPITypeAnthropicMessages && (len(maxTokens) == 0 || maxTokens[0] == nil)) {
		return "", types.NewErrorWithStatusCode(errors.New("an explicit output token limit is required"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return user.Group, nil
}

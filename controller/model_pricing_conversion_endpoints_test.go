package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturePricingSettlement records the actual charge produced by the public
// settlement path without depending on wallet or token funding.
type capturePricingSettlement struct{ quota int }

func (capture *capturePricingSettlement) Settle(quota int) error { capture.quota = quota; return nil }
func (*capturePricingSettlement) Refund(*gin.Context)            {}
func (*capturePricingSettlement) NeedsRefund() bool              { return false }
func (*capturePricingSettlement) GetPreConsumedQuota() int       { return 0 }
func (*capturePricingSettlement) Reserve(int) error              { return nil }
func (*capturePricingSettlement) ReserveBeforeSubmit(int) error  { return nil }

// Model metadata may declare endpoints either as a type array or as a path map.
// Conversion must understand both to avoid silently skipping endpoint rules.
func TestModelPricingConversionAcceptsDeclaredEndpointArray(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	previousQuota := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuota })

	for _, tc := range []struct {
		name        string
		endpoints   string
		draft       model.PricingValues
		wantImageCt bool
	}{
		{"array-endpoints-image", `["image-generation","openai"]`,
			model.PricingValues{"ModelRatio": 1.7875, "CompletionRatio": 6, "ImageRatio": 1.6}, false},
		{"array-endpoints-image-fixed", `["image-generation","openai"]`,
			model.PricingValues{"ModelPrice": 1}, true},
		{"array-endpoints-gemini", `["gemini","openai"]`,
			model.PricingValues{"ModelRatio": 0.10725, "CompletionRatio": 100}, false},
		{"array-endpoints-gemini-cache", `["gemini","openai"]`,
			model.PricingValues{"ModelRatio": 0.715, "CompletionRatio": 60, "CacheRatio": 0.1}, false},
		{"array-endpoints-openai-response", `["openai","openai-response","openai-response-compact","gemini"]`,
			model.PricingValues{"ModelRatio": 0.17875, "CompletionRatio": 120}, false},
		{"map-endpoints-image", `{"image-generation": {"path": "/v1/images/generations", "method": "POST"}}`,
			model.PricingValues{"ModelPrice": 1}, true},
		// Endpoint names must be trimmed before the switch matches them: a
		// padded name would otherwise match nothing and silently downgrade a
		// fixed-price image model to per-request billing.
		{"array-endpoints-image-padded", `[" image-generation ","openai"]`,
			model.PricingValues{"ModelPrice": 1}, true},
		{"map-endpoints-image-padded", `{"image-generation ": {"path": "/v1/images/generations", "method": "POST"}}`,
			model.PricingValues{"ModelPrice": 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Create(&model.Model{
				ModelName: tc.name, Endpoints: tc.endpoints, Status: 1, NameRule: model.NameRuleExact,
			}).Error)
			var response struct {
				Success bool
				Data    model.ModelPricingConversion
			}
			modelManagementRequest(t, PreviewModelPricingConversion, http.MethodPost,
				"/api/option/model_pricing/convert",
				map[string]any{"model_name": tc.name, "pricing": tc.draft}, &response)
			require.True(t, response.Success)
			t.Logf("PROBE %-34s expression=%q unsupported=%q image_count=%v",
				tc.name, response.Data.Expression, response.Data.UnsupportedReason,
				response.Data.BillingDetails.ImageCount)
			require.Empty(t, response.Data.UnsupportedReason)
			require.NotEmpty(t, response.Data.Expression)
			// Declared endpoint types must actually reach the endpoint switch;
			// an array of names read as array indices would silently skip it.
			assert.Equal(t, tc.wantImageCt, response.Data.BillingDetails.ImageCount,
				"declared image-generation endpoint must drive image-count billing")
		})
	}
}

func TestModelPricingConversionCacheSettlement(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	previousQuota, previousLog, previousBatch := common.QuotaPerUnit, common.LogConsumeEnabled, common.BatchUpdateEnabled
	common.QuotaPerUnit, common.LogConsumeEnabled, common.BatchUpdateEnabled = 500000, false, false
	t.Cleanup(func() {
		common.QuotaPerUnit, common.LogConsumeEnabled, common.BatchUpdateEnabled = previousQuota, previousLog, previousBatch
	})
	user := model.User{Username: "conversion-settlement", Status: common.UserStatusEnabled}
	channel := model.Channel{Name: "conversion-settlement", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&channel).Error)

	for _, tc := range []struct {
		name  string
		draft model.PricingValues
		usage dto.Usage
		group float64
		want  int
	}{
		{
			"gpt-conversion-12h", model.PricingValues{"ModelRatio": 5.0, "CompletionRatio": 5.0, "CacheRatio": 0.1, "CreateCacheRatio": 1.25},
			dto.Usage{PromptTokens: 12000, TotalTokens: 12000, PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 4000, CacheWriteTokens: 7000,
				CacheWriteTokensDetails: &dto.CacheWriteTokenDetails{TTL30mTokens: common.GetPointer(3000), TTL12hTokens: common.GetPointer(4000)},
			}}, 1, 50750,
		},
		{
			"gpt-conversion-default-write", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 2.0, "CacheRatio": 0.5},
			dto.Usage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 100, CacheWriteTokens: 200}}, 1, 2400,
		},
		{
			"gpt-conversion-free-write", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 2.0, "CacheRatio": 0.5, "CreateCacheRatio": 0.0},
			dto.Usage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 100, CacheWriteTokens: 200}}, 1, 1900,
		},
		{
			"gpt-conversion-overlap", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 2.0, "CacheRatio": 1.0, "CreateCacheRatio": 1.25},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 800, CacheWriteTokens: 500}}, 1, 2850,
		},
		{
			"image-cache-same-price", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 1.0, "ImageRatio": 1.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 800, ImageTokens: 500}}, 1, 2600,
		},
		{
			"image-cache-default-price", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 0.5},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 800, ImageTokens: 500}}, 1, 1800,
		},
		{
			"image-cache-free-image", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 1.0, "ImageRatio": 0.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 800, ImageTokens: 500}}, 1, 1600,
		},
		{
			"image-cache-free-cache", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 0.0, "ImageRatio": 1.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 800, ImageTokens: 500}}, 1, 1000,
		},
		{
			"image-write-same-price", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 1.0, "CreateCacheRatio": 1.25, "ImageRatio": 1.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CacheWriteTokens: 800, ImageTokens: 500}}, 1, 3000,
		},
		{
			"image-write-free", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 1.0, "CreateCacheRatio": 0.0, "ImageRatio": 1.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CacheWriteTokens: 800, ImageTokens: 500}}, 1, 1000,
		},
		{
			"image-cache-write-overlap", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 1.0, "CreateCacheRatio": 1.25, "ImageRatio": 1.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 400, CacheWriteTokens: 400, ImageTokens: 300, CachedTokensDetails: &dto.CachedTokenDetails{ImageTokens: common.GetPointer(100)}}}, 1.5, 3600,
		},
		{
			"image-cache-no-overlap", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 1.0, "ImageRatio": 1.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 200, ImageTokens: 100}}, 1, 2000,
		},
		{
			"image-free-no-overlap", model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 1.0, "CacheRatio": 0.5, "CreateCacheRatio": 0.0, "ImageRatio": 0.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 200, CacheWriteTokens: 100, ImageTokens: 100}}, 1, 1400,
		},
		{
			"zero-input-price", model.PricingValues{"ModelRatio": 0.0, "CompletionRatio": 1.0, "CacheRatio": 1.0, "ImageRatio": 1.0},
			dto.Usage{PromptTokens: 1000, TotalTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 800, ImageTokens: 500}}, 1, 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			converted, err := model.PreviewModelPricingConversion(tc.name, tc.draft)
			require.NoError(t, err)
			require.Empty(t, converted.UnsupportedReason)
			require.NotEmpty(t, converted.Expression)
			assert.False(t, billingexpr.UsedVars(converted.Expression)["cc12h"])
			assert.False(t, billingexpr.UsedVars(converted.Expression)["img_cr"])

			price := hosttypes.PriceData{ModelRatio: converted.Effective["ModelRatio"].(float64), CompletionRatio: converted.Effective["CompletionRatio"].(float64), CacheRatio: converted.Effective["CacheRatio"].(float64), CacheCreationRatio: converted.Effective["CreateCacheRatio"].(float64), ImageRatio: converted.Effective["ImageRatio"].(float64), GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: tc.group}}
			for _, expression := range []string{"", converted.Expression} {
				capture := &capturePricingSettlement{}
				info := &relaycommon.RelayInfo{UserId: user.Id, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id}, OriginModelName: tc.name, RelayFormat: types.RelayFormatOpenAI, UsingGroup: "default", StartTime: time.Now(), PriceData: price, Billing: capture}
				if expression != "" {
					info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: tc.group, QuotaPerUnit: common.QuotaPerUnit}
				}
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				service.PostTextConsumeQuota(ctx, info, &tc.usage, nil)
				require.Nil(t, info.BillingSettlementFailure)
				assert.Equal(t, tc.want, capture.quota, "settlement expression %q", expression)
			}
		})
	}
}

func TestClaudeCacheRemainderMakesLegacyConversionUnsafe(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	previousQuota, previousLog, previousBatch := common.QuotaPerUnit, common.LogConsumeEnabled, common.BatchUpdateEnabled
	common.QuotaPerUnit, common.LogConsumeEnabled, common.BatchUpdateEnabled = 500000, false, false
	t.Cleanup(func() {
		common.QuotaPerUnit, common.LogConsumeEnabled, common.BatchUpdateEnabled = previousQuota, previousLog, previousBatch
	})
	user := model.User{Username: "claude-settlement", Status: common.UserStatusEnabled}
	channel := model.Channel{Name: "claude-settlement", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&channel).Error)

	name := "claude-conversion-unsafe"
	draft := model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 2.0, "CacheRatio": 0.5, "CreateCacheRatio": 1.25}
	conversion, err := model.PreviewModelPricingConversion(name, draft)
	require.NoError(t, err)
	require.Empty(t, conversion.Expression)
	require.Contains(t, conversion.UnsupportedReason, "Claude cache-write totals and TTL splits")
	// This was the generated expression before the safety check.
	candidate := `tier("base", p * 4 + c * 8 + cr * 2 + cc * 5 + cc1h * 8)`
	for _, tc := range []struct {
		name                      string
		total, five, one          int
		legacyQuota, convertQuota int
	}{
		{"no TTL split", 100, 0, 0, 2350, 2100},
		{"partial TTL split", 100, 20, 30, 2395, 2270},
		{"complete TTL split", 100, 40, 60, 2440, 2440},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := dto.Usage{PromptTokens: 1000, TotalTokens: 1000, UsageSemantic: "anthropic", ClaudeCacheCreation5mTokens: tc.five, ClaudeCacheCreation1hTokens: tc.one, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 100, CachedCreationTokens: tc.total}}
			price := hosttypes.PriceData{ModelRatio: 2, CompletionRatio: 2, CacheRatio: 0.5, CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2, ImageRatio: 1, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}}
			for _, check := range []struct {
				expression string
				want       int
			}{{"", tc.legacyQuota}, {candidate, tc.convertQuota}} {
				capture := &capturePricingSettlement{}
				info := &relaycommon.RelayInfo{UserId: user.Id, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id}, OriginModelName: name, RelayFormat: types.RelayFormatClaude, UsingGroup: "default", StartTime: time.Now(), PriceData: price, Billing: capture}
				if check.expression != "" {
					info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: check.expression, ExprHash: billingexpr.ExprHashString(check.expression), GroupRatio: 1, QuotaPerUnit: common.QuotaPerUnit}
				}
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				service.PostTextConsumeQuota(ctx, info, &usage, nil)
				require.Nil(t, info.BillingSettlementFailure)
				assert.Equal(t, check.want, capture.quota, "settlement expression %q", check.expression)
			}
		})
	}
}

func TestModelPricingConversionRejectsClaudeCacheMismatch(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	previousQuota := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuota })

	for _, tc := range []struct {
		name       string
		channel    *model.Channel
		endpoints  string
		wantReject bool
	}{
		{name: "claude-named-route", wantReject: true},
		{name: "opaque-anthropic", channel: &model.Channel{Type: constant.ChannelTypeAnthropic}, wantReject: true},
		{name: "opaque-bedrock", channel: &model.Channel{Type: constant.ChannelTypeAws}, wantReject: true},
		{name: "opaque-compatible", channel: &model.Channel{Type: constant.ChannelTypeNewAPI}, wantReject: true},
		{name: "opaque-sub2api", channel: &model.Channel{Type: constant.ChannelTypeSub2API}, wantReject: true},
		{name: "opaque-metadata", channel: &model.Channel{Type: constant.ChannelTypeOpenAI}, endpoints: `["openai","anthropic"]`, wantReject: true},
		{name: "opaque-hop", channel: &model.Channel{Type: constant.ChannelTypeOpenAI, ModelMapping: common.GetPointer(`{"opaque-hop":"intermediate","intermediate":"vendor/claude-sonnet"}`)}, wantReject: true},
		{name: "opaque-router-alias", channel: &model.Channel{Type: constant.ChannelTypeOpenRouter, ModelMapping: common.GetPointer(`{"opaque-router-alias":"vendor/claude-sonnet"}`)}, wantReject: true},
		{name: "plain-openai", channel: &model.Channel{Type: constant.ChannelTypeOpenAI}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.channel != nil {
				tc.channel.Name, tc.channel.Models, tc.channel.Status = tc.name, tc.name, common.ChannelStatusEnabled
				require.NoError(t, tc.channel.Insert())
			}
			if tc.endpoints != "" {
				require.NoError(t, db.Create(&model.Model{ModelName: tc.name, NameRule: model.NameRuleExact, Endpoints: tc.endpoints}).Error)
			}
			draft := model.PricingValues{"ModelRatio": 2.0, "CompletionRatio": 2.0, "CacheRatio": 0.5}
			conversion, err := model.PreviewModelPricingConversion(tc.name, draft)
			require.NoError(t, err)
			if tc.wantReject {
				assert.Empty(t, conversion.Expression)
				assert.Contains(t, conversion.UnsupportedReason, "Claude cache-write totals and TTL splits")
			} else {
				assert.Empty(t, conversion.UnsupportedReason)
				assert.NotEmpty(t, conversion.Expression)
			}
			if tc.name == "opaque-anthropic" {
				// An explicit zero cache-write rate removes the unobservable remainder's cost.
				draft["CreateCacheRatio"] = 0.0
				free, err := model.PreviewModelPricingConversion(tc.name, draft)
				require.NoError(t, err)
				assert.Empty(t, free.UnsupportedReason)
				assert.Contains(t, free.Expression, "cc * 0")
			}
		})
	}
}

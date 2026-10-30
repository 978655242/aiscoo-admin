package dto

import (
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeClaudeUsageCacheCreationReplacesWholeObject(t *testing.T) {
	t.Parallel()

	merged := MergeClaudeUsageNonZero(
		&ClaudeUsage{
			CacheCreation: &ClaudeCacheCreationUsage{Ephemeral1hInputTokens: 1000},
		},
		&ClaudeUsage{
			CacheCreation: &ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 1000,
				Ephemeral1hInputTokens: 0,
			},
		},
	)

	require.NotNil(t, merged.CacheCreation)
	assert.Equal(t, 1000, merged.CacheCreation.Ephemeral5mInputTokens)
	assert.Equal(t, 0, merged.CacheCreation.Ephemeral1hInputTokens)
}

func TestMergeGeminiUsageMetadataCandidatesAndThoughtsReplacedAsPair(t *testing.T) {
	t.Parallel()

	merged := MergeGeminiUsageMetadataNonZero(
		&GeminiUsageMetadata{
			PromptTokenCount:   10,
			ThoughtsTokenCount: 100,
		},
		&GeminiUsageMetadata{
			PromptTokenCount:     10,
			CandidatesTokenCount: 150,
			ThoughtsTokenCount:   0,
			TotalTokenCount:      160,
		},
	)
	require.NotNil(t, merged)
	assert.Equal(t, 150, merged.CandidatesTokenCount)
	assert.Equal(t, 0, merged.ThoughtsTokenCount)

	billing := NewGeminiChatBillingUsage(merged)
	usage, ok := billing.CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, 150, usage.CompletionTokens)
}

func TestGeminiModalityKeysSettleConsistentlyAndDuplicateEntriesSum(t *testing.T) {
	t.Parallel()

	for _, modality := range []string{"audio", " AUDIO ", "AUDIO"} {
		t.Run("settle_"+modality, func(t *testing.T) {
			t.Parallel()
			billing := NewGeminiChatBillingUsage(&GeminiUsageMetadata{
				PromptTokenCount: 100,
				PromptTokensDetails: []GeminiPromptTokensDetails{
					{Modality: modality, TokenCount: 40},
					{Modality: "TEXT", TokenCount: 60},
				},
			})
			usage, ok := billing.CanonicalUsage()
			require.True(t, ok)
			assert.Equal(t, 40, usage.PromptTokensDetails.AudioTokens)
			assert.Equal(t, 60, usage.PromptTokensDetails.TextTokens)
		})
	}

	// A case variant in a later cumulative frame is the same modality and
	// replaces the earlier value.
	mergedDetails := mergeGeminiTokenDetails(
		[]GeminiPromptTokensDetails{{Modality: "AUDIO", TokenCount: 10}},
		[]GeminiPromptTokensDetails{{Modality: "audio", TokenCount: 15}},
	)
	assert.Equal(t, []GeminiPromptTokensDetails{{Modality: "audio", TokenCount: 15}}, mergedDetails)

	// Duplicate entries inside one snapshot settle as their sum, and merging
	// that snapshot again (every frame repeats it) does not add it twice.
	snapshot := &GeminiUsageMetadata{
		PromptTokenCount: 25,
		PromptTokensDetails: []GeminiPromptTokensDetails{
			{Modality: "AUDIO", TokenCount: 10},
			{Modality: "audio", TokenCount: 15},
		},
	}
	streamMerged := MergeGeminiUsageMetadataNonZero(MergeGeminiUsageMetadataNonZero(nil, snapshot), snapshot)
	require.NotNil(t, streamMerged)
	streamUsage, ok := NewGeminiChatBillingUsage(streamMerged).CanonicalUsage()
	require.True(t, ok)

	decodedUsage, ok := NewGeminiChatBillingUsage(snapshot).CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, 25, decodedUsage.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 25, streamUsage.PromptTokensDetails.AudioTokens)
}

func TestMergeGeminiUsageMetadataStreamDetailsAreCumulativeSnapshots(t *testing.T) {
	t.Parallel()

	prompt := []GeminiPromptTokensDetails{
		{Modality: "IMAGE", TokenCount: 1089},
		{Modality: "TEXT", TokenCount: 42},
	}
	frames := []*GeminiUsageMetadata{
		{PromptTokenCount: 1131, TotalTokenCount: 1131, PromptTokensDetails: prompt},
		{
			PromptTokenCount:           1131,
			ToolUsePromptTokenCount:    7,
			CandidatesTokenCount:       5,
			TotalTokenCount:            1143,
			PromptTokensDetails:        prompt,
			ToolUsePromptTokensDetails: []GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 7}},
			CandidatesTokensDetails:    []GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 5}},
		},
		{
			PromptTokenCount:           1131,
			ToolUsePromptTokenCount:    7,
			CandidatesTokenCount:       1310,
			ThoughtsTokenCount:         76,
			TotalTokenCount:            2524,
			PromptTokensDetails:        prompt,
			ToolUsePromptTokensDetails: []GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 7}},
			CandidatesTokensDetails: []GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 20},
				{Modality: "IMAGE", TokenCount: 1290},
			},
		},
		// A trailing frame without details keeps the last reported ones.
		{PromptTokenCount: 1131, CandidatesTokenCount: 1310, ThoughtsTokenCount: 76, TotalTokenCount: 2524},
	}
	var merged *GeminiUsageMetadata
	for _, frame := range frames {
		merged = MergeGeminiUsageMetadataNonZero(merged, frame)
	}
	require.NotNil(t, merged)
	assert.Equal(t, prompt, merged.PromptTokensDetails)
	assert.Equal(t, []GeminiPromptTokensDetails{{Modality: "TEXT", TokenCount: 7}}, merged.ToolUsePromptTokensDetails)
	assert.Equal(t, []GeminiPromptTokensDetails{
		{Modality: "TEXT", TokenCount: 20},
		{Modality: "IMAGE", TokenCount: 1290},
	}, merged.CandidatesTokensDetails)

	streamUsage, ok := NewGeminiChatBillingUsage(merged).CanonicalUsage()
	require.True(t, ok)
	nonStreamUsage, ok := NewGeminiChatBillingUsage(frames[2]).CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, nonStreamUsage, streamUsage)
	assert.Equal(t, 1089, streamUsage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 1290, streamUsage.CompletionTokenDetails.ImageTokens)

	// Converted streams remember the same cumulative sidecar at several
	// pipeline steps; repeated merges must not change the result.
	sidecar := NewGeminiChatBillingUsage(merged)
	again := MergeBillingUsageNonZero(MergeBillingUsageNonZero(sidecar, sidecar), sidecar)
	repeatedUsage, ok := again.CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, nonStreamUsage, repeatedUsage)
}

func TestClaudeCacheCreationSubObjectZeroDoesNotReviveFlatLegacyFields(t *testing.T) {
	t.Parallel()

	merged := MergeClaudeUsageNonZero(
		&ClaudeUsage{ClaudeCacheCreation1hTokens: 1000},
		&ClaudeUsage{
			InputTokens: 10,
			CacheCreation: &ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 1000,
				Ephemeral1hInputTokens: 0,
			},
		},
	)
	require.NotNil(t, merged.CacheCreation)
	assert.Equal(t, 1000, merged.CacheCreation.Ephemeral5mInputTokens)
	assert.Equal(t, 0, merged.CacheCreation.Ephemeral1hInputTokens)
	assert.Equal(t, 1000, merged.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 0, merged.ClaudeCacheCreation1hTokens)

	usage, ok := NewClaudeMessagesBillingUsage(merged).CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, 1000, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 0, usage.ClaudeCacheCreation1hTokens)
}

func TestClaudeCacheCreationFlatFieldsStillSettleWhenSnapshotNeverHadSubObject(t *testing.T) {
	t.Parallel()

	usage, ok := NewClaudeMessagesBillingUsage(&ClaudeUsage{
		InputTokens:                 10,
		ClaudeCacheCreation1hTokens: 1000,
	}).CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, 0, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 1000, usage.ClaudeCacheCreation1hTokens)
}

func TestMergeBillingUsageORsEstimatedOnSameAndCrossDialect(t *testing.T) {
	t.Parallel()

	estimated := NewEstimatedGeminiChatBillingUsage(&Usage{PromptTokens: 10, CompletionTokens: 2})
	require.NotNil(t, estimated)
	require.True(t, estimated.Estimated)

	sameDialect := MergeBillingUsageNonZero(estimated, NewGeminiChatBillingUsage(&GeminiUsageMetadata{
		PromptTokenCount:     11,
		CandidatesTokenCount: 3,
		TotalTokenCount:      14,
	}))
	require.NotNil(t, sameDialect)
	assert.True(t, sameDialect.Estimated)

	crossDialect := MergeBillingUsageNonZero(estimated, NewOpenAIChatBillingUsage(&Usage{
		PromptTokens:     12,
		CompletionTokens: 4,
		TotalTokens:      16,
	}))
	require.NotNil(t, crossDialect)
	assert.True(t, crossDialect.Estimated)
	require.NotNil(t, crossDialect.OpenAIUsage)
	assert.Equal(t, 12, crossDialect.OpenAIUsage.PromptTokens)
}

func TestMergeClaudeUsageNonZeroPreservesBillingUsage(t *testing.T) {
	t.Parallel()

	currentSidecar := NewGeminiChatBillingUsage(&GeminiUsageMetadata{
		PromptTokenCount:        3868,
		TotalTokenCount:         3868,
		CachedContentTokenCount: 20,
	})
	incomingSidecar := NewGeminiChatBillingUsage(&GeminiUsageMetadata{
		PromptTokenCount:     3868,
		CandidatesTokenCount: 12,
		TotalTokenCount:      3880,
	})
	require.NotNil(t, currentSidecar)
	require.NotNil(t, incomingSidecar)

	withIncoming := MergeClaudeUsageNonZero(
		&ClaudeUsage{
			InputTokens:          3868,
			CacheReadInputTokens: 20,
			BillingUsage:         currentSidecar,
		},
		&ClaudeUsage{
			InputTokens:  3868,
			OutputTokens: 12,
			BillingUsage: incomingSidecar,
		},
	)
	require.NotNil(t, withIncoming.BillingUsage)
	assert.Equal(t, BillingUsageSourceGeminiChat, withIncoming.BillingUsage.Source)
	assert.Equal(t, BillingUsageSemanticGemini, withIncoming.BillingUsage.Semantic)
	require.NotNil(t, withIncoming.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, 12, withIncoming.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 20, withIncoming.CacheReadInputTokens)
	assert.NotSame(t, incomingSidecar, withIncoming.BillingUsage)

	keepCurrent := MergeClaudeUsageNonZero(
		&ClaudeUsage{
			InputTokens:          3868,
			CacheReadInputTokens: 20,
			BillingUsage:         currentSidecar,
		},
		&ClaudeUsage{InputTokens: 3868, OutputTokens: 12},
	)
	require.NotNil(t, keepCurrent.BillingUsage)
	require.NotNil(t, keepCurrent.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, 20, keepCurrent.BillingUsage.GeminiUsageMetadata.CachedContentTokenCount)
	assert.Equal(t, 0, keepCurrent.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 20, keepCurrent.CacheReadInputTokens)
}

func TestMergeUsageNonZeroKeepsPositiveValuesAndTakesMaxTotal(t *testing.T) {
	t.Parallel()

	merged := MergeUsageNonZero(
		&Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		&Usage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 20},
	)

	require.NotNil(t, merged)
	assert.Equal(t, 10, merged.PromptTokens)
	assert.Equal(t, 5, merged.CompletionTokens)
	assert.Equal(t, 20, merged.TotalTokens)
}

// Synthetic compatible snapshots exercise fieldwise updates, including cases
// not documented as actual provider stream behavior.
func TestMergeUsageCacheWriteTTLSparseSnapshots(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"prompt_tokens_details", "input_tokens_details"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			cases := []struct {
				name        string
				updates     []string
				short, long int
			}{
				{"clear 12h", []string{`{"ttl_30m_tokens":1000,"ttl_12h_tokens":0}`}, 1000, 0},
				{"clear 30m only", []string{`{"ttl_30m_tokens":0}`}, 0, 700},
				{"clear 12h only", []string{`{"ttl_12h_tokens":0}`}, 300, 0},
				{"replace 12h only", []string{`{"ttl_12h_tokens":800}`}, 300, 800},
				{"replace 30m only", []string{`{"ttl_30m_tokens":200}`}, 200, 700},
				{"empty then no breakdown", []string{`{}`, "absent"}, 300, 700},
				{"null is not a number", []string{`{"ttl_12h_tokens":null}`}, 300, 700},
				{"clear then omit", []string{`{"ttl_12h_tokens":0}`, "absent"}, 300, 0},
				{"negative update is bounded by billing", []string{`{"ttl_12h_tokens":-1}`}, 300, -1},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					var current Usage
					require.NoError(t, kitutil.Unmarshal([]byte(`{"`+field+`":{"cache_write_tokens":1000,"cache_write_tokens_details":{"ttl_30m_tokens":300,"ttl_12h_tokens":700}}}`), &current))
					for _, update := range tc.updates {
						var incoming Usage
						raw := `{"` + field + `":{"cache_write_tokens_details":` + update + `}}`
						if update == "absent" {
							raw = `{"` + field + `":{"cache_write_tokens":1000}}`
						}
						require.NoError(t, kitutil.Unmarshal([]byte(raw), &incoming))
						MergeUsageNonZero(&current, &incoming)
					}
					details := &current.PromptTokensDetails
					if field == "input_tokens_details" {
						details = current.InputTokensDetails
					}
					require.NotNil(t, details)
					require.NotNil(t, details.CacheWriteTokensDetails)
					require.NotNil(t, details.CacheWriteTokensDetails.TTL30mTokens)
					require.NotNil(t, details.CacheWriteTokensDetails.TTL12hTokens)
					assert.Equal(t, tc.short, *details.CacheWriteTokensDetails.TTL30mTokens)
					assert.Equal(t, tc.long, *details.CacheWriteTokensDetails.TTL12hTokens)
					assert.Equal(t, min(max(tc.long, 0), 1000), details.CacheWrite12hTokens())
				})
			}
		})
	}
}

func TestCacheWriteTTLMergeAndCloneIsolation(t *testing.T) {
	t.Parallel()

	original := InputTokenDetails{CacheWriteTokensDetails: &CacheWriteTokenDetails{
		TTL30mTokens: kitutil.GetPointer(300), TTL12hTokens: kitutil.GetPointer(700),
	}}
	current := &Usage{PromptTokensDetails: original}
	incoming := &Usage{PromptTokensDetails: InputTokenDetails{CacheWriteTokensDetails: &CacheWriteTokenDetails{TTL30mTokens: kitutil.GetPointer(0)}}}
	MergeUsageNonZero(current, incoming)
	*incoming.PromptTokensDetails.CacheWriteTokensDetails.TTL30mTokens = 1
	*original.CacheWriteTokensDetails.TTL12hTokens = 2
	assert.Zero(t, *current.PromptTokensDetails.CacheWriteTokensDetails.TTL30mTokens)
	assert.Equal(t, 700, *current.PromptTokensDetails.CacheWriteTokensDetails.TTL12hTokens)

	clone := current.PromptTokensDetails.Clone()
	*clone.CacheWriteTokensDetails.TTL30mTokens = 3
	*clone.CacheWriteTokensDetails.TTL12hTokens = 4
	assert.Zero(t, *current.PromptTokensDetails.CacheWriteTokensDetails.TTL30mTokens)
	assert.Equal(t, 700, *current.PromptTokensDetails.CacheWriteTokensDetails.TTL12hTokens)
}

func TestMergeUsageSparseCacheWriteRequiresResponsesOrigin(t *testing.T) {
	t.Parallel()

	snapshot := &Usage{InputTokens: 1000, InputTokensDetails: &InputTokenDetails{
		CacheWriteTokens: 1000, CacheWriteTokensDetails: &CacheWriteTokenDetails{TTL12hTokens: kitutil.GetPointer(1000)},
	}}
	for _, tc := range []struct {
		name, source, semantic string
		want12h                int
	}{
		{"missing origin", "", "", 1000},
		{"different source", BillingUsageSourceOAIChat, BillingUsageSemanticOpenAI, 1000},
		{"different semantic", BillingUsageSourceOAIResponses, BillingUsageSemanticGemini, 1000},
		{"responses origin", BillingUsageSourceOAIResponses, BillingUsageSemanticOpenAI, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := &Usage{BillingUsage: NewOpenAIResponsesBillingUsage(snapshot)}
			original := current.BillingUsage
			incoming := &Usage{UsageSource: tc.source, UsageSemantic: tc.semantic,
				InputTokensDetails: &InputTokenDetails{CacheWriteTokensDetails: &CacheWriteTokenDetails{TTL12hTokens: kitutil.GetPointer(0)}}}
			MergeUsageNonZero(current, incoming)
			canonical, ok := current.BillingUsage.CanonicalUsage()
			require.True(t, ok)
			assert.Equal(t, tc.want12h, canonical.PromptTokensDetails.CacheWrite12hTokens())
			assert.Equal(t, 1000, *original.OpenAIUsage.InputTokensDetails.CacheWriteTokensDetails.TTL12hTokens)
			*incoming.InputTokensDetails.CacheWriteTokensDetails.TTL12hTokens = 9
			canonical, ok = current.BillingUsage.CanonicalUsage()
			require.True(t, ok)
			assert.Equal(t, tc.want12h, canonical.PromptTokensDetails.CacheWrite12hTokens())
		})
	}
}

func TestCacheWriteTTLJSONCompatibility(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, raw, encoded string
	}{
		{"full", `{"ttl_30m_tokens":300,"ttl_12h_tokens":700}`, `{"ttl_30m_tokens":300,"ttl_12h_tokens":700}`},
		{"sparse", `{"ttl_12h_tokens":700}`, `{"ttl_12h_tokens":700}`},
		{"sparse explicit zero", `{"ttl_30m_tokens":0}`, `{"ttl_30m_tokens":0}`},
		{"empty", `{}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var details CacheWriteTokenDetails
			require.NoError(t, kitutil.Unmarshal([]byte(tc.raw), &details))
			encoded, err := kitutil.Marshal(details)
			require.NoError(t, err)
			assert.JSONEq(t, tc.encoded, string(encoded))
		})
	}
	encoded, err := kitutil.Marshal(CacheWriteTokenDetails{TTL12hTokens: kitutil.GetPointer(700)})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ttl_12h_tokens":700}`, string(encoded))

	// Forwarding a sparse update through JSON must not synthesize a zero for
	// the unreported lifetime when the next hop merges it.
	var sparse Usage
	require.NoError(t, kitutil.Unmarshal([]byte(`{"input_tokens_details":{"cache_write_tokens_details":{"ttl_30m_tokens":0}}}`), &sparse))
	encoded, err = kitutil.Marshal(sparse)
	require.NoError(t, err)
	var decoded Usage
	require.NoError(t, kitutil.Unmarshal(encoded, &decoded))
	current := &Usage{InputTokensDetails: &InputTokenDetails{CacheWriteTokensDetails: &CacheWriteTokenDetails{TTL30mTokens: kitutil.GetPointer(300), TTL12hTokens: kitutil.GetPointer(700)}}}
	MergeUsageNonZero(current, &decoded)
	require.NotNil(t, current.InputTokensDetails.CacheWriteTokensDetails.TTL30mTokens)
	require.NotNil(t, current.InputTokensDetails.CacheWriteTokensDetails.TTL12hTokens)
	assert.Zero(t, *current.InputTokensDetails.CacheWriteTokensDetails.TTL30mTokens)
	assert.Equal(t, 700, *current.InputTokensDetails.CacheWriteTokensDetails.TTL12hTokens)
}

func TestInputTokenDetailsCacheCreationInputTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		raw   string
		total int
	}{
		{
			name:  "scalar only",
			raw:   `{"cached_tokens":1200,"cache_creation_input_tokens":300}`,
			total: 300,
		},
		{
			name:  "scalar larger than cache write",
			raw:   `{"cache_write_tokens":250,"cache_creation_input_tokens":400}`,
			total: 400,
		},
		{
			name:  "cache write larger than scalar",
			raw:   `{"cache_write_tokens":250,"cache_creation_input_tokens":100}`,
			total: 250,
		},
		{
			name:  "scalar larger than cached creation",
			raw:   `{"cached_creation_tokens":300,"cache_creation_input_tokens":400}`,
			total: 400,
		},
		{
			name:  "negative scalar ignored beside cache write",
			raw:   `{"cache_write_tokens":80,"cache_creation_input_tokens":-1}`,
			total: 80,
		},
		{
			name:  "only negative scalar",
			raw:   `{"cache_creation_input_tokens":-5}`,
			total: 0,
		},
		{
			name:  "undocumented creation object ignored",
			raw:   `{"cached_tokens":1200,"cache_creation":{"ephemeral_5m_input_tokens":300}}`,
			total: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var details InputTokenDetails
			require.NoError(t, kitutil.Unmarshal([]byte(tc.raw), &details))
			assert.Equal(t, tc.total, details.CacheCreationTokensTotal())
		})
	}

	var withCreation Usage
	require.NoError(t, kitutil.Unmarshal([]byte(`{"prompt_tokens":1510,"completion_tokens":20,"total_tokens":1530,"prompt_tokens_details":{"cached_tokens":1200,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":300}}}`), &withCreation))
	assert.Equal(t, 1510, withCreation.PromptTokens)
	assert.Equal(t, 1200, withCreation.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 300, withCreation.PromptTokensDetails.CacheCreationInputTokens)
	assert.Equal(t, 300, withCreation.PromptTokensDetails.CacheCreationTokensTotal())
	assert.Zero(t, withCreation.PromptTokensDetails.CachedCreationTokens)
	assert.Zero(t, withCreation.PromptTokensDetails.CacheWriteTokens)
	assert.Zero(t, withCreation.ClaudeCacheCreation5mTokens)
	assert.Zero(t, withCreation.ClaudeCacheCreation1hTokens)

	encoded, err := kitutil.Marshal(withCreation)
	require.NoError(t, err)
	body := string(encoded)
	assert.Contains(t, body, `"cache_creation_input_tokens":300`)
	assert.NotContains(t, body, `"cache_creation":`)
	assert.NotContains(t, body, "ephemeral_5m_input_tokens")
	assert.NotContains(t, body, "cache_write_tokens")
	assert.NotContains(t, body, "cached_creation_tokens")
	assert.Contains(t, body, `"cached_tokens":1200`)
	assert.Contains(t, body, `"prompt_tokens":1510`)

	var withoutCreation Usage
	require.NoError(t, kitutil.Unmarshal([]byte(`{"prompt_tokens":1510,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":1200}}`), &withoutCreation))
	encoded, err = kitutil.Marshal(withoutCreation)
	require.NoError(t, err)
	absent := string(encoded)
	assert.NotContains(t, absent, "cache_creation_input_tokens")
	assert.NotContains(t, absent, `"cache_creation":`)
	assert.NotContains(t, absent, "ephemeral_5m_input_tokens")
	assert.NotContains(t, absent, "cache_write_tokens")
	assert.NotContains(t, absent, "cached_creation_tokens")
}

func TestCacheCreationInputTokensRoundTrip(t *testing.T) {
	t.Parallel()

	var usage Usage
	require.NoError(t, kitutil.Unmarshal([]byte(`{"prompt_tokens":1510,"prompt_tokens_details":{"cached_tokens":1200,"cache_creation_input_tokens":300}}`), &usage))
	encoded, err := kitutil.Marshal(usage)
	require.NoError(t, err)
	var again Usage
	require.NoError(t, kitutil.Unmarshal(encoded, &again))
	assert.Equal(t, 300, again.PromptTokensDetails.CacheCreationInputTokens)
	assert.Equal(t, 300, again.PromptTokensDetails.CacheCreationTokensTotal())
	assert.Zero(t, again.ClaudeCacheCreation5mTokens)
	assert.Zero(t, again.ClaudeCacheCreation1hTokens)
}

func TestMergeUsageNonZeroCacheCreationInputTokensReplacesWithoutAdding(t *testing.T) {
	t.Parallel()

	first := &Usage{}
	require.NoError(t, kitutil.Unmarshal([]byte(`{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":10,"cache_creation_input_tokens":100}}`), first))
	second := &Usage{}
	require.NoError(t, kitutil.Unmarshal([]byte(`{"prompt_tokens":200,"prompt_tokens_details":{"cached_tokens":20,"cache_creation_input_tokens":300}}`), second))
	merged := MergeUsageNonZero(first, second)
	require.NotNil(t, merged)
	assert.Equal(t, 300, merged.PromptTokensDetails.CacheCreationTokensTotal())
	assert.Equal(t, 20, merged.PromptTokensDetails.CachedTokens)
	assert.Zero(t, merged.ClaudeCacheCreation5mTokens)
	assert.Zero(t, merged.ClaudeCacheCreation1hTokens)

	kept := MergeUsageNonZero(merged, &Usage{})
	assert.Equal(t, 300, kept.PromptTokensDetails.CacheCreationTokensTotal())

	write := &Usage{}
	require.NoError(t, kitutil.Unmarshal([]byte(`{"prompt_tokens_details":{"cache_write_tokens":250}}`), write))
	scalar := &Usage{}
	require.NoError(t, kitutil.Unmarshal([]byte(`{"prompt_tokens_details":{"cache_creation_input_tokens":100}}`), scalar))
	both := MergeUsageNonZero(write, scalar)
	assert.Equal(t, 250, both.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, 250, both.PromptTokensDetails.CacheCreationTokensTotal())

	responses := &Usage{}
	require.NoError(t, kitutil.Unmarshal([]byte(`{"input_tokens_details":{"cache_creation_input_tokens":300}}`), responses))
	overlaid := MergeUsageNonZero(&Usage{}, responses)
	require.NotNil(t, overlaid.InputTokensDetails)
	assert.Equal(t, 300, overlaid.InputTokensDetails.CacheCreationTokensTotal())
	assert.Zero(t, overlaid.PromptTokensDetails.CacheCreationTokensTotal())
}

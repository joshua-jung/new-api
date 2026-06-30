package gemini

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertResponsesToGeminiDeepResearch(t *testing.T) {
	stream := true
	req := dto.OpenAIResponsesRequest{
		Model:  geminiDeepResearchModel,
		Input:  []byte(`"Research Google TPUs"`),
		Stream: &stream,
		ExtraBody: []byte(`{
			"google": {
				"agent_config": {
					"type": "deep-research",
					"thinking_summaries": "auto",
					"collaborative_planning": false
				},
				"tools": [{"type": "google_search"}],
				"previous_interaction_id": "int_prev"
			}
		}`),
	}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: geminiDeepResearchModel}}

	got, err := convertResponsesToGeminiDeepResearch(req, info)
	require.NoError(t, err)

	converted, ok := got.(geminiDeepResearchRequest)
	require.True(t, ok)
	assert.Equal(t, "Research Google TPUs", converted.Input)
	assert.Equal(t, geminiDeepResearchModel, converted.Agent)
	assert.True(t, converted.Background)
	assert.True(t, converted.Stream)
	assert.Equal(t, "deep-research", converted.AgentConfig["type"])
	assert.Equal(t, "auto", converted.AgentConfig["thinking_summaries"])
	assert.Equal(t, "int_prev", converted.PreviousInteractionID)

	tools, ok := converted.Tools.([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "google_search", tool["type"])
}

func TestConvertResponsesToGeminiDeepResearchRequiresStream(t *testing.T) {
	req := dto.OpenAIResponsesRequest{
		Model: geminiDeepResearchModel,
		Input: []byte(`"Research Google TPUs"`),
	}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: geminiDeepResearchModel}}

	_, err := convertResponsesToGeminiDeepResearch(req, info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream=true")
}

func TestGeminiDeepResearchUsageToDTO(t *testing.T) {
	usage := geminiDeepResearchUsageToDTO(&geminiDeepResearchUsage{
		TotalOutputTokens:  300,
		TotalThoughtTokens: 40,
		TotalCachedTokens:  50,
		TotalToolUseTokens: 60,
		TotalTokens:        1000,
	})

	assert.Equal(t, 700, usage.PromptTokens)
	assert.Equal(t, 300, usage.CompletionTokens)
	assert.Equal(t, 1000, usage.TotalTokens)
	assert.Equal(t, 700, usage.InputTokens)
	assert.Equal(t, 300, usage.OutputTokens)
	assert.Equal(t, 50, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 40, usage.CompletionTokenDetails.ReasoningTokens)
}

func TestResponsesInputToGeminiDeepResearchInputArray(t *testing.T) {
	req := dto.OpenAIResponsesRequest{
		Input: []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"Analyze cloud GPUs"}]}]`),
	}

	input, err := responsesInputToGeminiDeepResearchInput(req)
	require.NoError(t, err)

	data, err := common.Marshal(input)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"content":[{"text":"Analyze cloud GPUs","type":"input_text"}],"role":"user","type":"message"}]`, string(data))
}

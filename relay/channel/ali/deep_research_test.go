package ali

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertResponsesToQwenDeepResearchStringInput(t *testing.T) {
	stream := true
	req := dto.OpenAIResponsesRequest{
		Model:  qwenDeepResearchModel,
		Input:  []byte(`"研究一下人工智能在教育中的应用"`),
		Stream: &stream,
		ExtraBody: []byte(`{
			"dashscope": {
				"output_format": "model_summary_report"
			}
		}`),
	}

	got, err := convertResponsesToQwenDeepResearch(req)
	require.NoError(t, err)

	converted, ok := got.(qwenDeepResearchRequest)
	require.True(t, ok)
	assert.Equal(t, qwenDeepResearchModel, converted.Model)
	require.Len(t, converted.Input.Messages, 1)
	assert.Equal(t, "user", converted.Input.Messages[0].Role)
	assert.Equal(t, "研究一下人工智能在教育中的应用", converted.Input.Messages[0].Content)
	require.NotNil(t, converted.Parameters)
	assert.Equal(t, "model_summary_report", converted.Parameters.OutputFormat)
}

func TestConvertResponsesToQwenDeepResearchConversationInput(t *testing.T) {
	stream := true
	req := dto.OpenAIResponsesRequest{
		Model: qwenDeepResearchModel,
		Input: []byte(`[
			{"role":"user","content":"研究一下人工智能在教育中的应用"},
			{"role":"assistant","content":"请告诉我您希望重点研究哪些具体应用场景？"},
			{"role":"user","content":[{"type":"input_text","text":"我主要关注个性化学习和智能评估"}]}
		]`),
		Stream: &stream,
	}

	got, err := convertResponsesToQwenDeepResearch(req)
	require.NoError(t, err)

	converted, ok := got.(qwenDeepResearchRequest)
	require.True(t, ok)
	require.Len(t, converted.Input.Messages, 3)
	assert.Equal(t, "user", converted.Input.Messages[0].Role)
	assert.Equal(t, "assistant", converted.Input.Messages[1].Role)
	assert.Equal(t, "user", converted.Input.Messages[2].Role)
	assert.Equal(t, "我主要关注个性化学习和智能评估", converted.Input.Messages[2].Content)
}

func TestConvertResponsesToQwenDeepResearchRequiresStream(t *testing.T) {
	req := dto.OpenAIResponsesRequest{
		Model: qwenDeepResearchModel,
		Input: []byte(`"研究一下人工智能在教育中的应用"`),
	}

	_, err := convertResponsesToQwenDeepResearch(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream=true")
}

func TestQwenDeepResearchUsageToDTO(t *testing.T) {
	usage := qwenDeepResearchUsageToDTO(AliUsage{
		InputTokens:  694,
		OutputTokens: 1200,
		TotalTokens:  1894,
	})

	assert.Equal(t, 694, usage.PromptTokens)
	assert.Equal(t, 1200, usage.CompletionTokens)
	assert.Equal(t, 1894, usage.TotalTokens)
	assert.Equal(t, 694, usage.InputTokens)
	assert.Equal(t, 1200, usage.OutputTokens)
}

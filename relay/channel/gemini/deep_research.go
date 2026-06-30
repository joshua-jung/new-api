package gemini

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	geminiDeepResearchModel    = "deep-research-preview-04-2026"
	geminiDeepResearchMaxModel = "deep-research-max-preview-04-2026"
)

type geminiDeepResearchRequest struct {
	Input                 any            `json:"input"`
	Agent                 string         `json:"agent"`
	Background            bool           `json:"background"`
	Stream                bool           `json:"stream,omitempty"`
	AgentConfig           map[string]any `json:"agent_config,omitempty"`
	Tools                 any            `json:"tools,omitempty"`
	PreviousInteractionID string         `json:"previous_interaction_id,omitempty"`
}

type geminiDeepResearchExtraBody struct {
	Google struct {
		AgentConfig           map[string]any `json:"agent_config,omitempty"`
		Tools                 any            `json:"tools,omitempty"`
		PreviousInteractionID string         `json:"previous_interaction_id,omitempty"`
	} `json:"google,omitempty"`
}

type geminiDeepResearchStreamEvent struct {
	Type        string                         `json:"type,omitempty"`
	EventType   string                         `json:"event_type,omitempty"`
	EventID     string                         `json:"event_id,omitempty"`
	Interaction *geminiDeepResearchInteraction `json:"interaction,omitempty"`
	Delta       *geminiDeepResearchDelta       `json:"delta,omitempty"`
	Error       any                            `json:"error,omitempty"`
}

type geminiDeepResearchInteraction struct {
	ID     string                   `json:"id,omitempty"`
	Status string                   `json:"status,omitempty"`
	Usage  *geminiDeepResearchUsage `json:"usage,omitempty"`
	Error  any                      `json:"error,omitempty"`
}

type geminiDeepResearchDelta struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type geminiDeepResearchUsage struct {
	TotalInputTokens   int `json:"total_input_tokens,omitempty"`
	TotalOutputTokens  int `json:"total_output_tokens,omitempty"`
	TotalThoughtTokens int `json:"total_thought_tokens,omitempty"`
	TotalCachedTokens  int `json:"total_cached_tokens,omitempty"`
	TotalToolUseTokens int `json:"total_tool_use_tokens,omitempty"`
	TotalTokens        int `json:"total_tokens,omitempty"`
}

func isGeminiDeepResearchModel(modelName string) bool {
	switch strings.TrimSpace(modelName) {
	case geminiDeepResearchModel, geminiDeepResearchMaxModel:
		return true
	default:
		return false
	}
}

func convertResponsesToGeminiDeepResearch(request dto.OpenAIResponsesRequest, info *relaycommon.RelayInfo) (any, error) {
	if !requestStreamEnabled(request.Stream) {
		return nil, errors.New("Gemini Deep Research requires stream=true on /v1/responses")
	}

	input, err := responsesInputToGeminiDeepResearchInput(request)
	if err != nil {
		return nil, err
	}

	out := geminiDeepResearchRequest{
		Input:      input,
		Agent:      info.UpstreamModelName,
		Background: true,
		Stream:     true,
	}

	if len(request.ExtraBody) > 0 && common.GetJsonType(request.ExtraBody) != "null" {
		extra := geminiDeepResearchExtraBody{}
		if err := common.Unmarshal(request.ExtraBody, &extra); err != nil {
			return nil, fmt.Errorf("invalid extra_body: %w", err)
		}
		out.AgentConfig = extra.Google.AgentConfig
		out.Tools = extra.Google.Tools
		out.PreviousInteractionID = extra.Google.PreviousInteractionID
	}

	if out.PreviousInteractionID == "" {
		out.PreviousInteractionID = strings.TrimSpace(request.PreviousResponseID)
	}

	return out, nil
}

func requestStreamEnabled(stream *bool) bool {
	return stream != nil && *stream
}

func responsesInputToGeminiDeepResearchInput(request dto.OpenAIResponsesRequest) (any, error) {
	if len(request.Input) == 0 || common.GetJsonType(request.Input) == "null" {
		if len(request.Instructions) == 0 {
			return nil, errors.New("input is required")
		}
		var instructions string
		if err := common.Unmarshal(request.Instructions, &instructions); err == nil && strings.TrimSpace(instructions) != "" {
			return instructions, nil
		}
		return string(request.Instructions), nil
	}

	if common.GetJsonType(request.Input) == "string" {
		var input string
		if err := common.Unmarshal(request.Input, &input); err != nil {
			return nil, err
		}
		return input, nil
	}

	var input any
	if err := common.Unmarshal(request.Input, &input); err != nil {
		return nil, err
	}
	return input, nil
}

func GeminiDeepResearchResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(errors.New("invalid Gemini Deep Research response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	usage := &dto.Usage{}
	responseID := helper.GetResponseID(c)
	created := common.GetTimestamp()
	model := info.OriginModelName
	outputID := responseID + "_msg_0"
	textStarted := false
	var textBuilder strings.Builder
	var streamErr *types.NewAPIError

	send := func(eventType string, payload dto.ResponsesStreamResponse) {
		if streamErr != nil {
			return
		}
		data, err := common.Marshal(payload)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
			return
		}
		helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: eventType}, string(data))
	}

	helper.SetEventStreamHeaders(c)
	send("response.created", dto.ResponsesStreamResponse{
		Type: "response.created",
		Response: &dto.OpenAIResponsesResponse{
			ID:        responseID,
			Object:    "response",
			CreatedAt: int(created),
			Status:    []byte(`"in_progress"`),
			Model:     model,
			Output:    []dto.ResponsesOutput{},
			Usage:     usage,
		},
	})

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			sr.Error(streamErr)
			sr.Stop(streamErr)
			return
		}

		event := geminiDeepResearchStreamEvent{}
		if err := common.UnmarshalJsonStr(data, &event); err != nil {
			logger.LogError(c, "Gemini Deep Research stream parse failed: "+err.Error())
			return
		}

		eventType := event.Type
		if eventType == "" {
			eventType = event.EventType
		}

		if event.Interaction != nil && event.Interaction.ID != "" {
			c.Header("X-New-Api-Interaction-Id", event.Interaction.ID)
		}

		switch eventType {
		case "step.delta":
			if event.Delta == nil || event.Delta.Text == "" {
				return
			}
			if event.Delta.Type == "thought" {
				sendReasoningDelta(send, outputID, event.Delta.Text, &textStarted)
				return
			}
			textBuilder.WriteString(event.Delta.Text)
			sendTextDelta(send, outputID, event.Delta.Text, &textStarted)
		case "interaction.completed":
			if event.Interaction != nil && event.Interaction.Usage != nil {
				usage = geminiDeepResearchUsageToDTO(event.Interaction.Usage)
			}
		case "interaction.error":
			msg := fmt.Sprintf("%v", event.Error)
			if event.Interaction != nil && event.Interaction.Error != nil {
				msg = fmt.Sprintf("%v", event.Interaction.Error)
			}
			streamErr = types.NewOpenAIError(errors.New(msg), types.ErrorCodeBadResponse, http.StatusBadGateway)
			sr.Error(streamErr)
			sr.Stop(streamErr)
		}
	})

	if streamErr != nil {
		return usage, streamErr
	}
	if usage.TotalTokens == 0 {
		return usage, types.NewOpenAIError(errors.New("Gemini Deep Research response did not include usage.total_tokens"), types.ErrorCodeBadResponse, http.StatusBadGateway)
	}

	if textStarted {
		send("response.output_item.done", dto.ResponsesStreamResponse{
			Type:        "response.output_item.done",
			OutputIndex: common.GetPointer(0),
			Item: &dto.ResponsesOutput{
				Type:   "message",
				ID:     outputID,
				Status: "completed",
				Role:   "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: textBuilder.String(), Annotations: []interface{}{}},
				},
			},
		})
	}

	send("response.completed", dto.ResponsesStreamResponse{
		Type: "response.completed",
		Response: &dto.OpenAIResponsesResponse{
			ID:        responseID,
			Object:    "response",
			CreatedAt: int(created),
			Status:    []byte(`"completed"`),
			Model:     model,
			Output: []dto.ResponsesOutput{
				{
					Type:   "message",
					ID:     outputID,
					Status: "completed",
					Role:   "assistant",
					Content: []dto.ResponsesOutputContent{
						{Type: "output_text", Text: textBuilder.String(), Annotations: []interface{}{}},
					},
				},
			},
			Usage: usage,
		},
	})

	return usage, nil
}

func sendTextDelta(send func(string, dto.ResponsesStreamResponse), outputID, text string, textStarted *bool) {
	if !*textStarted {
		*textStarted = true
		send("response.output_item.added", dto.ResponsesStreamResponse{
			Type:        "response.output_item.added",
			OutputIndex: common.GetPointer(0),
			Item: &dto.ResponsesOutput{
				Type:    "message",
				ID:      outputID,
				Status:  "in_progress",
				Role:    "assistant",
				Content: []dto.ResponsesOutputContent{},
			},
		})
	}
	send("response.output_text.delta", dto.ResponsesStreamResponse{
		Type:         "response.output_text.delta",
		OutputIndex:  common.GetPointer(0),
		ContentIndex: common.GetPointer(0),
		Delta:        text,
	})
}

func sendReasoningDelta(send func(string, dto.ResponsesStreamResponse), outputID, text string, textStarted *bool) {
	sendTextDelta(send, outputID, text, textStarted)
}

func geminiDeepResearchUsageToDTO(src *geminiDeepResearchUsage) *dto.Usage {
	usage := &dto.Usage{}
	if src == nil {
		return usage
	}
	outputTokens := src.TotalOutputTokens
	promptTokens := src.TotalTokens - outputTokens
	if promptTokens < 0 {
		promptTokens = src.TotalInputTokens + src.TotalThoughtTokens + src.TotalToolUseTokens + src.TotalCachedTokens
	}
	if src.TotalTokens == 0 {
		usage.TotalTokens = promptTokens + outputTokens
	} else {
		usage.TotalTokens = src.TotalTokens
	}
	usage.PromptTokens = promptTokens
	usage.CompletionTokens = outputTokens
	usage.InputTokens = promptTokens
	usage.OutputTokens = outputTokens
	usage.PromptTokensDetails.CachedTokens = src.TotalCachedTokens
	usage.CompletionTokenDetails.ReasoningTokens = src.TotalThoughtTokens
	return usage
}

func readGeminiDeepResearchBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("empty response body")
	}
	defer service.CloseResponseBodyGracefully(resp)
	return io.ReadAll(resp.Body)
}

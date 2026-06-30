package ali

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const qwenDeepResearchModel = "qwen-deep-research"

type qwenDeepResearchRequest struct {
	Model      string                      `json:"model"`
	Input      qwenDeepResearchInput       `json:"input"`
	Parameters *qwenDeepResearchParameters `json:"parameters,omitempty"`
}

type qwenDeepResearchInput struct {
	Messages []AliMessage `json:"messages"`
}

type qwenDeepResearchParameters struct {
	OutputFormat string `json:"output_format,omitempty"`
}

type qwenDeepResearchExtraBody struct {
	DashScope struct {
		OutputFormat string `json:"output_format,omitempty"`
	} `json:"dashscope,omitempty"`
}

type qwenDeepResearchStreamResponse struct {
	StatusCode int    `json:"status_code,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
	Output     struct {
		Message *struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
			Phase   string `json:"phase,omitempty"`
			Status  string `json:"status,omitempty"`
			Extra   any    `json:"extra,omitempty"`
		} `json:"message,omitempty"`
		FinishReason string `json:"finish_reason,omitempty"`
		Finished     bool   `json:"finished,omitempty"`
		Fininshed    bool   `json:"fininshed,omitempty"`
	} `json:"output,omitempty"`
	Usage AliUsage `json:"usage,omitempty"`
}

func isQwenDeepResearchModel(modelName string) bool {
	return strings.TrimSpace(modelName) == qwenDeepResearchModel
}

func convertResponsesToQwenDeepResearch(request dto.OpenAIResponsesRequest) (any, error) {
	if request.Stream == nil || !*request.Stream {
		return nil, errors.New("Qwen Deep Research requires stream=true on /v1/responses")
	}

	messages, err := responsesInputToQwenMessages(request)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, errors.New("input is required")
	}

	out := qwenDeepResearchRequest{
		Model: qwenDeepResearchModel,
		Input: qwenDeepResearchInput{Messages: messages},
	}

	if len(request.ExtraBody) > 0 && common.GetJsonType(request.ExtraBody) != "null" {
		extra := qwenDeepResearchExtraBody{}
		if err := common.Unmarshal(request.ExtraBody, &extra); err != nil {
			return nil, fmt.Errorf("invalid extra_body: %w", err)
		}
		if strings.TrimSpace(extra.DashScope.OutputFormat) != "" {
			out.Parameters = &qwenDeepResearchParameters{OutputFormat: extra.DashScope.OutputFormat}
		}
	}

	return out, nil
}

func responsesInputToQwenMessages(request dto.OpenAIResponsesRequest) ([]AliMessage, error) {
	if len(request.Input) == 0 || common.GetJsonType(request.Input) == "null" {
		return nil, nil
	}

	if common.GetJsonType(request.Input) == "string" {
		var input string
		if err := common.Unmarshal(request.Input, &input); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input) == "" {
			return nil, nil
		}
		return []AliMessage{{Role: "user", Content: input}}, nil
	}

	if common.GetJsonType(request.Input) != "array" {
		return nil, fmt.Errorf("unsupported input type %s", common.GetJsonType(request.Input))
	}

	var items []struct {
		Role    string          `json:"role,omitempty"`
		Type    string          `json:"type,omitempty"`
		Content json.RawMessage `json:"content,omitempty"`
	}
	if err := common.Unmarshal(request.Input, &items); err != nil {
		return nil, err
	}

	messages := make([]AliMessage, 0, len(items))
	for _, item := range items {
		role := strings.TrimSpace(item.Role)
		if role == "" {
			role = "user"
		}
		if role != "user" && role != "assistant" {
			continue
		}

		content := qwenContentToString(item.Content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		messages = append(messages, AliMessage{Role: role, Content: content})
	}
	return messages, nil
}

func qwenContentToString(raw []byte) string {
	if len(raw) == 0 || common.GetJsonType(raw) == "null" {
		return ""
	}
	if common.GetJsonType(raw) == "string" {
		var content string
		_ = common.Unmarshal(raw, &content)
		return content
	}
	if common.GetJsonType(raw) != "array" {
		return common.JsonRawMessageToString(raw)
	}

	var parts []map[string]any
	if err := common.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		partType := strings.TrimSpace(common.Interface2String(part["type"]))
		switch partType {
		case "input_text", "output_text", "text":
			if text := strings.TrimSpace(common.Interface2String(part["text"])); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func QwenDeepResearchResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(errors.New("invalid Qwen Deep Research response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	usage := &dto.Usage{}
	responseID := helper.GetResponseID(c)
	created := common.GetTimestamp()
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
			Model:     info.OriginModelName,
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

		event := qwenDeepResearchStreamResponse{}
		if err := common.UnmarshalJsonStr(data, &event); err != nil {
			return
		}
		if event.Code != "" {
			streamErr = types.NewOpenAIError(errors.New(event.Message), types.ErrorCodeBadResponse, http.StatusBadGateway)
			sr.Error(streamErr)
			sr.Stop(streamErr)
			return
		}

		if event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0 || event.Usage.TotalTokens > 0 {
			usage = qwenDeepResearchUsageToDTO(event.Usage)
		}

		if event.Output.Message != nil && event.Output.Message.Content != "" {
			textBuilder.WriteString(event.Output.Message.Content)
			qwenSendTextDelta(send, outputID, event.Output.Message.Content, &textStarted)
		}
	})

	if streamErr != nil {
		return usage, streamErr
	}
	if usage.TotalTokens == 0 {
		return usage, types.NewOpenAIError(errors.New("Qwen Deep Research response did not include usage tokens"), types.ErrorCodeBadResponse, http.StatusBadGateway)
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
			Model:     info.OriginModelName,
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

func qwenSendTextDelta(send func(string, dto.ResponsesStreamResponse), outputID, text string, textStarted *bool) {
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

func qwenDeepResearchUsageToDTO(src AliUsage) *dto.Usage {
	usage := &dto.Usage{
		PromptTokens:     src.InputTokens,
		CompletionTokens: src.OutputTokens,
		InputTokens:      src.InputTokens,
		OutputTokens:     src.OutputTokens,
	}
	if src.TotalTokens > 0 {
		usage.TotalTokens = src.TotalTokens
	} else {
		usage.TotalTokens = src.InputTokens + src.OutputTokens
	}
	return usage
}

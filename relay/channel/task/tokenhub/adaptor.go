package tokenhub

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type seedanceRequest struct {
	Model         string                        `json:"model"`
	Content       []relaycommon.TaskContentItem `json:"content"`
	GenerateAudio *bool                         `json:"generate_audio,omitempty"`
	Ratio         string                        `json:"ratio,omitempty"`
	Duration      *int                          `json:"duration,omitempty"`
	Watermark     *bool                         `json:"watermark,omitempty"`
}

type seedanceSubmitResponse struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
	} `json:"output"`
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
}

type seedanceTaskResponse struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Ratio                 string `json:"ratio"`
	Resolution            string `json:"resolution"`
	Duration              int    `json:"duration"`
	Seed                  int    `json:"seed"`
	FramesPerSecond       int    `json:"framespersecond"`
	ExecutionExpiresAfter int    `json:"execution_expires_after"`
	CreatedAt             int64  `json:"created_at"`
	UpdatedAt             int64  `json:"updated_at"`
	Usage                 struct {
		CompletionTokens int    `json:"completion_tokens"`
		TotalTokens      int    `json:"total_tokens"`
		SR               any    `json:"SR"`
		Duration         int    `json:"duration"`
		Ratio            string `json:"ratio"`
	} `json:"usage"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = info.ApiKey
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("TokenHub video requests must use application/json"),
			"invalid_content_type",
			http.StatusBadRequest,
		)
	}

	var req relaycommon.TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if strings.TrimSpace(req.Model) == "" {
		return service.TaskErrorWrapperLocal(fmt.Errorf("model field is required"), "missing_model", http.StatusBadRequest)
	}

	if len(req.Images) == 0 && strings.TrimSpace(req.Image) != "" {
		req.Images = []string{strings.TrimSpace(req.Image)}
	}
	if len(req.Images) == 0 && strings.TrimSpace(req.InputReference) != "" {
		req.Images = []string{strings.TrimSpace(req.InputReference)}
	}
	hasLegacyInput := strings.TrimSpace(req.Prompt) != "" || len(req.Images) > 0 || strings.TrimSpace(req.InputReference) != ""
	if len(req.Content) > 0 && hasLegacyInput {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("content cannot be combined with prompt, image, images, or input_reference"),
			"conflicting_video_input",
			http.StatusBadRequest,
		)
	}

	if len(req.Content) == 0 {
		if strings.TrimSpace(req.Prompt) == "" {
			return service.TaskErrorWrapperLocal(fmt.Errorf("prompt is required"), "invalid_request", http.StatusBadRequest)
		}
		if len(req.Images) > 9 {
			return service.TaskErrorWrapperLocal(fmt.Errorf("images supports at most 9 items"), "invalid_content", http.StatusBadRequest)
		}
		for _, imageURL := range req.Images {
			if strings.TrimSpace(imageURL) == "" {
				return service.TaskErrorWrapperLocal(fmt.Errorf("image URL cannot be empty"), "invalid_content", http.StatusBadRequest)
			}
		}
	} else if taskErr := validateSeedanceContent(req.Content); taskErr != nil {
		return taskErr
	}

	duration := req.Duration
	secondsSet := req.Seconds != ""
	if req.Seconds != "" {
		parsed, err := strconv.Atoi(req.Seconds)
		if err != nil {
			return service.TaskErrorWrapperLocal(fmt.Errorf("seconds must be an integer"), "invalid_seconds", http.StatusBadRequest)
		}
		if duration == 0 {
			duration = parsed
			req.Duration = parsed
		}
	}
	if ((req.DurationSet || secondsSet) && duration <= 0) || duration < 0 || duration > relaycommon.MaxTaskDurationSeconds {
		return service.TaskErrorWrapperLocal(
			fmt.Errorf("duration must be between 1 and %d", relaycommon.MaxTaskDurationSeconds),
			"invalid_duration",
			http.StatusBadRequest,
		)
	}
	if req.Ratio != "" && req.Ratio != "9:16" && req.Ratio != "16:9" && req.Ratio != "1:1" {
		return service.TaskErrorWrapperLocal(fmt.Errorf("ratio must be one of 9:16, 16:9, or 1:1"), "invalid_ratio", http.StatusBadRequest)
	}

	relaycommon.StoreTaskRequest(c, info, constant.TaskActionGenerate, req)
	return nil
}

func validateSeedanceContent(content []relaycommon.TaskContentItem) *dto.TaskError {
	textCount := 0
	imageCount := 0
	videoCount := 0
	audioCount := 0

	for index, item := range content {
		switch item.Type {
		case "text":
			if strings.TrimSpace(item.Text) == "" {
				return invalidContentError(index, "text is required")
			}
			textCount++
		case "image_url":
			if item.ImageURL == nil || strings.TrimSpace(item.ImageURL.URL) == "" {
				return invalidContentError(index, "image_url.url is required")
			}
			if item.Role != "reference_image" && item.Role != "first_frame" && item.Role != "last_frame" {
				return invalidContentError(index, "invalid image role")
			}
			imageCount++
		case "video_url":
			if item.VideoURL == nil || strings.TrimSpace(item.VideoURL.URL) == "" {
				return invalidContentError(index, "video_url.url is required")
			}
			if item.Role != "reference_video" {
				return invalidContentError(index, "role must be reference_video")
			}
			videoCount++
		case "audio_url":
			if item.AudioURL == nil || strings.TrimSpace(item.AudioURL.URL) == "" {
				return invalidContentError(index, "audio_url.url is required")
			}
			if item.Role != "reference_audio" {
				return invalidContentError(index, "role must be reference_audio")
			}
			audioCount++
		default:
			return invalidContentError(index, "unsupported content type")
		}
	}

	if textCount == 0 {
		return service.TaskErrorWrapperLocal(fmt.Errorf("content must include a non-empty text item"), "invalid_content", http.StatusBadRequest)
	}
	if imageCount > 9 {
		return service.TaskErrorWrapperLocal(fmt.Errorf("content supports at most 9 images"), "invalid_content", http.StatusBadRequest)
	}
	if videoCount > 1 {
		return service.TaskErrorWrapperLocal(fmt.Errorf("content supports at most 1 video"), "invalid_content", http.StatusBadRequest)
	}
	if audioCount > 1 {
		return service.TaskErrorWrapperLocal(fmt.Errorf("content supports at most 1 audio"), "invalid_content", http.StatusBadRequest)
	}
	return nil
}

func invalidContentError(index int, message string) *dto.TaskError {
	return service.TaskErrorWrapperLocal(
		fmt.Errorf("content[%d]: %s", index, message),
		"invalid_content",
		http.StatusBadRequest,
	)
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	protocol, ok := modelVideoProtocols[info.UpstreamModelName]
	if !ok {
		return "", fmt.Errorf("TokenHub video model %q has no registered protocol", info.UpstreamModelName)
	}
	switch protocol {
	case seedanceGenerationsV1:
		return a.baseURL + "/v1/videos/generations", nil
	default:
		return "", fmt.Errorf("unsupported TokenHub video protocol %q", protocol)
	}
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}
	if _, ok := modelVideoProtocols[info.UpstreamModelName]; !ok {
		return nil, fmt.Errorf("TokenHub video model %q has no registered protocol", info.UpstreamModelName)
	}

	content := req.Content
	if len(content) == 0 {
		content = make([]relaycommon.TaskContentItem, 0, len(req.Images)+1)
		for _, imageURL := range req.Images {
			content = append(content, relaycommon.TaskContentItem{
				Type:     "image_url",
				ImageURL: &relaycommon.TaskMediaURL{URL: imageURL},
				Role:     "reference_image",
			})
		}
		content = append(content, relaycommon.TaskContentItem{
			Type: "text",
			Text: req.Prompt,
		})
	}

	body := seedanceRequest{
		Model:         info.UpstreamModelName,
		Content:       content,
		GenerateAudio: req.GenerateAudio,
		Ratio:         req.Ratio,
		Watermark:     req.Watermark,
	}
	if req.Duration > 0 {
		body.Duration = &req.Duration
	}

	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	requestURL, _ := a.BuildRequestURL(info)
	startedAt := time.Now()
	logger.LogInfo(c, fmt.Sprintf(
		"TokenHub Seedance submit started: channel_id=%d model=%s upstream=%s relay_timeout_seconds=%d",
		info.ChannelId,
		info.UpstreamModelName,
		relaycommon.SanitizeURLForLog(requestURL),
		common.RelayTimeout,
	))

	resp, err := channel.DoTaskApiRequest(a, c, info, requestBody)
	elapsed := time.Since(startedAt)
	if err != nil {
		logger.LogError(c, fmt.Sprintf(
			"TokenHub Seedance submit failed: channel_id=%d model=%s elapsed_ms=%d error=%v",
			info.ChannelId,
			info.UpstreamModelName,
			elapsed.Milliseconds(),
			err,
		))
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		logger.LogError(c, fmt.Sprintf(
			"TokenHub Seedance upstream returned error: channel_id=%d model=%s status=%d elapsed_ms=%d request_id=%s content_type=%s response=%s",
			info.ChannelId,
			info.UpstreamModelName,
			resp.StatusCode,
			elapsed.Milliseconds(),
			upstreamRequestID(resp),
			resp.Header.Get("Content-Type"),
			summarizeErrorResponse(resp),
		))
		return resp, nil
	}

	logger.LogInfo(c, fmt.Sprintf(
		"TokenHub Seedance submit completed: channel_id=%d model=%s status=%d elapsed_ms=%d request_id=%s",
		info.ChannelId,
		info.UpstreamModelName,
		resp.StatusCode,
		elapsed.Milliseconds(),
		upstreamRequestID(resp),
	))
	return resp, nil
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var upstream seedanceSubmitResponse
	if err := common.Unmarshal(responseBody, &upstream); err != nil {
		return "", responseBody, service.TaskErrorWrapper(
			errors.Wrapf(err, "body: %s", responseBody),
			"unmarshal_response_body_failed",
			http.StatusInternalServerError,
		)
	}
	if upstream.Output.TaskID == "" {
		return "", responseBody, service.TaskErrorWrapper(
			fmt.Errorf("task_id is empty"),
			"invalid_response",
			http.StatusInternalServerError,
		)
	}
	logger.LogInfo(c, fmt.Sprintf(
		"TokenHub Seedance task accepted: channel_id=%d model=%s upstream_task_id=%s upstream_status=%s request_id=%s",
		info.ChannelId,
		info.UpstreamModelName,
		upstream.Output.TaskID,
		upstream.Output.TaskStatus,
		upstream.RequestID,
	))

	video := dto.NewOpenAIVideo()
	video.ID = info.PublicTaskID
	video.TaskID = info.PublicTaskID
	video.CreatedAt = time.Now().Unix()
	video.Model = info.OriginModelName
	c.JSON(http.StatusOK, video)
	return upstream.Output.TaskID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || taskID == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	modelName, ok := body["model"].(string)
	if !ok || modelName == "" {
		return nil, fmt.Errorf("invalid model")
	}
	protocol, ok := modelVideoProtocols[modelName]
	if !ok {
		return nil, fmt.Errorf("TokenHub video model %q has no registered protocol", modelName)
	}

	var uri string
	switch protocol {
	case seedanceGenerationsV1:
		uri = strings.TrimRight(baseURL, "/") + "/v1/videos/generations/task/" + taskID
	default:
		return nil, fmt.Errorf("unsupported TokenHub video protocol %q", protocol)
	}

	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	startedAt := time.Now()
	logger.LogInfo(context.Background(), fmt.Sprintf(
		"TokenHub Seedance polling started: model=%s upstream_task_id=%s upstream=%s relay_timeout_seconds=%d",
		modelName,
		taskID,
		relaycommon.SanitizeURLForLog(uri),
		common.RelayTimeout,
	))
	resp, err := client.Do(req)
	elapsed := time.Since(startedAt)
	if err != nil {
		logger.LogError(context.Background(), fmt.Sprintf(
			"TokenHub Seedance polling failed: model=%s upstream_task_id=%s elapsed_ms=%d error=%v",
			modelName,
			taskID,
			elapsed.Milliseconds(),
			err,
		))
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		logger.LogError(context.Background(), fmt.Sprintf(
			"TokenHub Seedance polling upstream returned error: model=%s upstream_task_id=%s status=%d elapsed_ms=%d request_id=%s content_type=%s response=%s",
			modelName,
			taskID,
			resp.StatusCode,
			elapsed.Milliseconds(),
			upstreamRequestID(resp),
			resp.Header.Get("Content-Type"),
			summarizeErrorResponse(resp),
		))
		return resp, nil
	}
	logger.LogInfo(context.Background(), fmt.Sprintf(
		"TokenHub Seedance polling completed: model=%s upstream_task_id=%s status=%d elapsed_ms=%d request_id=%s",
		modelName,
		taskID,
		resp.StatusCode,
		elapsed.Milliseconds(),
		upstreamRequestID(resp),
	))
	return resp, nil
}

func upstreamRequestID(resp *http.Response) string {
	for _, header := range []string{
		"X-Oneapi-Request-Id",
		"X-Request-Id",
		"X-Dashscope-Request-Id",
		"X-Volc-Request-Id",
	} {
		if requestID := resp.Header.Get(header); requestID != "" {
			return requestID
		}
	}
	return ""
}

func summarizeErrorResponse(resp *http.Response) string {
	body, err := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return fmt.Sprintf("read_error=%q", err.Error())
	}

	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return fmt.Sprintf("unparseable_body_bytes=%d", len(body))
	}

	summary := make(map[string]any)
	for _, key := range []string{"request_id", "code", "message"} {
		if value, ok := payload[key]; ok {
			summary[key] = value
		}
	}
	if upstreamError, ok := payload["error"].(map[string]any); ok {
		errorSummary := make(map[string]any)
		for _, key := range []string{"code", "type", "message"} {
			if value, ok := upstreamError[key]; ok {
				errorSummary[key] = value
			}
		}
		if len(errorSummary) > 0 {
			summary["error"] = errorSummary
		}
	} else if upstreamError, ok := payload["error"].(string); ok {
		summary["error"] = upstreamError
	}
	if len(summary) == 0 {
		return fmt.Sprintf("json_body_bytes=%d", len(body))
	}
	data, err := common.Marshal(summary)
	if err != nil {
		return fmt.Sprintf("json_body_bytes=%d", len(body))
	}
	return string(data)
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var upstream seedanceTaskResponse
	if err := common.Unmarshal(respBody, &upstream); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}
	if upstream.Model != "" {
		if _, ok := modelVideoProtocols[upstream.Model]; !ok {
			return nil, fmt.Errorf("TokenHub video model %q has no registered protocol", upstream.Model)
		}
	}

	result := &relaycommon.TaskInfo{Code: 0, TaskID: upstream.TaskID}
	switch upstream.Status {
	case "pending", "queued":
		result.Status = string(model.TaskStatusQueued)
		result.Progress = taskcommon.ProgressQueued
	case "processing", "running":
		result.Status = string(model.TaskStatusInProgress)
		result.Progress = taskcommon.ProgressInProgress
	case "succeeded":
		result.Status = string(model.TaskStatusSuccess)
		result.Progress = taskcommon.ProgressComplete
		result.Url = upstream.Content.VideoURL
		if upstream.Usage.CompletionTokens > 0 {
			result.CompletionTokens = upstream.Usage.CompletionTokens
		}
		if upstream.Usage.TotalTokens > 0 {
			result.TotalTokens = upstream.Usage.TotalTokens
		} else {
			result.TotalTokens = result.CompletionTokens
		}
	case "failed":
		result.Status = string(model.TaskStatusFailure)
		result.Progress = taskcommon.ProgressComplete
		result.Reason = upstream.Error.Message
		if result.Reason == "" {
			result.Reason = upstream.Message
		}
		if result.Reason == "" {
			result.Reason = "upstream video generation failed"
		}
	default:
		return nil, fmt.Errorf("unknown TokenHub task status %q", upstream.Status)
	}
	return result, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	var upstream seedanceTaskResponse
	if err := common.Unmarshal(task.Data, &upstream); err != nil {
		return nil, errors.Wrap(err, "unmarshal TokenHub task data failed")
	}

	video := dto.NewOpenAIVideo()
	video.ID = task.TaskID
	video.TaskID = task.TaskID
	video.Status = task.Status.ToVideoStatus()
	video.SetProgressStr(task.Progress)
	video.CreatedAt = task.CreatedAt
	video.CompletedAt = task.UpdatedAt
	video.Model = task.Properties.OriginModelName
	if upstream.Duration > 0 {
		video.Seconds = strconv.Itoa(upstream.Duration)
	}
	video.Size = upstream.Resolution
	if upstream.ExecutionExpiresAfter > 0 && upstream.CreatedAt > 0 {
		video.ExpiresAt = upstream.CreatedAt + int64(upstream.ExecutionExpiresAfter)
	}
	video.SetMetadata("url", upstream.Content.VideoURL)
	video.SetMetadata("ratio", upstream.Ratio)
	video.SetMetadata("resolution", upstream.Resolution)
	video.SetMetadata("duration", upstream.Duration)
	video.SetMetadata("seed", upstream.Seed)
	video.SetMetadata("framespersecond", upstream.FramesPerSecond)
	video.SetMetadata("execution_expires_after", upstream.ExecutionExpiresAfter)
	video.SetMetadata("usage", upstream.Usage)

	if task.Status == model.TaskStatusFailure {
		message := upstream.Error.Message
		if message == "" {
			message = upstream.Message
		}
		if message == "" {
			message = task.FailReason
		}
		video.Error = &dto.OpenAIVideoError{
			Message: message,
			Code:    upstream.Error.Code,
		}
	}
	return common.Marshal(video)
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

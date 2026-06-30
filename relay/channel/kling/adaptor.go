package kling

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// 异步图片任务轮询参数
	imagePollInterval = 2 * time.Second
	imageMaxWait      = 3 * time.Minute
)

// Adaptor 可灵图片生成渠道适配器。可灵图片生成为异步接口（提交任务 -> 轮询查询），
// 此处轮询直至完成，将其包装为同步的 /v1/images/generations 响应。
type Adaptor struct {
	apiKey  string
	baseURL string
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = info.ApiKey
	a.baseURL = info.ChannelBaseUrl
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	// new-api 中转渠道（密钥为 sk- 开头）需要带上 /kling 前缀，与 task/kling 保持一致。
	if isNewAPIRelay(a.apiKey) {
		return fmt.Sprintf("%s/kling/v1/images/generations", a.baseURL), nil
	}
	return fmt.Sprintf("%s/v1/images/generations", a.baseURL), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	return errors.New("not implemented")
}

// ============================
// Request / Response structures
// ============================

type imageRequestPayload struct {
	ModelName      string   `json:"model_name,omitempty"`
	Prompt         string   `json:"prompt"`
	NegativePrompt string   `json:"negative_prompt,omitempty"`
	Image          string   `json:"image,omitempty"`
	ImageReference string   `json:"image_reference,omitempty"`
	ImageFidelity  *float64 `json:"image_fidelity,omitempty"`
	HumanFidelity  *float64 `json:"human_fidelity,omitempty"`
	N              *int     `json:"n,omitempty"`
	AspectRatio    string   `json:"aspect_ratio,omitempty"`
	Resolution     string   `json:"resolution,omitempty"`
	CallbackUrl    string   `json:"callback_url,omitempty"`
}

type imageResponsePayload struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	RequestId string `json:"request_id"`
	Data      struct {
		TaskID        string `json:"task_id"`
		TaskStatus    string `json:"task_status"`
		TaskStatusMsg string `json:"task_status_msg"`
		TaskResult    struct {
			Images []struct {
				Index int    `json:"index"`
				Url   string `json:"url"`
			} `json:"images"`
		} `json:"task_result"`
	} `json:"data"`
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	modelName := request.Model
	info.UpstreamModelName = modelName

	payload := imageRequestPayload{
		ModelName:   modelName,
		Prompt:      request.Prompt,
		AspectRatio: mapSizeToAspectRatio(request.Size),
	}
	if request.N != nil && *request.N > 0 {
		n := int(*request.N)
		payload.N = &n
	}

	// 透传额外参数（negative_prompt、image、image_reference、image_fidelity 等）。
	if len(request.ExtraFields) > 0 {
		if err := common.Unmarshal(request.ExtraFields, &payload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal extra fields: %w", err)
		}
	}

	return payload, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	// 1. 提交任务
	submitURL, _ := a.GetRequestURL(info)
	submitReq, err := http.NewRequest(http.MethodPost, submitURL, requestBody)
	if err != nil {
		return nil, fmt.Errorf("new submit request failed: %w", err)
	}
	if err = a.setAuthHeader(submitReq); err != nil {
		return nil, err
	}
	submitResp, err := channel.DoRequest(c, submitReq, info)
	if err != nil {
		return nil, fmt.Errorf("submit task failed: %w", err)
	}
	submitBody, err := io.ReadAll(submitResp.Body)
	service.CloseResponseBodyGracefully(submitResp)
	if err != nil {
		return nil, fmt.Errorf("read submit response failed: %w", err)
	}
	if submitResp.StatusCode != http.StatusOK {
		return newJSONResponse(submitResp.StatusCode, submitBody), nil
	}
	var submit imageResponsePayload
	if err = common.Unmarshal(submitBody, &submit); err != nil {
		return nil, fmt.Errorf("unmarshal submit response failed: %w, body: %s", err, submitBody)
	}
	if submit.Code != 0 || submit.Data.TaskID == "" {
		// 业务错误：交给 klingImageHandler 统一返回错误信息。
		return newJSONResponse(submitResp.StatusCode, submitBody), nil
	}

	// 2. 轮询查询结果
	taskID := submit.Data.TaskID
	deadline := time.Now().Add(imageMaxWait)
	for {
		select {
		case <-c.Request.Context().Done():
			return nil, c.Request.Context().Err()
		case <-time.After(imagePollInterval):
		}

		statusCode, resultBody, err := a.fetchResult(c, info, taskID)
		if err != nil {
			logger.LogWarn(c, fmt.Sprintf("kling fetch image result failed: %v", err))
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("kling image task polling failed: %w", err)
			}
			continue
		}

		var result imageResponsePayload
		if err = common.Unmarshal(resultBody, &result); err != nil {
			return nil, fmt.Errorf("unmarshal image result failed: %w, body: %s", err, resultBody)
		}

		if result.Code != 0 || isTerminalStatus(result.Data.TaskStatus) {
			return newJSONResponse(statusCode, resultBody), nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("kling image task %s polling timeout after %s (last status: %s)", taskID, imageMaxWait, result.Data.TaskStatus)
		}
	}
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.RelayMode == relayconstant.RelayModeImagesGenerations {
		return klingImageHandler(c, resp, info)
	}
	return nil, types.NewError(errors.New("unsupported relay mode for kling"), types.ErrorCodeInvalidRequest)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

// ============================
// helpers
// ============================

// fetchResult 查询一次图片任务结果。
func (a *Adaptor) fetchResult(c *gin.Context, info *relaycommon.RelayInfo, taskID string) (int, []byte, error) {
	uri := fmt.Sprintf("%s/v1/images/generations/%s", a.baseURL, taskID)
	if isNewAPIRelay(a.apiKey) {
		uri = fmt.Sprintf("%s/kling/v1/images/generations/%s", a.baseURL, taskID)
	}
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("new query request failed: %w", err)
	}
	if err = a.setAuthHeader(req); err != nil {
		return 0, nil, err
	}
	resp, err := channel.DoRequest(c, req, info)
	if err != nil {
		return 0, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	service.CloseResponseBodyGracefully(resp)
	if err != nil {
		return 0, nil, fmt.Errorf("read query response failed: %w", err)
	}
	return resp.StatusCode, body, nil
}

func (a *Adaptor) setAuthHeader(req *http.Request) error {
	token, err := a.createJWTToken()
	if err != nil {
		return fmt.Errorf("failed to create JWT token: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "kling-sdk/1.0")
	return nil
}

// isNewAPIRelay 判断是否为 new-api 中转渠道（密钥为 sk- 开头），与 task/kling 保持一致。
func isNewAPIRelay(apiKey string) bool {
	return strings.HasPrefix(apiKey, "sk-")
}

// createJWTToken 按可灵要求用 accessKey|secretKey 生成 JWT；
// 中转渠道（sk- 开头）直接使用令牌，不再签发 JWT。
func (a *Adaptor) createJWTToken() (string, error) {
	if isNewAPIRelay(a.apiKey) {
		return a.apiKey, nil
	}
	keyParts := strings.Split(a.apiKey, "|")
	if len(keyParts) != 2 {
		return "", errors.New("invalid api_key, required format is accessKey|secretKey")
	}
	accessKey := strings.TrimSpace(keyParts[0])
	secretKey := strings.TrimSpace(keyParts[1])
	now := time.Now().Unix()
	claims := jwt.MapClaims{
		"iss": accessKey,
		"exp": now + 1800, // 30 分钟
		"nbf": now - 5,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["typ"] = "JWT"
	return token.SignedString([]byte(secretKey))
}

// isTerminalStatus 判断可灵任务是否进入终态。
// 任务状态：submitted（已提交）、processing（处理中）、succeed（成功）、failed（失败）
func isTerminalStatus(status string) bool {
	switch status {
	case "submitted", "processing", "":
		return false
	default:
		return true
	}
}

// mapSizeToAspectRatio 将 OpenAI size 映射为可灵 aspect_ratio。
func mapSizeToAspectRatio(size string) string {
	switch size {
	case "":
		return ""
	case "1024x1024", "512x512":
		return "1:1"
	case "1792x1024", "1280x720", "1920x1080":
		return "16:9"
	case "1024x1792", "720x1280", "1080x1920":
		return "9:16"
	default:
		// 允许直接传入 "16:9" 等比例字符串
		if strings.Contains(size, ":") {
			return size
		}
		return "1:1"
	}
}

// newJSONResponse 用给定状态码与 body 构造可供 DoResponse 读取的 http.Response。
func newJSONResponse(statusCode int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    statusCode,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// klingImageHandler 解析可灵图片任务结果并转换为 OpenAI 图片响应。
func klingImageHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)

	var kResp imageResponsePayload
	if err = common.Unmarshal(responseBody, &kResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	if kResp.Code != 0 {
		return nil, types.WithOpenAIError(types.OpenAIError{
			Message: kResp.Message,
			Type:    "kling_error",
			Code:    fmt.Sprintf("%d", kResp.Code),
		}, resp.StatusCode)
	}
	if kResp.Data.TaskStatus != "succeed" {
		msg := kResp.Data.TaskStatusMsg
		if msg == "" {
			msg = fmt.Sprintf("kling image task status: %s", kResp.Data.TaskStatus)
		}
		return nil, types.WithOpenAIError(types.OpenAIError{
			Message: msg,
			Type:    "kling_error",
			Code:    kResp.Data.TaskStatus,
		}, http.StatusInternalServerError)
	}

	imageResponse := dto.ImageResponse{Created: info.StartTime.Unix()}
	for _, img := range kResp.Data.TaskResult.Images {
		imageResponse.Data = append(imageResponse.Data, dto.ImageData{Url: img.Url})
	}

	jsonResponse, err := common.Marshal(imageResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	if _, err = c.Writer.Write(jsonResponse); err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	return &dto.Usage{}, nil
}

// ============================
// 未使用的接口方法（图片渠道仅实现 ConvertImageRequest / DoRequest / DoResponse）
// ============================

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("not implemented")
}

package jimeng

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	// 异步图片任务轮询参数
	asyncImagePollInterval = 2 * time.Second
	asyncImageMaxWait      = 3 * time.Minute
)

// isAsyncImageModel 判断图片模型是否使用异步「提交任务 / 查询结果」接口。
// 2.x 通用模型 (jimeng_high_aes_general_*) 使用同步 CVProcess 接口；
// 3.x/4.x 系列 (jimeng_t2i_v30/v31/v40/v46、jimeng_i2i_v30 等) 使用异步接口。
func isAsyncImageModel(reqKey string) bool {
	if reqKey == "" {
		return false
	}
	return !strings.HasPrefix(reqKey, "jimeng_high_aes")
}

// parseImageSize 解析 "宽x高" 形式的尺寸字符串（如 "2048x2048"）。
func parseImageSize(size string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// isTerminalImageStatus 判断异步任务是否进入终态（不再需要轮询）。
// 即梦任务执行状态 https://www.volcengine.com/docs/85621/1544774
func isTerminalImageStatus(status string) bool {
	switch status {
	case "in_queue", "generating", "":
		// 排队中 / 处理中：继续轮询
		return false
	default:
		// done / not_found / expired 等：终态
		return true
	}
}

// asyncSubmitResponse 提交任务接口（CVSync2AsyncSubmitTask）的响应。
type asyncSubmitResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		TaskID string `json:"task_id"`
	} `json:"data"`
}

// asyncResultResponse 查询结果接口（CVSync2AsyncGetResult）的响应。
type asyncResultResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Status           string   `json:"status"`
		ImageUrls        []string `json:"image_urls"`
		BinaryDataBase64 []string `json:"binary_data_base64"`
	} `json:"data"`
}

// newJSONResponse 用给定的状态码与 body 构造一个可供 DoResponse 读取的 http.Response。
func newJSONResponse(statusCode int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    statusCode,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// doAsyncImageRequest 提交异步图片任务并轮询直至完成，将异步接口包装为同步响应。
func (a *Adaptor) doAsyncImageRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	bodyBytes, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, fmt.Errorf("read request body failed: %w", err)
	}

	// 解析提交体，复用其中的 req_key 与 return_url。
	var submitPayload imageRequestPayload
	_ = common.Unmarshal(bodyBytes, &submitPayload)

	// 1. 提交任务
	submitURL, err := a.GetRequestURL(info)
	if err != nil {
		return nil, fmt.Errorf("get submit url failed: %w", err)
	}
	submitReq, err := http.NewRequest(http.MethodPost, submitURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("new submit request failed: %w", err)
	}
	if err = Sign(c, submitReq, info.ApiKey); err != nil {
		return nil, fmt.Errorf("sign submit request failed: %w", err)
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
	// 提交失败（HTTP 非 200 或上游业务错误）直接交回上层处理。
	if submitResp.StatusCode != http.StatusOK {
		return newJSONResponse(submitResp.StatusCode, submitBody), nil
	}
	var submit asyncSubmitResponse
	if err = common.Unmarshal(submitBody, &submit); err != nil {
		return nil, fmt.Errorf("unmarshal submit response failed: %w, body: %s", err, submitBody)
	}
	if submit.Code != 10000 || submit.Data.TaskID == "" {
		// 业务错误：交给 jimengAsyncImageHandler 统一返回错误信息。
		return newJSONResponse(submitResp.StatusCode, submitBody), nil
	}

	// 2. 轮询查询结果，直至任务进入终态或超时。
	taskID := submit.Data.TaskID
	deadline := time.Now().Add(asyncImageMaxWait)
	for {
		select {
		case <-c.Request.Context().Done():
			return nil, c.Request.Context().Err()
		case <-time.After(asyncImagePollInterval):
		}

		statusCode, resultBody, err := a.fetchAsyncImageResult(c, info, submitPayload.ReqKey, taskID, submitPayload.ReturnURL)
		if err != nil {
			logger.LogWarn(c, fmt.Sprintf("jimeng fetch async image result failed: %v", err))
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("jimeng image task polling failed: %w", err)
			}
			continue
		}

		var result asyncResultResponse
		if err = common.Unmarshal(resultBody, &result); err != nil {
			return nil, fmt.Errorf("unmarshal async result failed: %w, body: %s", err, resultBody)
		}

		// 上游业务错误或任务进入终态，结束轮询并返回最终结果。
		if result.Code != 10000 || isTerminalImageStatus(result.Data.Status) {
			return newJSONResponse(statusCode, resultBody), nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("jimeng image task %s polling timeout after %s (last status: %s)", taskID, asyncImageMaxWait, result.Data.Status)
		}
	}
}

// fetchAsyncImageResult 调用 CVSync2AsyncGetResult 查询一次任务结果。
func (a *Adaptor) fetchAsyncImageResult(c *gin.Context, info *relaycommon.RelayInfo, reqKey, taskID string, returnURL bool) (int, []byte, error) {
	uri := fmt.Sprintf("%s/?Action=CVSync2AsyncGetResult&Version=2022-08-31", info.ChannelBaseUrl)
	// req_json 为 JSON 字符串，控制是否返回图片 URL（valid 24h）还是 base64。
	reqJSON := `{"return_url":false}`
	if returnURL {
		reqJSON = `{"return_url":true}`
	}
	payload := map[string]string{
		"req_key":  reqKey,
		"task_id":  taskID,
		"req_json": reqJSON,
	}
	payloadBytes, err := common.Marshal(payload)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal query payload failed: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, uri, bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("new query request failed: %w", err)
	}
	if err = Sign(c, req, info.ApiKey); err != nil {
		return 0, nil, fmt.Errorf("sign query request failed: %w", err)
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

// jimengAsyncImageHandler 解析异步图片任务的最终结果并转换为 OpenAI 图片响应。
func jimengAsyncImageHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)

	var result asyncResultResponse
	if err = common.Unmarshal(responseBody, &result); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	if result.Code != 10000 {
		return nil, types.WithOpenAIError(types.OpenAIError{
			Message: result.Message,
			Type:    "jimeng_error",
			Code:    fmt.Sprintf("%d", result.Code),
		}, resp.StatusCode)
	}
	if result.Data.Status != "done" {
		return nil, types.WithOpenAIError(types.OpenAIError{
			Message: fmt.Sprintf("jimeng image task not completed, status: %s", result.Data.Status),
			Type:    "jimeng_error",
			Code:    result.Data.Status,
		}, http.StatusInternalServerError)
	}

	imageResponse := dto.ImageResponse{Created: info.StartTime.Unix()}
	for _, b64 := range result.Data.BinaryDataBase64 {
		imageResponse.Data = append(imageResponse.Data, dto.ImageData{B64Json: b64})
	}
	for _, imageUrl := range result.Data.ImageUrls {
		imageResponse.Data = append(imageResponse.Data, dto.ImageData{Url: imageUrl})
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

package tokenhub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTaskContext(t *testing.T, body string) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeTokenHub,
			ChannelBaseUrl: "https://provider.example/",
		},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}
	return context, info
}

func TestSeedanceStructuredRequestPreservesAllFields(t *testing.T) {
	context, info := newTaskContext(t, `{
		"model":"doubao-seedance-2-5-260628",
		"content":[
			{"type":"text","text":"create a video"},
			{"type":"image_url","image_url":{"url":"asset://image"},"role":"first_frame"},
			{"type":"video_url","video_url":{"url":"https://example.com/reference.mp4"},"role":"reference_video"},
			{"type":"audio_url","audio_url":{"url":"data:audio/mp3;base64,AAAA"},"role":"reference_audio"}
		],
		"resolution":"720p",
		"generate_audio":false,
		"ratio":"adaptive",
		"duration":-1,
		"watermark":false,
		"seed":0,
		"return_last_frame":false,
		"execution_expires_after":3600,
		"tools":[{"type":"web_search"}],
		"output_format":"mov",
		"safety_identifier":"user-123"
	}`)
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)

	require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
	info.UpstreamModelName = "doubao-seedance-2-5-260628"
	requestURL, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://provider.example/v1/videos/generations", requestURL)

	reader, err := adaptor.BuildRequestBody(context, info)
	require.NoError(t, err)
	payload, err := io.ReadAll(reader)
	require.NoError(t, err)
	var request seedanceRequest
	require.NoError(t, common.Unmarshal(payload, &request))

	assert.Equal(t, info.UpstreamModelName, request.Model)
	require.Len(t, request.Content, 4)
	assert.Equal(t, "asset://image", request.Content[1].ImageURL.URL)
	assert.Equal(t, "first_frame", request.Content[1].Role)
	assert.Equal(t, "https://example.com/reference.mp4", request.Content[2].VideoURL.URL)
	assert.Equal(t, "data:audio/mp3;base64,AAAA", request.Content[3].AudioURL.URL)
	require.NotNil(t, request.GenerateAudio)
	assert.False(t, *request.GenerateAudio)
	require.NotNil(t, request.Watermark)
	assert.False(t, *request.Watermark)
	require.NotNil(t, request.Duration)
	assert.Equal(t, -1, *request.Duration)
	assert.Equal(t, "720p", request.Resolution)
	assert.Equal(t, "adaptive", request.Ratio)
	require.NotNil(t, request.Seed)
	assert.Zero(t, *request.Seed)
	require.NotNil(t, request.ReturnLastFrame)
	assert.False(t, *request.ReturnLastFrame)
	require.NotNil(t, request.ExecutionExpiresAfter)
	assert.Equal(t, 3600, *request.ExecutionExpiresAfter)
	require.Len(t, request.Tools, 1)
	assert.Equal(t, "web_search", request.Tools[0]["type"])
	assert.Equal(t, "mov", request.OutputFormat)
	assert.Equal(t, "user-123", request.SafetyIdentifier)
}

func TestSeedanceLegacyRequestConvertsImagesToReferences(t *testing.T) {
	context, info := newTaskContext(t, `{
		"model":"doubao-seedance-2-0-fast-260128",
		"prompt":"animate this image",
		"images":["https://example.com/reference.png"],
		"duration":7
	}`)
	adaptor := &TaskAdaptor{}
	adaptor.Init(info)

	require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
	info.UpstreamModelName = "doubao-seedance-2-0-fast-260128"
	reader, err := adaptor.BuildRequestBody(context, info)
	require.NoError(t, err)
	payload, err := io.ReadAll(reader)
	require.NoError(t, err)
	var request seedanceRequest
	require.NoError(t, common.Unmarshal(payload, &request))

	require.Len(t, request.Content, 2)
	assert.Equal(t, "image_url", request.Content[0].Type)
	assert.Equal(t, "reference_image", request.Content[0].Role)
	assert.Equal(t, "text", request.Content[1].Type)
	assert.Equal(t, "animate this image", request.Content[1].Text)
}

func TestSeedanceRequestValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{
			name: "structured and legacy input conflict",
			body: `{"model":"doubao-seedance-2-0-260128","prompt":"legacy","content":[{"type":"text","text":"structured"}]}`,
			code: "conflicting_video_input",
		},
		{
			name: "2.0 audio without image or video",
			body: `{"model":"doubao-seedance-2-0-260128","content":[{"type":"text","text":"video"},{"type":"audio_url","audio_url":{"url":"asset://audio"}}]}`,
			code: "invalid_content",
		},
		{
			name: "invalid media role",
			body: `{"model":"doubao-seedance-2-0-260128","content":[{"type":"text","text":"video"},{"type":"video_url","video_url":{"url":"https://example.com/video.mp4"},"role":"first_frame"}]}`,
			code: "invalid_content",
		},
		{
			name: "invalid ratio",
			body: `{"model":"doubao-seedance-2-0-260128","content":[{"type":"text","text":"video"}],"ratio":"2:1"}`,
			code: "invalid_ratio",
		},
		{
			name: "zero explicit duration",
			body: `{"model":"doubao-seedance-2-0-260128","content":[{"type":"text","text":"video"}],"duration":0}`,
			code: "invalid_duration",
		},
		{
			name: "2.0 oversized duration",
			body: `{"model":"doubao-seedance-2-0-260128","content":[{"type":"text","text":"video"}],"duration":16}`,
			code: "invalid_duration",
		},
		{
			name: "2.5 oversized duration",
			body: `{"model":"doubao-seedance-2-5-260628","content":[{"type":"text","text":"video"}],"duration":31}`,
			code: "invalid_duration",
		},
		{
			name: "2.5 unsupported resolution",
			body: `{"model":"doubao-seedance-2-5-260628","content":[{"type":"text","text":"video"}],"resolution":"1080p"}`,
			code: "invalid_resolution",
		},
		{
			name: "invalid expiration",
			body: `{"model":"doubao-seedance-2-5-260628","content":[{"type":"text","text":"video"}],"execution_expires_after":3599}`,
			code: "invalid_execution_expires_after",
		},
		{
			name: "invalid output format",
			body: `{"model":"doubao-seedance-2-5-260628","content":[{"type":"text","text":"video"}],"output_format":"avi"}`,
			code: "invalid_output_format",
		},
		{
			name: "oversized safety identifier",
			body: `{"model":"doubao-seedance-2-5-260628","content":[{"type":"text","text":"video"}],"safety_identifier":"12345678901234567890123456789012345678901234567890123456789012345"}`,
			code: "invalid_safety_identifier",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context, info := newTaskContext(t, test.body)
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			taskErr := adaptor.ValidateRequestAndSetAction(context, info)
			require.NotNil(t, taskErr)
			assert.Equal(t, test.code, taskErr.Code)
		})
	}
}

func TestSeedanceModelSpecificInputsAreAccepted(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "2.0 image only with omitted role",
			body: `{"model":"doubao-seedance-2-0-260128","content":[{"type":"image_url","image_url":{"url":"asset://image"}}],"resolution":"4k","ratio":"4:3","duration":-1}`,
		},
		{
			name: "2.5 audio only",
			body: `{"model":"doubao-seedance-2-5-260628","content":[{"type":"audio_url","audio_url":{"url":"asset://audio"}}],"duration":30}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context, info := newTaskContext(t, test.body)
			adaptor := &TaskAdaptor{}
			adaptor.Init(info)
			require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
		})
	}
}

func TestSeedanceProtocolRejectsUnregisteredModel(t *testing.T) {
	adaptor := &TaskAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelBaseUrl: "https://provider.example",
	}})

	_, err := adaptor.BuildRequestURL(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		UpstreamModelName: "future-video-model",
	}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no registered protocol")
}

func TestSeedance25ModelsUseGenerationsV1(t *testing.T) {
	adaptor := &TaskAdaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelBaseUrl: "https://provider.example",
	}})

	for _, modelName := range []string{"doubao-seedance-2-5-260628", "doubao-seedance-2-5-cloud"} {
		t.Run(modelName, func(t *testing.T) {
			requestURL, err := adaptor.BuildRequestURL(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: modelName,
			}})

			require.NoError(t, err)
			assert.Equal(t, "https://provider.example/v1/videos/generations", requestURL)
		})
	}
}

func TestSeedanceTaskResultMapsUsageForTokenSettlement(t *testing.T) {
	tests := []struct {
		name string
		sr   string
	}{
		{name: "numeric resolution", sr: `720`},
		{name: "string resolution", sr: `"720p"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adaptor := &TaskAdaptor{}
			body := `{
				"id":"upstream-task",
				"task_id":"upstream-task",
				"model":"doubao-seedance-2-0-mini-260615",
				"status":"succeeded",
				"content":{"video_url":"https://example.com/result.mp4"},
				"usage":{"completion_tokens":108900,"total_tokens":108900,"SR":` + test.sr + `,"duration":5,"ratio":"16:9"}
			}`
			result, err := adaptor.ParseTaskResult([]byte(body))

			require.NoError(t, err)
			assert.Equal(t, "SUCCESS", result.Status)
			assert.Equal(t, "https://example.com/result.mp4", result.Url)
			assert.Equal(t, 108900, result.CompletionTokens)
			assert.Equal(t, 108900, result.TotalTokens)
		})
	}
}

func TestSeedanceExpiredTaskMapsToFailure(t *testing.T) {
	adaptor := &TaskAdaptor{}
	result, err := adaptor.ParseTaskResult([]byte(`{
		"task_id":"upstream-task",
		"model":"doubao-seedance-2-5-260628",
		"status":"expired"
	}`))

	require.NoError(t, err)
	assert.Equal(t, "FAILURE", result.Status)
	assert.Equal(t, "100%", result.Progress)
	assert.Equal(t, "upstream video generation task expired", result.Reason)
}

func TestSeedanceSubmitResponseKeepsUpstreamTaskIDPrivate(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         42,
			UpstreamModelName: "doubao-seedance-2-0-260128",
		},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			PublicTaskID: "task_public",
		},
		OriginModelName: "doubao-seedance-2-0-260128",
	}
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{
			"output":{"task_id":"upstream-task","task_status":"PENDING"},
			"request_id":"request-id",
			"model":"doubao-seedance-2-0-260128"
		}`)),
	}
	adaptor := &TaskAdaptor{}

	upstreamTaskID, _, taskErr := adaptor.DoResponse(context, response, info)

	require.Nil(t, taskErr)
	assert.Equal(t, "upstream-task", upstreamTaskID)
	var video dto.OpenAIVideo
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &video))
	assert.Equal(t, "task_public", video.ID)
	assert.NotContains(t, recorder.Body.String(), "upstream-task")
}

func TestSeedanceFetchTaskUsesRegisteredQueryEndpoint(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/v1/videos/generations/task/upstream-task", request.URL.Path)
		assert.Equal(t, "Bearer secret-key", request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"task_id":"upstream-task","model":"doubao-seedance-2-5-260628","status":"running"}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	adaptor := &TaskAdaptor{}
	response, err := adaptor.FetchTask(server.URL, "secret-key", map[string]any{
		"task_id": "upstream-task",
		"model":   "doubao-seedance-2-5-260628",
	}, "")

	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

func TestSummarizeErrorResponsePreservesBody(t *testing.T) {
	const body = `{"request_id":"request-id","error":{"code":"gateway_timeout","message":"upstream timed out"},"internal":"not logged"}`
	response := &http.Response{
		Header: make(http.Header),
		Body:   io.NopCloser(strings.NewReader(body)),
	}

	summary := summarizeErrorResponse(response)
	preserved, err := io.ReadAll(response.Body)

	require.NoError(t, err)
	assert.JSONEq(t, `{"request_id":"request-id","error":{"code":"gateway_timeout","message":"upstream timed out"}}`, summary)
	assert.Equal(t, body, string(preserved))
	assert.NotContains(t, summary, "internal")
}

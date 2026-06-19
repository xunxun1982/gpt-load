package proxy

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

var benchmarkTokenCountSink int64

type errorAfterReadCloser struct {
	data []byte
	done bool
}

func compressGzipForResponseHandlerTest(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, err := writer.Write(body)
	if err != nil {
		t.Fatalf("failed to write gzip body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func (r *errorAfterReadCloser) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.data), nil
	}
	return 0, errors.New("test copy error")
}

func (r *errorAfterReadCloser) Close() error {
	return nil
}

func TestShouldCaptureResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("capture enabled", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		group := &models.Group{
			EffectiveConfig: types.SystemSettings{
				EnableRequestBodyLogging: true,
			},
		}
		c.Set("group", group)

		result := shouldCaptureResponse(c)
		assert.True(t, result)
	})

	t.Run("capture disabled", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		group := &models.Group{
			EffectiveConfig: types.SystemSettings{
				EnableRequestBodyLogging: false,
			},
		}
		c.Set("group", group)

		result := shouldCaptureResponse(c)
		assert.False(t, result)
	})

	t.Run("no group in context", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)

		result := shouldCaptureResponse(c)
		assert.False(t, result)
	})
}

func TestTailUsageCaptureKeepsResponseTail(t *testing.T) {
	capture := &tailUsageCapture{
		limit: 10,
	}

	if _, err := capture.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.Write([]byte("defghijkl")); err != nil {
		t.Fatal(err)
	}

	if got := string(capture.buf); got != "cdefghijkl" {
		t.Fatalf("unexpected tail capture: %q", got)
	}
}

func TestHandleNormalResponseSetsEstimatedOutputFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"hello world"}}]}`)),
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	if usage, source, ok := getTokenUsage(c); ok || !usage.IsZero() || source != "" {
		t.Fatalf("unexpected upstream usage: %+v source=%q ok=%v", usage, source, ok)
	}
	assert.Greater(t, getEstimatedOutputTokens(c), int64(0))
}

func TestHandleNormalResponseSkipsEstimatedOutputForError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream failed"}}`)),
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	if usage, source, ok := getTokenUsage(c); ok || !usage.IsZero() || source != "" {
		t.Fatalf("unexpected upstream usage: %+v source=%q ok=%v", usage, source, ok)
	}
	assert.Equal(t, int64(0), getEstimatedOutputTokens(c))
}

func TestHandleNormalResponseCaptureSkipsEstimatedOutputForError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", &models.Group{EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true}})
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader(`plain upstream error`)),
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	assert.Equal(t, int64(0), getEstimatedOutputTokens(c))
	if usage, source, ok := getTokenUsage(c); ok || !usage.IsZero() || source != "" {
		t.Fatalf("unexpected upstream usage: %+v source=%q ok=%v", usage, source, ok)
	}
}

func TestHandleNormalResponseKeepsExplicitUsageOnError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5},"error":{"message":"bad request"}}`)),
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	usage, source, ok := getTokenUsage(c)
	if !ok {
		t.Fatal("expected explicit usage")
	}
	assert.Equal(t, int64(5), usage.TotalTokens)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(c))
}

func TestHandleNormalResponsePrefersUpstreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)),
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	usage, source, ok := getTokenUsage(c)
	if !ok {
		t.Fatal("expected upstream usage")
	}
	assert.Equal(t, int64(12), usage.TotalTokens)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(c))
}

func TestHandleStreamingResponseParsesResponsesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":8,\"total_tokens\":20}}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	usage, source, ok := getTokenUsage(c)
	if !ok {
		t.Fatal("expected upstream usage")
	}
	assert.Equal(t, int64(12), usage.InputTokens)
	assert.Equal(t, int64(8), usage.OutputTokens)
	assert.Equal(t, int64(20), usage.TotalTokens)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(c))
}

func TestHandleStreamingResponseSetsEstimatedOutputFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello world\"}\n\n" +
				"data: [DONE]\n\n",
		)),
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	if usage, source, ok := getTokenUsage(c); ok || !usage.IsZero() || source != "" {
		t.Fatalf("unexpected upstream usage: %+v source=%q ok=%v", usage, source, ok)
	}
	assert.Greater(t, getEstimatedOutputTokens(c), int64(0))
}

func TestHandleStreamingResponseRecordsResponsesFailedRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", &models.Group{EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"event: response.failed\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_123\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"Concurrency limit exceeded for user, please retry later\"}}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	statusCode, exists := c.Get(ctxKeyUpstreamLogicalStatusCode)
	if assert.True(t, exists) {
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
	}
	message, exists := c.Get(ctxKeyUpstreamLogicalErrorMessage)
	if assert.True(t, exists) {
		assert.Contains(t, message, "Concurrency limit exceeded")
	}
	body, exists := c.Get("response_body")
	if assert.True(t, exists) {
		assert.Contains(t, body, "rate_limit_exceeded")
		assert.Contains(t, body, "Concurrency limit exceeded")
	}
	assert.Equal(t, int64(0), getEstimatedOutputTokens(c))
}

func TestHandleNormalResponseSkipsTokenAccountingOnCopyError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: &errorAfterReadCloser{
			data: []byte(`{"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`),
		},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	if usage, source, ok := getTokenUsage(c); ok || !usage.IsZero() || source != "" {
		t.Fatalf("unexpected token usage from truncated body: %+v source=%q ok=%v", usage, source, ok)
	}
	assert.Equal(t, int64(0), getEstimatedOutputTokens(c))
}

func BenchmarkTailUsageCaptureWrite(b *testing.B) {
	payload := bytes.Repeat([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello world\"}}]}\n\n"), 2048)
	b.SetBytes(int64(len(payload)))
	// Go 1.26 supports B.Loop and lets testing manage benchmark timing.
	for b.Loop() {
		capture := &tailUsageCapture{
			limit: maxUsageTailCaptureBytes,
		}
		if _, err := capture.Write(payload); err != nil {
			b.Fatal(err)
		}
		benchmarkTokenCountSink = int64(len(capture.buf))
	}
}

func BenchmarkEstimatedTokenCaptureWrite(b *testing.B) {
	payload := bytes.Repeat([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello 世界\"}}]}\n\n"), 2048)
	b.SetBytes(int64(len(payload)))
	// Go 1.26 supports B.Loop and lets testing manage benchmark timing.
	for b.Loop() {
		var capture estimatedTokenCapture
		if _, err := capture.Write(payload); err != nil {
			b.Fatal(err)
		}
		benchmarkTokenCountSink = capture.Tokens()
	}
}

func TestCollectCodexStreamToResponse(t *testing.T) {
	t.Run("simple text response", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_123","model":"gpt-4","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":" World"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_123","model":"gpt-4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello World"}]}]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "resp_123", result.ID)
		assert.Equal(t, "gpt-4", result.Model)
		assert.Equal(t, "completed", result.Status)
	})

	t.Run("function call response", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_456","model":"gpt-4"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"get_weather"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","delta":"{\"location\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","delta":"\"Tokyo\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_123","name":"get_weather","arguments":"{\"location\":\"Tokyo\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_456","model":"gpt-4","status":"completed","output":[{"type":"function_call","call_id":"call_123","name":"get_weather","arguments":"{\"location\":\"Tokyo\"}"}]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "resp_456", result.ID)
		assert.Len(t, result.Output, 1)
		assert.Equal(t, "function_call", result.Output[0].Type)
	})

	t.Run("stream without completion event", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_789","model":"gpt-4"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Incomplete"}
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		// Should build response from collected data
		assert.Equal(t, "resp_789", result.ID)
		assert.Equal(t, "completed", result.Status)
	})

	t.Run("invalid JSON in stream", func(t *testing.T) {
		streamData := `event: response.created
data: {invalid json}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Text"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_999","status":"completed","output":[]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		// Should handle parse errors gracefully
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("response failed event", func(t *testing.T) {
		streamData := `event: response.failed
data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.4","status":"failed","output":[],"error":{"code":"rate_limit_exceeded","message":"Concurrency limit exceeded for user, please retry later"}}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		if assert.NotNil(t, result) {
			assert.Equal(t, "resp_failed", result.ID)
			assert.Equal(t, "failed", result.Status)
			if assert.NotNil(t, result.Error) {
				assert.Equal(t, "rate_limit_exceeded", result.Error.Code)
				assert.Contains(t, result.Error.Message, "Concurrency limit exceeded")
			}
		}
	})

	t.Run("empty stream", func(t *testing.T) {
		resp := &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte{})),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		// Should return a minimal response
		assert.Equal(t, "completed", result.Status)
	})

	t.Run("gzip compressed stream", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_zip","model":"gpt-4","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello Zip"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_zip","model":"gpt-4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello Zip"}]}]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(bytes.NewReader(compressGzipForResponseHandlerTest(t, []byte(streamData)))),
			Header: http.Header{
				"Content-Encoding": []string{"gzip"},
			},
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "resp_zip", result.ID)
		assert.Equal(t, "gpt-4", result.Model)
		assert.Equal(t, "completed", result.Status)
		assert.Len(t, result.Output, 1)
	})
}

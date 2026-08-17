package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"gpt-load/internal/models"
	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var benchmarkTokenCountSink int64

type errorAfterReadCloser struct {
	data []byte
	done bool
}

type dataAndErrorReadCloser struct {
	data []byte
	done bool
}

type errorGinResponseWriter struct {
	gin.ResponseWriter
}

type failAfterWritesGinResponseWriter struct {
	gin.ResponseWriter
	successfulWrites int
	writes           int
}

type shortWriteErrorWriter struct {
	n int
}

type countingReadCloser struct {
	reader io.Reader
	read   int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func (r *countingReadCloser) Close() error { return nil }

func (w shortWriteErrorWriter) Write(p []byte) (int, error) {
	if w.n > len(p) {
		w.n = len(p)
	}
	return w.n, errors.New("short write")
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

func compressGzipSegmentsForResponseHandlerTest(t *testing.T, first, trailing []byte) ([]byte, int) {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(first); err != nil {
		t.Fatalf("failed to write first gzip segment: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("failed to flush first gzip segment: %v", err)
	}
	split := buf.Len()
	if _, err := writer.Write(trailing); err != nil {
		t.Fatalf("failed to write trailing gzip segment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close segmented gzip writer: %v", err)
	}
	return buf.Bytes(), split
}

func largeBase64PayloadForResponseHandlerTest(size int) string {
	rng := rand.New(rand.NewSource(42))
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}
	return base64.StdEncoding.EncodeToString(data)
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

func (r *dataAndErrorReadCloser) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) > 0 {
		return n, nil
	}
	r.done = true
	return n, errors.New("test read error")
}

func (r *dataAndErrorReadCloser) Close() error {
	return nil
}

func (w errorGinResponseWriter) Write(p []byte) (int, error) {
	return len(p), errors.New("test client write error")
}

func (w *failAfterWritesGinResponseWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > w.successfulWrites {
		return len(p), errors.New("test trailing client write error")
	}
	return w.ResponseWriter.Write(p)
}

type alwaysErrorReadCloser struct{}

func (r alwaysErrorReadCloser) Read(_ []byte) (int, error) {
	return 0, errors.New("test read error")
}

func (r alwaysErrorReadCloser) Close() error {
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

func TestHandleCodexForcedStreamResponseSanitizesErrorLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logHook := captureGlobalLogrusEntries(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	bearerToken := strings.Repeat("a", 32)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"event: response.failed\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream rejected Bearer " + bearerToken + " for operator@example.invalid\"}}}\n\n",
		)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	ps := &ProxyServer{}
	ps.handleCodexForcedStreamResponse(c, resp)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	logOutput := logrusHookText(logHook)
	assert.NotContains(t, logOutput, bearerToken)
	assert.NotContains(t, logOutput, "operator@example.invalid")
	assert.Contains(t, logOutput, "Bearer [REDACTED]")
	assert.Contains(t, logOutput, "[REDACTED_EMAIL]")
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

func TestHandleNormalResponseRecordsLargeCompressedResponsesFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(`{"id":"resp_failed","status":"failed","error":{"code":"server_error","message":"temporary upstream failure"},"padding":"` + strings.Repeat("x", maxResponseCaptureBytes+1024) + `"}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressGzipForResponseHandlerTest(t, body))),
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	statusCode, _, logicalFailure := logicalStatusFromContext(c)
	require.True(t, logicalFailure)
	assert.Equal(t, http.StatusBadGateway, statusCode)
}

func TestRetryableStreamProbeDetectsLeadingFailureAndReplaysSuccess(t *testing.T) {
	t.Parallel()

	t.Run("leading failure", func(t *testing.T) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader("event: response.failed\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"rate limit exceeded\"}}}\n\n")),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		statusCode, message, failed := retryableStreamProbe(resp)
		require.True(t, failed)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
		assert.Contains(t, message, "rate limit")
		replayed, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(replayed), "response.failed")
	})

	t.Run("successful prefix is replayed", func(t *testing.T) {
		const stream = "event: response.created\n" +
			"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}},
		}

		_, _, failed := retryableStreamProbe(resp)
		replayed, err := io.ReadAll(resp.Body)

		require.NoError(t, err)
		assert.False(t, failed)
		assert.Equal(t, stream, string(replayed))
	})

	t.Run("probe read error is replayed to the response handler", func(t *testing.T) {
		const stream = "event: ping\n" +
			"data: {\"type\":\"ping\"}\n\n"
		resp := &http.Response{
			Body:   &dataAndErrorReadCloser{data: []byte(stream)},
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		_, _, failed := retryableStreamProbe(resp)
		require.False(t, failed)

		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		(&ProxyServer{}).handleStreamingResponse(c, resp)

		assert.Equal(t, stream, w.Body.String())
		_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
		assert.True(t, processingFailed)
	})

	t.Run("failure after response prelude is retryable", func(t *testing.T) {
		const stream = "event: response.created\n" +
			"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
			"event: response.in_progress\n" +
			"data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
			"event: response.failed\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"rate limit exceeded\"}}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		statusCode, _, failed := retryableStreamProbe(resp)
		replayed, err := io.ReadAll(resp.Body)

		require.NoError(t, err)
		assert.True(t, failed)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
		assert.Equal(t, stream, string(replayed))
	})

	t.Run("Claude message prelude and ping remain retryable", func(t *testing.T) {
		const stream = "event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"content\":[]}}\n\n" +
			"event: ping\n" +
			"data: {\"type\":\"ping\"}\n\n" +
			"event: error\n" +
			"data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"temporary capacity limit\"}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		statusCode, message, failed := retryableStreamProbe(resp)

		require.True(t, failed)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
		assert.Contains(t, message, "temporary capacity")
		replayed, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, stream, string(replayed))
	})

	t.Run("OpenAI Chat rate limit error maps to 429", func(t *testing.T) {
		const stream = "data: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"requests are temporarily limited\"}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		statusCode, _, failed := retryableStreamProbe(resp)

		require.True(t, failed)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
	})

	t.Run("Gemini numeric resource exhaustion maps to 429", func(t *testing.T) {
		const stream = "data: {\"error\":{\"code\":429,\"message\":\"Resource exhausted\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		statusCode, _, failed := retryableStreamProbe(resp)

		require.True(t, failed)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
	})

	t.Run("Gemini transport fragments are reassembled before parsing", func(t *testing.T) {
		resp := &http.Response{
			Body: io.NopCloser(io.MultiReader(
				strings.NewReader("data: {"),
				strings.NewReader(`"error":{"code":429,`),
				strings.NewReader(`"message":"Resource exhausted",`),
				strings.NewReader(`"status":"RESOURCE_EXHAUSTED"}}`+"\n\n"),
			)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		statusCode, _, failed := retryableStreamProbe(resp)

		require.True(t, failed)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
	})

	t.Run("Anthropic overload and timeout preserve official status", func(t *testing.T) {
		tests := []struct {
			name       string
			errorType  string
			statusCode int
		}{
			{name: "overload", errorType: "overloaded_error", statusCode: 529},
			{name: "timeout", errorType: "timeout_error", statusCode: http.StatusGatewayTimeout},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				stream := fmt.Sprintf("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":%q,\"message\":\"temporary failure\"}}\n\n", tt.errorType)
				resp := &http.Response{
					Body:   io.NopCloser(strings.NewReader(stream)),
					Header: http.Header{"Content-Type": []string{"text/event-stream"}},
				}

				statusCode, _, failed := retryableStreamProbe(resp)

				require.True(t, failed)
				assert.Equal(t, tt.statusCode, statusCode)
			})
		}
	})

	t.Run("OpenAI top-level error event maps to 429", func(t *testing.T) {
		const stream = "event: error\n" +
			"data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"Rate limit reached\",\"param\":null,\"sequence_number\":1}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		statusCode, message, failed := retryableStreamProbe(resp)

		require.True(t, failed)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
		assert.Equal(t, "Rate limit reached", message)
	})

	t.Run("response incomplete is not retried", func(t *testing.T) {
		const stream = "event: response.incomplete\n" +
			"data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		_, _, failed := retryableStreamProbe(resp)
		replayed, err := io.ReadAll(resp.Body)

		require.NoError(t, err)
		assert.False(t, failed)
		assert.Equal(t, stream, string(replayed))
	})

	t.Run("failure after content delta is not retryable", func(t *testing.T) {
		const stream = "event: response.created\n" +
			"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
			"event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
			"event: response.failed\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"rate limit exceeded\"}}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		_, _, failed := retryableStreamProbe(resp)
		replayed, err := io.ReadAll(resp.Body)

		require.NoError(t, err)
		assert.False(t, failed)
		assert.Equal(t, stream, string(replayed))
	})

	t.Run("probe limit closes retry window and preserves the stream", func(t *testing.T) {
		prelude := strings.Repeat("event: ping\ndata: {\"type\":\"ping\"}\n\n", maxRetryableStreamProbeBytes/32)
		stream := prelude + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"late failure\"}}\n\n"
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}

		_, _, failed := retryableStreamProbe(resp)
		replayed, err := io.ReadAll(resp.Body)

		require.NoError(t, err)
		assert.False(t, failed)
		assert.Equal(t, stream, string(replayed))
	})

	t.Run("compressed stream is replayed byte-for-byte", func(t *testing.T) {
		decoded := []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"compressed failure\"}}\n\n")
		compressed := compressGzipForResponseHandlerTest(t, decoded)
		resp := &http.Response{
			Body: io.NopCloser(bytes.NewReader(compressed)),
			Header: http.Header{
				"Content-Type":     []string{"text/event-stream"},
				"Content-Encoding": []string{"gzip"},
			},
		}

		_, _, failed := retryableStreamProbe(resp)
		replayed, err := io.ReadAll(resp.Body)

		require.NoError(t, err)
		assert.False(t, failed)
		assert.Equal(t, compressed, replayed)

		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp.Body = io.NopCloser(bytes.NewReader(replayed))
		ps := &ProxyServer{}
		ps.handleStreamingResponse(c, resp)

		statusCode, _, logicalFailure := logicalStatusFromContext(c)
		require.True(t, logicalFailure)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
		assert.Equal(t, compressed, w.Body.Bytes())
	})
}

func TestRetryableResponseProbeDetectsNonStreamFailureAndReplaysBody(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	body := []byte(`{"id":"resp-limited","status":"failed","error":{"code":"rate_limit_exceeded","message":"temporarily limited"},"output":[]}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	statusCode, message, failed := retryableResponseProbe(c, resp)
	replayed, err := io.ReadAll(resp.Body)

	require.NoError(t, err)
	require.True(t, failed)
	assert.Equal(t, http.StatusTooManyRequests, statusCode)
	assert.Equal(t, "temporarily limited", message)
	assert.Equal(t, body, replayed)
}

func TestRetryableResponseProbeBoundsSuccessfulResponsePrefixAndReplaysBody(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	body := []byte(`{"id":"resp-ok","status":"completed","output":[],"padding":"` + strings.Repeat("x", maxResponseCaptureBytes+1024) + `"}`)
	upstreamBody := &countingReadCloser{reader: bytes.NewReader(body)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       upstreamBody,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	_, _, failed := retryableResponseProbe(c, resp)
	probeRead := upstreamBody.read
	replayed, err := io.ReadAll(resp.Body)

	require.NoError(t, err)
	assert.False(t, failed)
	assert.LessOrEqual(t, probeRead, 4*1024)
	assert.Equal(t, body, replayed)
}

func TestRetryableResponseProbeStopsAfterFailureMessageAndReplaysBody(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	body := []byte(`{"status":"failed","error":{"message":"temporary rate limit"},"padding":"` + strings.Repeat("x", maxResponseCaptureBytes+1024) + `"}`)
	upstreamBody := &countingReadCloser{reader: bytes.NewReader(body)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       upstreamBody,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	statusCode, message, failed := retryableResponseProbe(c, resp)
	probeRead := upstreamBody.read
	replayed, err := io.ReadAll(resp.Body)

	require.NoError(t, err)
	require.True(t, failed)
	assert.Equal(t, http.StatusTooManyRequests, statusCode)
	assert.Equal(t, "temporary rate limit", message)
	assert.LessOrEqual(t, probeRead, 4*1024)
	assert.Equal(t, body, replayed)
}

func TestSSELogicalFailureCapturePreservesNumericErrorCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{
			name: "top-level error",
			data: `data: {"type":"error","error":{"code":9007199254740993,"message":"failed"}}` + "\n",
		},
		{
			name: "nested response error",
			data: `data: {"type":"response.failed","response":{"status":"failed","error":{"code":9007199254740993,"message":"failed"}}}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capture sseLogicalFailureCapture
			_, err := capture.Write([]byte(tt.data))

			require.NoError(t, err)
			assert.Equal(t, "9007199254740993", capture.errorCode)
		})
	}
}

func TestSSELogicalFailureCaptureClassifiesEquivalentNumericCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		code       string
		wantStatus int
	}{
		{name: "decimal rate limit", code: "429.0", wantStatus: http.StatusTooManyRequests},
		{name: "exponent rate limit", code: "4.29e2", wantStatus: http.StatusTooManyRequests},
		{name: "decimal overloaded", code: "529.00", wantStatus: 529},
		{name: "exponent timeout", code: "5.04e2", wantStatus: http.StatusGatewayTimeout},
		{name: "nearby high precision value", code: "429.0000000000000001", wantStatus: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capture sseLogicalFailureCapture
			_, err := capture.Write([]byte(`data: {"type":"error","error":{"code":` + tt.code + `,"message":"failed"}}` + "\n"))

			require.NoError(t, err)
			assert.Equal(t, tt.code, capture.errorCode)
			assert.Equal(t, tt.wantStatus, capture.statusCode)
		})
	}
}

func TestSSELogicalFailureCaptureOversizedStatusFallback(t *testing.T) {
	t.Parallel()

	body := `data: {"error":{"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"},"padding":"` +
		strings.Repeat("x", maxCodexStreamLineBytes) + `"}`
	var capture sseLogicalFailureCapture
	_, err := capture.Write([]byte(body))

	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, capture.statusCode)
	assert.True(t, capture.firstSemanticFailed)
}

func BenchmarkRetryableStreamProbe(b *testing.B) {
	stream := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n"
	b.ReportAllocs()
	for range b.N {
		resp := &http.Response{
			Body:   io.NopCloser(strings.NewReader(stream)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}
		_, _, _ = retryableStreamProbe(resp)
	}
}

func BenchmarkRetryableStreamProbeNonSSE(b *testing.B) {
	resp := &http.Response{
		Body:   http.NoBody,
		Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
	}
	b.ReportAllocs()
	for range b.N {
		_, _, _ = retryableStreamProbe(resp)
	}
}

func TestHandleNormalResponseRecordsIdentityEncodedResponsesFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"status":"failed","error":{"code":"server_error","message":"temporary upstream failure"}}`)),
		Header:     http.Header{"Content-Encoding": []string{"identity"}},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	statusCode, _, logicalFailure := logicalStatusFromContext(c)
	require.True(t, logicalFailure)
	assert.Equal(t, http.StatusBadGateway, statusCode)
}

func TestHandleNormalResponseMarksUnsupportedEncodedResponsesStatusUnverified(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`opaque response`)),
		Header:     http.Header{"Content-Encoding": []string{"custom"}},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	unverified, exists := c.Get(ctxKeyResponsesStatusUnverified)
	require.True(t, exists)
	assert.Equal(t, true, unverified)
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

func TestHandleStreamingResponseTreatsCompletedEventAsSuccessBeforeEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: &errorAfterReadCloser{data: []byte(
			"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\"}}\n\n",
		)},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	assert.False(t, processingFailed)
	assert.Contains(t, w.Body.String(), `"type":"response.completed"`)
}

func TestHandleStreamingResponseTreatsIncompleteEventAsNonFailingTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte("event: response.incomplete\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_incomplete\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       &dataAndErrorReadCloser{data: body},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	assert.False(t, processingFailed)
	_, _, logicalFailure := logicalStatusFromContext(c)
	assert.False(t, logicalFailure)
	assert.Equal(t, body, w.Body.Bytes())
}

func TestHandleStreamingResponseTreatsIdentityCompletedEventAsSuccessBeforeEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(
		"event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\"}}\n\n",
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       &errorAfterReadCloser{data: body},
		Header:     http.Header{"Content-Encoding": []string{"identity"}},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	assert.False(t, processingFailed)
	assert.Equal(t, body, w.Body.Bytes())
}

func TestHandleStreamingResponseTreatsEncodedCompletedEventAsSuccessWithTrailingUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := compressGzipForResponseHandlerTest(t, []byte(
		"event: response.completed\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\"}}\n\n",
	))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       &dataAndErrorReadCloser{data: body},
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	assert.False(t, processingFailed)
	assert.Equal(t, body, w.Body.Bytes())
}

func TestHandleStreamingResponseForwardsCompleteEncodedBodyAfterCompletedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	terminal := []byte(
		"event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\"}}\n\n",
	)
	body, split := compressGzipSegmentsForResponseHandlerTest(t, terminal, []byte("data: [DONE]\n\n"))
	require.Less(t, split, len(body))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(io.MultiReader(
			bytes.NewReader(body[:split]),
			bytes.NewReader(body[split:]),
		)),
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	assert.False(t, processingFailed)
	assert.Equal(t, body, w.Body.Bytes())
	reader, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, append(terminal, []byte("data: [DONE]\n\n")...), decoded)
}

func TestHandleStreamingResponseMarksDrainWriteFailureAfterEncodedCompletedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Writer = &failAfterWritesGinResponseWriter{
		ResponseWriter:   c.Writer,
		successfulWrites: 1,
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	terminal := []byte(
		"event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\",\"usage\":{\"input_tokens\":12,\"output_tokens\":8,\"total_tokens\":20}}}\n\n",
	)
	body, split := compressGzipSegmentsForResponseHandlerTest(t, terminal, []byte("data: [DONE]\n\n"))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(io.MultiReader(
			bytes.NewReader(body[:split]),
			bytes.NewReader(body[split:]),
		)),
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	assert.True(t, processingFailed)
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(20), usage.TotalTokens)
}

func TestHandleStreamingResponseKeepsEncodedCompletedEventFailedOnClientWriteError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Writer = errorGinResponseWriter{ResponseWriter: c.Writer}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := compressGzipForResponseHandlerTest(t, []byte(
		"event: response.completed\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\"}}\n\n",
	))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	assert.True(t, processingFailed)
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

func TestHandleStreamingResponseRecordsTopLevelErrorAfterSSEComments(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "bare JSON error",
			body: ": PING\n\n: PING\n\n" +
				`{"error":{"message":"openai_error","type":"bad_response_status_code","param":"","code":"bad_response_status_code"}}`,
		},
		{
			name: "data event error",
			body: ": PING\n\n" +
				`data: {"error":{"message":"openai_error","type":"bad_response_status_code","param":"","code":"bad_response_status_code"}}` + "\n\n",
		},
		{
			name: "oversized bare JSON error",
			body: `{"error":{"message":"openai_error","type":"bad_response_status_code","param":"","code":"bad_response_status_code"},"padding":"` +
				strings.Repeat("x", maxCodexStreamLineBytes) + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}

			ps := &ProxyServer{}
			ps.handleStreamingResponse(c, resp)

			statusCode, exists := c.Get(ctxKeyUpstreamLogicalStatusCode)
			if assert.True(t, exists) {
				assert.Equal(t, http.StatusBadGateway, statusCode)
			}
			message, exists := c.Get(ctxKeyUpstreamLogicalErrorMessage)
			if assert.True(t, exists) {
				assert.Equal(t, "openai_error", message)
			}
		})
	}
}

func TestHandleStreamingResponseIgnoresNonErrorSSELines(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "comment only", body: ": PING\n\n"},
		{name: "normal data event", body: `data: {"id":"chatcmpl-test","choices":[]}` + "\n\n"},
		{name: "bare success JSON", body: `{"ok":true}`},
		{name: "null error", body: `data: {"error":null,"id":"chatcmpl-test"}` + "\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}

			ps := &ProxyServer{}
			ps.handleStreamingResponse(c, resp)

			_, exists := c.Get(ctxKeyUpstreamLogicalStatusCode)
			assert.False(t, exists)
		})
	}
}

func TestHandleStreamingResponseMarksInvalidResponsesEventUnverified(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {invalid}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
		)),
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	unverified, exists := c.Get(ctxKeyResponsesStatusUnverified)
	require.True(t, exists)
	assert.Equal(t, true, unverified)
}

func TestHandleStreamingResponseSanitizesCapturedLogicalFailureBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", &models.Group{EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"event: response.failed\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream leaked operator@example.invalid\"}}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	body, exists := c.Get("response_body")
	if assert.True(t, exists) {
		bodyStr, ok := body.(string)
		if assert.True(t, ok) {
			assert.NotContains(t, bodyStr, "operator@example.invalid")
			assert.Contains(t, bodyStr, "[REDACTED_EMAIL]")
		}
	}
}

func TestSetLogicalFailureContextSanitizesSyntheticBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	setLogicalFailureContext(c, http.StatusBadGateway, "server_error", "upstream leaked operator@example.invalid")

	body, exists := c.Get("response_body")
	if assert.True(t, exists) {
		bodyStr, ok := body.(string)
		if assert.True(t, ok) {
			assert.NotContains(t, bodyStr, "operator@example.invalid")
			assert.Contains(t, bodyStr, "[REDACTED_EMAIL]")
		}
	}
}

func TestHandleCodexForcedStreamResponseUsesBadGatewayForNonRateLimitFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"event: response.failed\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"server_error\",\"message\":\"upstream failed\"}}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: make(http.Header),
	}

	ps := &ProxyServer{}
	ps.handleCodexForcedStreamResponse(c, resp)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	statusCode, exists := c.Get(ctxKeyUpstreamLogicalStatusCode)
	if assert.True(t, exists) {
		assert.Equal(t, http.StatusBadGateway, statusCode)
	}
}

func TestHandleCodexForcedStreamResponseKeepsFailedEventTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"event: response.failed\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"server_error\",\"message\":\"upstream failed\"}}}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_completed\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[]}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: make(http.Header),
	}

	ps := &ProxyServer{}
	ps.handleCodexForcedStreamResponse(c, resp)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	statusCode, exists := c.Get(ctxKeyUpstreamLogicalStatusCode)
	if assert.True(t, exists) {
		assert.Equal(t, http.StatusBadGateway, statusCode)
	}
}

func TestHandleCodexForcedStreamResponseAppliesFunctionCallConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyFunctionCallEnabled, true)
	c.Set(ctxKeyTriggerSignal, "<<CALL_forced>>")
	setTestFunctionCallSecuritySession(c, "<<CALL_forced>>", "web_search")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_forced\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Let me search.\\n<<CALL_forced>>\\n<invoke name=\\\"web_search\\\"><parameter name=\\\"query\\\">weather</parameter></invoke>\"}]}],\"usage\":{\"input_tokens\":7,\"output_tokens\":5,\"total_tokens\":12}}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: make(http.Header),
	}

	ps := &ProxyServer{}
	ps.handleCodexForcedStreamResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	output := w.Body.String()
	assert.Contains(t, output, `"type":"function_call"`)
	assert.Contains(t, output, `"name":"web_search"`)
	assert.NotContains(t, output, "<invoke")
	assert.NotContains(t, output, "<<CALL_forced>>")
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
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

func TestHandleNormalResponseLogsDecodedCompressedBodyWithoutChangingClientBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
	compressedBody := compressGzipForResponseHandlerTest(t, body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", &models.Group{EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressedBody)),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	assert.Equal(t, compressedBody, w.Body.Bytes())
	rawLogBody, exists := c.Get("response_body")
	require.True(t, exists)
	logBody, ok := rawLogBody.(string)
	require.True(t, ok)
	assert.Contains(t, logBody, `"content":"pong"`)
	assert.NotContains(t, logBody, "\x1f\x8b")
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestHandleNormalResponseParsesCompressedUsageWithoutBodyLogging(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
	compressedBody := compressGzipForResponseHandlerTest(t, body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressedBody)),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	assert.Equal(t, compressedBody, w.Body.Bytes())
	_, exists := c.Get("response_body")
	assert.False(t, exists)
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestHandleNormalResponseParsesCompressedUsagePastCaptureLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payload := largeBase64PayloadForResponseHandlerTest(96 * 1024)
	body := []byte(`{"choices":[{"message":{"content":"` + payload + `"}}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
	compressedBody := compressGzipForResponseHandlerTest(t, body)
	require.Greater(t, len(compressedBody), maxResponseCaptureBytes)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressedBody)),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	assert.Equal(t, compressedBody, w.Body.Bytes())
	_, exists := c.Get("response_body")
	assert.False(t, exists)
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestHandleNormalResponseParsesCompressedUsagePastLogCaptureLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payload := largeBase64PayloadForResponseHandlerTest(96 * 1024)
	body := []byte(`{"choices":[{"message":{"content":"` + payload + `"}}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
	compressedBody := compressGzipForResponseHandlerTest(t, body)
	require.Greater(t, len(compressedBody), maxResponseCaptureBytes)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", &models.Group{EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressedBody)),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	ps := &ProxyServer{}
	ps.handleNormalResponse(c, resp)

	assert.Equal(t, compressedBody, w.Body.Bytes())
	rawLogBody, exists := c.Get("response_body")
	require.True(t, exists)
	logBody, ok := rawLogBody.(string)
	require.True(t, ok)
	assert.Contains(t, logBody, "compressed response omitted")
	assert.NotContains(t, logBody, "\x1f\x8b")
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestHandleStreamingResponseLogsDecodedCompressedBodyWithoutChangingClientBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	streamBody := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello Zip\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\n" +
		"data: [DONE]\n\n")
	compressedBody := compressGzipForResponseHandlerTest(t, streamBody)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", &models.Group{EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressedBody)),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	assert.Equal(t, compressedBody, w.Body.Bytes())
	rawLogBody, exists := c.Get("response_body")
	require.True(t, exists)
	logBody, ok := rawLogBody.(string)
	require.True(t, ok)
	assert.Contains(t, logBody, "Hello Zip")
	assert.NotContains(t, logBody, "\x1f\x8b")
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestHandleStreamingResponseLogsCompressedUsagePastCaptureLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payload := largeBase64PayloadForResponseHandlerTest(96 * 1024)
	streamBody := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + payload + "\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\n" +
		"data: [DONE]\n\n")
	compressedBody := compressGzipForResponseHandlerTest(t, streamBody)
	require.Greater(t, len(compressedBody), maxResponseCaptureBytes)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", &models.Group{EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressedBody)),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	assert.Equal(t, compressedBody, w.Body.Bytes())
	rawLogBody, exists := c.Get("response_body")
	require.True(t, exists)
	logBody, ok := rawLogBody.(string)
	require.True(t, ok)
	assert.NotContains(t, logBody, "\x1f\x8b")
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestHandleStreamingResponseParsesCompressedUsagePastCaptureLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payload := largeBase64PayloadForResponseHandlerTest(96 * 1024)
	streamBody := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + payload + "\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\n" +
		"data: [DONE]\n\n")
	compressedBody := compressGzipForResponseHandlerTest(t, streamBody)
	require.Greater(t, len(compressedBody), maxResponseCaptureBytes)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(compressedBody)),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	ps := &ProxyServer{}
	ps.handleStreamingResponse(c, resp)

	assert.Equal(t, compressedBody, w.Body.Bytes())
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.TotalTokens)
}

func TestLimitedResponseCaptureWriter(t *testing.T) {
	var downstream bytes.Buffer
	capture := newLimitedResponseCaptureWriter(&downstream, 5)

	n, err := capture.Write([]byte("hello world"))

	require.NoError(t, err)
	assert.Equal(t, len("hello world"), n)
	assert.Equal(t, "hello world", downstream.String())
	assert.Equal(t, "hello", capture.String())

	capture = newLimitedResponseCaptureWriter(shortWriteErrorWriter{n: 3}, 5)
	n, err = capture.Write([]byte("abcdef"))

	require.Error(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, "abc", capture.String())

	capture = newLimitedResponseCaptureWriter(io.Discard, len("hello 世")-1)
	n, err = capture.Write([]byte("hello 世界"))

	require.NoError(t, err)
	assert.Equal(t, len("hello 世界"), n)
	assert.True(t, utf8.ValidString(capture.String()))
	assert.Equal(t, "hello ", capture.String())
}

func BenchmarkTailUsageCaptureWrite(b *testing.B) {
	payload := bytes.Repeat([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello world\"}}]}\n\n"), 2048)
	b.SetBytes(int64(len(payload)))
	// Go 1.24+ supports B.Loop and lets testing manage benchmark timing.
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

func BenchmarkLimitedResponseCaptureWriter(b *testing.B) {
	payload := bytes.Repeat([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hello world\"}}\n\n"), 2048)
	b.SetBytes(int64(len(payload)))
	// Go 1.24+ supports B.Loop and lets testing manage benchmark timing.
	for b.Loop() {
		capture := newLimitedResponseCaptureWriter(io.Discard, maxResponseCaptureBytes)
		if _, err := capture.Write(payload); err != nil {
			b.Fatal(err)
		}
		benchmarkTokenCountSink = int64(len(capture.String()))
	}
}

func BenchmarkEstimatedTokenCaptureWrite(b *testing.B) {
	payload := bytes.Repeat([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello 世界\"}}]}\n\n"), 2048)
	b.SetBytes(int64(len(payload)))
	// Go 1.24+ supports B.Loop and lets testing manage benchmark timing.
	for b.Loop() {
		var capture estimatedTokenCapture
		if _, err := capture.Write(payload); err != nil {
			b.Fatal(err)
		}
		benchmarkTokenCountSink = capture.Tokens()
	}
}

func TestCollectCodexStreamToResponse(t *testing.T) {
	t.Run("nil response", func(t *testing.T) {
		result, err := collectCodexStreamToResponse(nil)

		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("nil response body", func(t *testing.T) {
		result, err := collectCodexStreamToResponse(&http.Response{})

		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("rejects oversized stream line", func(t *testing.T) {
		streamData := "data: " + strings.Repeat("x", maxCodexStreamLineBytes+1) + "\n"
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.Error(t, err)
		assert.Nil(t, result)
	})

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

	t.Run("reasoning item preserves encrypted content", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_reasoning","model":"gpt-5"}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_123","status":"completed","encrypted_content":"gAAAA-test","summary":[{"type":"summary_text","text":"brief"}]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Output, 1)
		assert.Equal(t, "reasoning", result.Output[0].Type)
		assert.Equal(t, "rs_123", result.Output[0].ID)
		assert.Equal(t, "completed", result.Output[0].Status)
		assert.Equal(t, "gAAAA-test", result.Output[0].EncryptedContent)
		assert.JSONEq(t, `[{"type":"summary_text","text":"brief"}]`, string(result.Output[0].Summary))
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

func TestHandleCodexForcedStreamResponseSanitizesEncryptedContentForLog(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_reasoning","model":"gpt-5"}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_123","status":"completed","encrypted_content":"gAAAA-response-reasoning","summary":[]}}

data: [DONE]
`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(streamData)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	group := &models.Group{
		EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true},
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("group", group)

	ps := &ProxyServer{}
	ps.handleCodexForcedStreamResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "gAAAA-response-reasoning")
	rawLogBody, exists := c.Get("response_body")
	require.True(t, exists)
	logBody, ok := rawLogBody.(string)
	require.True(t, ok)
	assert.NotContains(t, logBody, "gAAAA-response-reasoning")
	assert.Contains(t, logBody, `"encrypted_content": "[REDACTED]"`)
}

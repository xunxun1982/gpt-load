package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestCodexDegradationMitigationShouldEnable(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5","stream":true}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex/v1/responses", strings.NewReader(string(body)))

	standard := &models.Group{
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	assert.True(t, codexDegradationMitigationShouldEnable(c, standard, standard, body, true))

	parent := &models.Group{
		GroupType:   "aggregate",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	child := &models.Group{
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{},
	}
	assert.True(t, codexDegradationMitigationShouldEnable(c, child, parent, body, true))

	child.Config = datatypes.JSONMap{"codex_degradation_mitigation_enabled": true}
	parent.Config = datatypes.JSONMap{}
	assert.True(t, codexDegradationMitigationShouldEnable(c, child, parent, body, true))

	assert.False(t, codexDegradationMitigationShouldEnable(c, child, parent, []byte(`{"stream":false}`), false))
	assert.False(t, codexDegradationMitigationShouldEnable(c, child, parent, []byte(`{"stream":true,"reasoning":false}`), true))

	nonCodex := &models.Group{
		GroupType:   "standard",
		ChannelType: "openai",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	assert.False(t, codexDegradationMitigationShouldEnable(c, nonCodex, nonCodex, body, true))
}

func TestCodexDegradationMitigationContinuationPayload(t *testing.T) {
	t.Parallel()

	base := []byte(`{"model":"gpt-5","stream":true,"previous_response_id":"resp_old","include":["web_search_call.action.sources"],"input":[{"role":"user","content":"hi"}]}`)
	reasoning := map[string]any{
		"type":              "reasoning",
		"id":                "rs_1",
		"status":            "completed",
		"encrypted_content": "enc_1",
	}

	payload, err := buildCodexDegradationMitigationContinuationPayload(base, []map[string]any{reasoning}, "Continue thinking")
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(payload, &out))
	assert.Equal(t, true, out["stream"])
	assert.NotContains(t, out, "previous_response_id")

	include := out["include"].([]any)
	assert.Contains(t, include, "web_search_call.action.sources")
	assert.Contains(t, include, responsesEncryptedReasoning)

	input := out["input"].([]any)
	require.Len(t, input, 3)
	assert.Equal(t, "reasoning", input[1].(map[string]any)["type"])
	assert.Equal(t, "commentary", input[2].(map[string]any)["phase"])
}

func TestMergeResponsesEncryptedIncludeHandlesUncomparableValues(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"include": []any{
			map[string]any{"type": "unexpected"},
			map[string]any{"type": "unexpected"},
		},
	}

	require.NotPanics(t, func() {
		mergeResponsesEncryptedInclude(body)
	})
	assert.Contains(t, body["include"].([]any), responsesEncryptedReasoning)
}

func TestCodexDegradationMitigationFoldDropsTruncatedOutputAndContinuesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	parent := &models.Group{
		GroupType:   "aggregate",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	child := &models.Group{
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	baseBody := []byte(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	first := codexMitigationTestResponse(codexMitigationTestSSE("resp_1", "discarded-first", 516, "enc_1"))
	second := codexMitigationTestResponse(codexMitigationTestSSE("resp_2", "final-answer", 20, "enc_2"))

	continuations := 0
	roundTrip := func(body []byte) (*http.Response, error) {
		continuations++
		require.Equal(t, 1, continuations)

		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		include := payload["include"].([]any)
		count := 0
		for _, item := range include {
			if item == responsesEncryptedReasoning {
				count++
			}
		}
		assert.Equal(t, 1, count)

		input := payload["input"].([]any)
		require.Len(t, input, 3)
		assert.Equal(t, "reasoning", input[1].(map[string]any)["type"])
		assert.Equal(t, "commentary", input[2].(map[string]any)["phase"])
		return second, nil
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex/v1/responses", strings.NewReader(string(baseBody)))

	ps := &ProxyServer{}
	ps.handleCodexDegradationMitigationStreamingResponse(c, first, baseBody, child, parent, roundTrip)

	require.Equal(t, 1, continuations)
	output := w.Body.String()
	assert.NotContains(t, output, "discarded-first")
	assert.Contains(t, output, "final-answer")
	assert.Contains(t, output, "proxy_rounds")
	assert.Contains(t, output, "proxy_billed_usage")
	assert.Contains(t, output, `"n":1`)
	assert.Contains(t, output, "data: [DONE]")
}

func TestCodexDegradationMitigationContinuesWhenAnyReasoningHasEncryptedContent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &models.Group{
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	baseBody := []byte(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	first := codexMitigationTestResponse(codexMitigationTestSSEWithTwoReasoningItems("resp_1", "discarded-first", 516))
	second := codexMitigationTestResponse(codexMitigationTestSSE("resp_2", "final-answer", 20, "enc_2"))

	continuations := 0
	roundTrip := func(body []byte) (*http.Response, error) {
		continuations++
		return second, nil
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex/v1/responses", strings.NewReader(string(baseBody)))

	ps := &ProxyServer{}
	ps.handleCodexDegradationMitigationStreamingResponse(c, first, baseBody, group, group, roundTrip)

	require.Equal(t, 1, continuations)
	output := w.Body.String()
	assert.NotContains(t, output, "discarded-first")
	assert.Contains(t, output, "final-answer")
}

func TestCodexDegradationMitigationBuffersFunctionCallAcrossContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &models.Group{
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	baseBody := []byte(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"call a tool"}]}`)
	first := codexMitigationTestResponse(codexMitigationTestSSEFunctionCall("resp_1", "discarded_call_args", 516, "enc_1"))
	second := codexMitigationTestResponse(codexMitigationTestSSEFunctionCall("resp_2", "final_call_args", 20, "enc_2"))

	continuations := 0
	roundTrip := func(body []byte) (*http.Response, error) {
		continuations++
		return second, nil
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex/v1/responses", strings.NewReader(string(baseBody)))

	ps := &ProxyServer{}
	ps.handleCodexDegradationMitigationStreamingResponse(c, first, baseBody, group, group, roundTrip)

	require.Equal(t, 1, continuations)
	output := w.Body.String()
	assert.NotContains(t, output, "discarded_call_args")
	assert.Contains(t, output, "final_call_args")
	assert.Contains(t, output, "response.function_call_arguments.delta")
	assert.Contains(t, output, "data: [DONE]")
}

func TestCodexDegradationMitigationStopsBeforeContinuationOnDownstreamWriteError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &models.Group{
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      datatypes.JSONMap{"codex_degradation_mitigation_enabled": true},
	}
	baseBody := []byte(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	first := codexMitigationTestResponse(codexMitigationTestSSE("resp_1", "discarded-first", 516, "enc_1"))

	continuations := 0
	roundTrip := func(body []byte) (*http.Response, error) {
		continuations++
		return codexMitigationTestResponse(codexMitigationTestSSE("resp_2", "final-answer", 20, "enc_2")), nil
	}

	c, _ := gin.CreateTestContext(&codexMitigationFailingHTTPWriter{header: http.Header{}})
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex/v1/responses", strings.NewReader(string(baseBody)))

	ps := &ProxyServer{}
	ps.handleCodexDegradationMitigationStreamingResponse(c, first, baseBody, group, group, roundTrip)

	assert.Equal(t, 0, continuations)
}

type codexMitigationFailingHTTPWriter struct {
	header http.Header
}

func (w *codexMitigationFailingHTTPWriter) Header() http.Header {
	return w.header
}

func (w *codexMitigationFailingHTTPWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream closed")
}

func (w *codexMitigationFailingHTTPWriter) WriteHeader(int) {}

func (w *codexMitigationFailingHTTPWriter) Flush() {}

func codexMitigationTestResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func codexMitigationTestSSE(responseID, text string, reasoningTokens int, encryptedContent string) string {
	return `event: response.created
data: {"type":"response.created","response":{"id":"` + responseID + `","model":"gpt-5","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_` + responseID + `","status":"in_progress"}}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_` + responseID + `","status":"completed","encrypted_content":"` + encryptedContent + `","summary":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":1,"delta":"` + text + `"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `"}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"` + responseID + `","model":"gpt-5","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":520,"total_tokens":530,"output_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoningTokens) + `}}}}

data: [DONE]
`
}

func codexMitigationTestSSEFunctionCall(responseID, args string, reasoningTokens int, encryptedContent string) string {
	return `event: response.created
data: {"type":"response.created","response":{"id":"` + responseID + `","model":"gpt-5","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_` + responseID + `","status":"in_progress"}}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_` + responseID + `","status":"completed","encrypted_content":"` + encryptedContent + `","summary":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_` + responseID + `","call_id":"call_` + responseID + `","name":"lookup","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_` + responseID + `","delta":"` + args + `"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_` + responseID + `","arguments":"` + args + `"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_` + responseID + `","call_id":"call_` + responseID + `","name":"lookup","arguments":"` + args + `"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"` + responseID + `","model":"gpt-5","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":520,"total_tokens":530,"output_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoningTokens) + `}}}}

data: [DONE]
`
}

func codexMitigationTestSSEWithTwoReasoningItems(responseID, text string, reasoningTokens int) string {
	return `event: response.created
data: {"type":"response.created","response":{"id":"` + responseID + `","model":"gpt-5","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_` + responseID + `_a","status":"in_progress"}}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_` + responseID + `_a","status":"completed","encrypted_content":"enc_1","summary":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"reasoning","id":"rs_` + responseID + `_b","status":"in_progress"}}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","id":"rs_` + responseID + `_b","status":"completed","summary":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":2,"item":{"type":"message","role":"assistant","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":2,"delta":"` + text + `"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":2,"item":{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `"}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"` + responseID + `","model":"gpt-5","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":520,"total_tokens":530,"output_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoningTokens) + `}}}}

data: [DONE]
`
}

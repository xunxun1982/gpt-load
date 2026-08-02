package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFunctionCallSecurityAnthropicDisconnectDoesNotExecute(t *testing.T) {
	ps, c, w, trigger := newFunctionCallSecurityContext(t, "anthropic", "/v1/messages", "auto", "A")
	text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","model":"test","usage":{"input_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		fmt.Sprintf(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":%q}}`, text),
		``,
	}, "\n")
	ps.handleFunctionCallAnthropicStreamingBody(c, strings.NewReader(stream), w, trigger)
	output := w.Body.String()
	if strings.Contains(output, `"type":"tool_use"`) || !strings.Contains(output, "<invoke") {
		t.Fatalf("disconnected stream executed or removed call: %s", output)
	}
}

func TestFunctionCallSecurityCodexCollectedDisconnectDoesNotExecute(t *testing.T) {
	ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai-response", "/v1/responses", "auto", "A")
	c.Set(ctxKeyFunctionCallEnabled, true)
	text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
	stream := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"test"}}`,
		``,
		`event: response.output_text.delta`,
		fmt.Sprintf(`data: {"type":"response.output_text.delta","delta":%q}`, text),
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	ps.handleCodexForcedStreamResponse(c, resp)
	output := w.Body.String()
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("invalid collected response: %v: %s", err, output)
	}
	if strings.Contains(output, `"type":"function_call"`) || !strings.Contains(extractResponsesOutputText(payload), "<invoke") {
		t.Fatalf("disconnected collected stream executed or removed call: %s", output)
	}
}

func TestFunctionCallSecurityChatUsageTailFollowsConvertedCall(t *testing.T) {
	ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai", "/v1/chat/completions", "auto", "A")
	callText := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
	stream := strings.Join([]string{
		fmt.Sprintf(`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, callText),
		``,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"id":"chat_1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	ps.handleFunctionCallStreamingResponse(c, resp)
	output := w.Body.String()
	toolAt := strings.Index(output, `"tool_calls"`)
	usageAt := strings.Index(output, `"total_tokens":8`)
	if toolAt < 0 || usageAt < 0 || usageAt < toolAt {
		t.Fatalf("expected converted call followed by usage tail: %s", output)
	}
}

package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFunctionCallSecurityPreservesResponsesNonTextItemsAndUsage(t *testing.T) {
	ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai-response", "/v1/responses", "auto", "A")
	text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
	body, err := json.Marshal(map[string]any{
		"id": "resp_1", "status": "completed", "output_text": text,
		"output": []any{
			map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}},
			map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": text},
				map[string]any{"type": "refusal", "refusal": "preserve-me"},
			}},
		},
		"usage": map[string]any{"input_tokens": 3, "output_tokens": 5, "total_tokens": 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	output := runFunctionCallSecurityResponse(t, ps, c, w, "openai-response", string(body))
	for _, required := range []string{`"type":"reasoning"`, `"type":"refusal"`, `"refusal":"preserve-me"`, `"total_tokens":8`, `"type":"function_call"`} {
		if !strings.Contains(output, required) {
			t.Fatalf("Responses field %s was lost: %s", required, output)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	if rootText, _ := payload["output_text"].(string); strings.Contains(rootText, "<invoke") || strings.Contains(rootText, trigger) {
		t.Fatalf("Responses output_text projection was not cleaned: %q", rootText)
	}
}

func TestFunctionCallSecurityPreservesAnthropicNonTextContent(t *testing.T) {
	ps, c, w, trigger := newFunctionCallSecurityContext(t, "anthropic", "/v1/messages", "auto", "A")
	text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
	body := fmt.Sprintf(`{"type":"message","content":[{"type":"thinking","thinking":"preserve-me","signature":"sig"},{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":5}}`, text)
	output := runFunctionCallSecurityResponse(t, ps, c, w, "anthropic", body)
	for _, required := range []string{`"type":"thinking"`, `"thinking":"preserve-me"`, `"type":"tool_use"`, `"output_tokens":5`} {
		if !strings.Contains(output, required) {
			t.Fatalf("Anthropic field %s was lost: %s", required, output)
		}
	}
}

func TestFunctionCallSecurityValidatesRequiredAndBaseType(t *testing.T) {
	tests := []struct {
		name  string
		param string
	}{
		{name: "required missing", param: `<parameter name="q">test</parameter>`},
		{name: "integer type mismatch", param: `<parameter name="n">not-a-number</parameter>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai", "/v1/chat/completions", "auto", "A")
			session := functionCallSessionFromContext(c, trigger)
			def := session.tools["A"]
			def.Parameters["required"] = []any{"n"}
			session.tools["A"] = def
			content := trigger + `<invoke name="A">` + tt.param + `</invoke>`
			body := fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)
			output := runFunctionCallSecurityResponse(t, ps, c, w, "openai", body)
			if strings.Contains(output, `"tool_calls"`) || !strings.Contains(output, "<invoke") {
				t.Fatalf("invalid schema call executed or removed: %s", output)
			}
		})
	}
}

func TestFunctionCallSecurityRejectsIncompleteTerminalStates(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		path    string
		body    func(string) string
		marker  string
	}{
		{
			name: "chat length", channel: "openai", path: "/v1/chat/completions", marker: `"tool_calls"`,
			body: func(trigger string) string {
				content := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
				return fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"length"}]}`, content)
			},
		},
		{
			name: "responses incomplete", channel: "openai-response", path: "/v1/responses", marker: `"type":"function_call"`,
			body: func(trigger string) string {
				text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
				return fmt.Sprintf(`{"id":"resp_1","status":"incomplete","output_text":%q}`, text)
			},
		},
		{
			name: "responses failed", channel: "openai-response", path: "/v1/responses", marker: `"type":"function_call"`,
			body: func(trigger string) string {
				text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
				return fmt.Sprintf(`{"id":"resp_1","status":"failed","output_text":%q,"error":{"code":"server_error","message":"failed"}}`, text)
			},
		},
		{
			name: "anthropic max tokens", channel: "anthropic", path: "/v1/messages", marker: `"type":"tool_use"`,
			body: func(trigger string) string {
				text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
				return fmt.Sprintf(`{"type":"message","content":[{"type":"text","text":%q}],"stop_reason":"max_tokens"}`, text)
			},
		},
		{
			name: "anthropic error", channel: "anthropic", path: "/v1/messages", marker: `"type":"tool_use"`,
			body: func(trigger string) string {
				text := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
				return fmt.Sprintf(`{"type":"error","content":[{"type":"text","text":%q}],"error":{"type":"api_error","message":"failed"}}`, text)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, c, w, trigger := newFunctionCallSecurityContext(t, tt.channel, tt.path, "auto", "A")
			output := runFunctionCallSecurityResponse(t, ps, c, w, tt.channel, tt.body(trigger))
			if strings.Contains(output, tt.marker) || !strings.Contains(output, "<invoke") {
				t.Fatalf("incomplete response executed or removed call: %s", output)
			}
		})
	}
}

func TestFunctionCallSecurityPreservesOrdinaryHTMLAndJSON(t *testing.T) {
	ps, c, w, _ := newFunctionCallSecurityContext(t, "openai", "/v1/chat/completions", "auto", "A")
	content := `Example: <invoke name="A">HTML only</invoke> {"name":"A","arguments":{"n":1}}`
	body := fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)
	output := runFunctionCallSecurityResponse(t, ps, c, w, "openai", body)
	if strings.Contains(output, `"tool_calls"`) || !strings.Contains(output, `HTML only`) || !strings.Contains(output, `arguments`) {
		t.Fatalf("ordinary content changed: %s", output)
	}
}

func TestAnthropicStreamKeepsKnownStopReasonWhenLaterDeltaIsEmpty(t *testing.T) {
	_, c, _, _ := newFunctionCallSecurityContext(t, "anthropic", "/v1/messages", "auto", "A")
	events := []functionCallSSEEvent{
		{Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
		{Data: `{"type":"message_delta","delta":{"stop_reason":""}}`},
		{Data: `{"type":"message_stop"}`},
	}

	_, _, _, _, _, complete := collectAnthropicFunctionCallStreamText(c, events)
	if !complete {
		t.Fatal("empty stop_reason overwrote the known terminal reason")
	}
}

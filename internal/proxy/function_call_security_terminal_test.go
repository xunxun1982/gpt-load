package proxy

import (
	"fmt"
	"strings"
	"testing"
)

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

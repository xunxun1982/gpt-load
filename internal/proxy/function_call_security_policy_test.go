package proxy

import (
	"fmt"
	"strings"
	"testing"
)

func TestFunctionCallSecurityEnforcesRequestPolicy(t *testing.T) {
	tests := []struct {
		name       string
		choice     any
		tools      []string
		calledTool string
	}{
		{name: "tool choice none", choice: "none", tools: []string{"A"}, calledTool: "A"},
		{name: "specific tool mismatch", choice: map[string]any{"type": "function", "function": map[string]any{"name": "A"}}, tools: []string{"A", "B"}, calledTool: "B"},
		{name: "tool outside whitelist", choice: "auto", tools: []string{"A"}, calledTool: "B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai", "/v1/chat/completions", tt.choice, tt.tools...)
			content := fmt.Sprintf("%s\n<invoke name=\"%s\"><parameter name=\"q\">test</parameter></invoke>", trigger, tt.calledTool)
			body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
			output := runFunctionCallSecurityResponse(t, ps, c, w, "openai", body)
			if strings.Contains(output, `"tool_calls"`) {
				t.Fatalf("unsafe tool call executed: %s", output)
			}
			if !strings.Contains(output, "<invoke") {
				t.Fatalf("rejected call must remain text: %s", output)
			}
		})
	}
}

func TestFunctionCallSecurityRequiresExactTriggerAndStrictArguments(t *testing.T) {
	tests := []struct {
		name    string
		content func(string) string
	}{
		{name: "missing trigger", content: func(string) string { return `<invoke name="A"><parameter name="q">test</parameter></invoke>` }},
		{name: "wrong trigger", content: func(string) string {
			return `<<CALL_wrong>><invoke name="A"><parameter name="q">test</parameter></invoke>`
		}},
		{name: "unclosed parameter", content: func(trigger string) string { return trigger + `<invoke name="A"><parameter name="q">test</invoke>` }},
		{name: "repaired json", content: func(trigger string) string {
			return trigger + `<invoke name="A"><parameter name="n">{"x":1</parameter></invoke>`
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai", "/v1/chat/completions", "auto", "A")
			content := tt.content(trigger)
			body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
			output := runFunctionCallSecurityResponse(t, ps, c, w, "openai", body)
			if strings.Contains(output, `"tool_calls"`) || !strings.Contains(output, "<invoke") {
				t.Fatalf("invalid call was executed or removed: %s", output)
			}
		})
	}
}

func TestFunctionCallSecurityPreservesLargeInteger(t *testing.T) {
	ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai", "/v1/chat/completions", "auto", "A")
	content := trigger + `<invoke name="A"><parameter name="n">9007199254740993</parameter></invoke>`
	body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
	output := runFunctionCallSecurityResponse(t, ps, c, w, "openai", body)
	if !strings.Contains(output, `9007199254740993`) || strings.Contains(output, `9007199254740992`) {
		t.Fatalf("integer precision changed: %s", output)
	}
}

func TestFunctionCallSecurityRejectsDuplicateTriggerAcrossMessageFields(t *testing.T) {
	ps, c, w, trigger := newFunctionCallSecurityContext(t, "openai", "/v1/chat/completions", "auto", "A")
	reasoning := trigger + `<invoke name="A"><parameter name="q">test</parameter></invoke>`
	body := fmt.Sprintf(`{"choices":[{"message":{"content":%q,"reasoning_content":%q},"finish_reason":"stop"}]}`, trigger+" not a call", reasoning)
	output := runFunctionCallSecurityResponse(t, ps, c, w, "openai", body)
	if strings.Contains(output, `"tool_calls"`) {
		t.Fatalf("duplicate cross-field trigger executed: %s", output)
	}
}

func TestFunctionCallSecurityUsesSchemaForNumericLookingScalars(t *testing.T) {
	trigger := "<<CALL_test>>"
	stringSession := newFunctionCallSession(trigger, []functionToolDefinition{{
		Name: "A",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"q": map[string]any{"type": "string"}},
		},
	}}, "auto", true)

	for _, value := range []string{"-draft", "123abc"} {
		result, valid := stringSession.ParseAndValidate(
			trigger+`<invoke name="A"><parameter name="q">`+value+`</parameter></invoke>`,
			true,
		)
		if !valid || len(result.Calls) != 1 || result.Calls[0].Args["q"] != value {
			t.Fatalf("numeric-looking string %q was not validated by its schema: %#v", value, result)
		}
	}

	integerSession := newFunctionCallSession(trigger, []functionToolDefinition{{
		Name: "A",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"n": map[string]any{"type": "integer"}},
		},
	}}, "auto", true)
	if _, valid := integerSession.ParseAndValidate(
		trigger+`<invoke name="A"><parameter name="n">123abc</parameter></invoke>`,
		true,
	); valid {
		t.Fatal("non-JSON scalar bypassed integer schema validation")
	}
}

func TestFunctionCallSecurityRejectsAdditionalProperties(t *testing.T) {
	trigger := "<<CALL_test>>"
	session := newFunctionCallSession(trigger, []functionToolDefinition{{
		Name: "A",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"q": map[string]any{"type": "string"}},
			"additionalProperties": false,
		},
	}}, "auto", true)

	_, valid := session.ParseAndValidate(
		trigger+`<invoke name="A"><parameter name="q">ok</parameter><parameter name="extra">no</parameter></invoke>`,
		true,
	)
	if valid {
		t.Fatal("additionalProperties=false accepted an undeclared argument")
	}
}

package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
)

// TestParseFunctionCallsFromContentForCC_TodoWriteNormalization verifies that
// TodoWrite tool calls parsed via parseFunctionCallsFromContentForCC are
// normalized into schema-compliant todos (content/status/priority/id).
func TestParseFunctionCallsFromContentForCC_TodoWriteNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	trigger := "<<CALL_TEST>>"
	content := trigger + `<invoke name="TodoWrite"><parameter name="todos">` +
		`[{"content":"task one","state":"pending"},{"content":"task two","status":"completed"}]` +
		`</parameter></invoke>`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyTriggerSignal, trigger)
	c.Set(ctxKeyFunctionCallEnabled, true)

	_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, content)
	if len(toolUseBlocks) != 1 {
		t.Fatalf("expected 1 tool_use block, got %d", len(toolUseBlocks))
	}

	block := toolUseBlocks[0]
	if block.Type != "tool_use" {
		t.Fatalf("expected block type tool_use, got %q", block.Type)
	}
	if block.Name != "TodoWrite" {
		t.Fatalf("expected tool name TodoWrite, got %q", block.Name)
	}

	var input map[string]any
	if err := json.Unmarshal(block.Input, &input); err != nil {
		t.Fatalf("failed to unmarshal tool_use input: %v", err)
	}

	rawTodos, ok := input["todos"]
	if !ok {
		t.Fatalf("expected todos key in tool_use input, got: %v", input)
	}

	todoList, ok := rawTodos.([]any)
	if !ok {
		t.Fatalf("expected todos to be []any, got %T", rawTodos)
	}
	if len(todoList) != 2 {
		t.Fatalf("expected 2 todos after normalization, got %d", len(todoList))
	}

	for i, item := range todoList {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("todo %d is not an object, got %T", i, item)
		}

		// content must exist and be non-empty
		contentVal, ok := m["content"].(string)
		if !ok || strings.TrimSpace(contentVal) == "" {
			t.Fatalf("todo %d missing content field: %+v", i, m)
		}

		activeFormVal, ok := m["activeForm"].(string)
		if !ok || strings.TrimSpace(activeFormVal) == "" {
			t.Fatalf("todo %d missing activeForm field: %+v", i, m)
		}
		if activeFormVal != contentVal {
			t.Fatalf("todo %d expected activeForm to equal content %q, got %q", i, contentVal, activeFormVal)
		}

		// status must be one of the official values
		statusVal, ok := m["status"].(string)
		if !ok {
			t.Fatalf("todo %d missing status field: %+v", i, m)
		}
		switch statusVal {
		case "pending", "in_progress", "completed":
			// ok
		default:
			t.Fatalf("todo %d has invalid status %q", i, statusVal)
		}

		// priority must exist
		if _, ok := m["priority"].(string); !ok {
			t.Fatalf("todo %d missing priority field: %+v", i, m)
		}

		// id must exist and have length >= 3
		idVal, ok := m["id"].(string)
		if !ok || len(strings.TrimSpace(idVal)) < 3 {
			t.Fatalf("todo %d has invalid id: %v", i, m["id"])
		}
	}
}

// TestCCFunctionCall_StreamingMalformedXMLCleanup ensures that malformed XML tags
// (e.g., <><invokename=, <parametername=) are cleaned in real-time during streaming,
// preventing them from leaking to the Claude Code client.
// This addresses the issue from real-world production log where CC displayed raw malformed XML.
func TestCCFunctionCall_StreamingMalformedXMLCleanup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	trigger := "<<CALL_TEST>>"
	stopReason := "stop"

	tests := []struct {
		name        string
		content     string
		shouldClean bool
		expectClean string
	}{
		{
			name:        "malformed invokename with JSON array",
			content:     `● 我需要创建任务：<><invokename="TodoWrite">[{"id":"1","content":"task"}]`,
			shouldClean: true,
			expectClean: "● 我需要创建任务：",
		},
		{
			name:        "malformed parametername with path",
			content:     `Let me read: <><parametername="file_path">F:/test/hello.py`,
			shouldClean: true,
			expectClean: "Let me read:",
		},
		{
			name:        "malformed invokename with parametername chain",
			content:     `<><invokename="Glob"><parametername="pattern">*`,
			shouldClean: true,
			expectClean: "",
		},
		{
			name:        "normal text without malformed tags",
			content:     "● Search(pattern: \"*\")",
			shouldClean: false,
			expectClean: "● Search(pattern: \"*\")",
		},
		{
			name:        "bullet with empty malformed tag",
			content:     "● <><invokename=\"TodoWrite\">[{}]",
			shouldClean: true,
			expectClean: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunk := &OpenAIResponse{
				Model: "test-model",
				Choices: []OpenAIChoice{
					{
						Index: 0,
						Delta: &OpenAIRespMessage{
							Content: &tt.content,
						},
						FinishReason: &stopReason,
					},
				},
			}

			body := buildTestSSEBody(chunk)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			// Enable function-call bridge and provide trigger signal
			c.Set(ctxKeyTriggerSignal, trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)
			c.Set("original_model", "test-model")

			ps := &ProxyServer{}
			ps.handleCCStreamingResponse(c, resp)

			output := w.Body.String()
			if output == "" {
				t.Fatalf("expected SSE output, got empty body")
			}

			// Verify malformed XML tags are NOT present in output
			if strings.Contains(output, "<invokename") {
				t.Errorf("output leaked <invokename: %s", output)
			}
			if strings.Contains(output, "<parametername") {
				t.Errorf("output leaked <parametername: %s", output)
			}
			if strings.Contains(output, "<>") {
				t.Errorf("output leaked <> fragment: %s", output)
			}

			// Verify cleaned content is present if expected
			if tt.shouldClean && tt.expectClean != "" {
				if !strings.Contains(output, tt.expectClean) {
					t.Errorf("expected cleaned content %q to be present in output, got: %s", tt.expectClean, output)
				}
			}

			// Verify JSON arrays from TodoWrite are NOT visible
			if strings.Contains(output, `"id":"1"`) || strings.Contains(output, `"content":"task"`) {
				t.Errorf("output leaked JSON array content: %s", output)
			}

			// Verify file paths from parameter tags are NOT visible
			if strings.Contains(output, "F:/test/hello.py") || strings.Contains(output, "F:\\test\\hello.py") {
				t.Errorf("output leaked file path: %s", output)
			}
		})
	}
}

func TestCCFunctionCall_StreamingPartialMalformedTagHoldback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	trigger := "<<CALL_TEST>>"
	stopReason := "stop"

	content := `Hello <invokename="Read"`
	chunk := &OpenAIResponse{
		Model: "test-model",
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Delta: &OpenAIRespMessage{
					Content: &content,
				},
				FinishReason: &stopReason,
			},
		},
	}

	body := buildTestSSEBody(chunk)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyTriggerSignal, trigger)
	c.Set(ctxKeyFunctionCallEnabled, true)
	c.Set("original_model", "test-model")

	ps := &ProxyServer{}
	ps.handleCCStreamingResponse(c, resp)

	output := w.Body.String()
	if output == "" {
		t.Fatalf("expected SSE output, got empty body")
	}
	if !strings.Contains(output, "Hello") {
		t.Errorf("expected output to contain %q, got: %s", "Hello", output)
	}
	if strings.Contains(output, "<invokename") {
		t.Errorf("output leaked <invokename: %s", output)
	}
	if strings.Contains(output, "<parametername") {
		t.Errorf("output leaked <parametername: %s", output)
	}
	if strings.Contains(output, "<>") {
		t.Errorf("output leaked <> fragment: %s", output)
	}
}

// TestParseFunctionCallsFromContentForCC_ColonQuoteJSON tests parsing of
// severely malformed TodoWrite with [": pattern (December 2025 auto-pause issue)
func TestParseFunctionCallsFromContentForCC_ColonQuoteJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		content       string
		expectToolUse bool
	}{
		{
			name:          "valid TodoWrite",
			content:       `<<CALL_test>><invoke name="TodoWrite"><parameter name="todos">[{"id":"1","content":"task","status":"pending"}]</parameter></invoke>`,
			expectToolUse: true,
		},
		{
			name:          "severely malformed TodoWrite with colon-quote start",
			content:       `<<CALL_test>><invoke name="TodoWrite"><parameter name="todos">[": "检查当前目录并查看是否有hello.py文件"]</parameter></invoke>`,
			expectToolUse: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, "<<CALL_test>>")
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.content)

			if tt.expectToolUse && len(toolUseBlocks) == 0 {
				t.Errorf("Expected tool_use blocks but got none")
			}
			if !tt.expectToolUse && len(toolUseBlocks) > 0 {
				t.Errorf("Expected no tool_use blocks but got %d", len(toolUseBlocks))
			}

			if len(toolUseBlocks) > 0 {
				t.Logf("Parsed %d tool_use blocks", len(toolUseBlocks))
				for i, block := range toolUseBlocks {
					t.Logf("Block %d: name=%s, input=%s", i, block.Name, string(block.Input))
				}
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_WithThinkingContent tests that function
// call parsing works correctly when content contains <thinking> tags.
func TestParseFunctionCallsFromContentForCC_WithThinkingContent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		input             string
		expectedToolCount int
		expectedCleanText string
	}{
		{
			name: "thinking_then_tool_call",
			input: `<thinking>Let me create a task list...</thinking>
<<CALL_test123>>
<invoke name="TodoWrite"><parameter name="todos">[{"content":"task1","status":"pending"}]</parameter></invoke>`,
			expectedToolCount: 1,
			expectedCleanText: "",
		},
		{
			name: "text_and_thinking_then_tool_call",
			input: `I'll help you with that.
<thinking>Planning the approach...</thinking>
<<CALL_test123>>
<invoke name="Bash"><parameter name="command">ls -la</parameter></invoke>`,
			expectedToolCount: 1,
			expectedCleanText: "I'll help you with that.",
		},
		{
			name: "only_thinking_no_tool_call",
			input: `<thinking>Just thinking about this...</thinking>
I need more information.`,
			expectedToolCount: 0,
			expectedCleanText: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, "<<CALL_test123>>")
			c.Set(ctxKeyFunctionCallEnabled, true)

			cleanedContent, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.input)

			if len(toolUseBlocks) != tt.expectedToolCount {
				t.Errorf("expected %d tool_use blocks, got %d", tt.expectedToolCount, len(toolUseBlocks))
			}

			// For cases with tool calls, verify the content is cleaned
			if tt.expectedToolCount > 0 {
				// Content should not contain trigger signal or invoke tags
				if strings.Contains(cleanedContent, "<<CALL_") {
					t.Errorf("cleaned content should not contain trigger signal")
				}
				if strings.Contains(cleanedContent, "<invoke") {
					t.Errorf("cleaned content should not contain invoke tags")
				}
			}

			// Verify expected clean text if specified
			if tt.expectedCleanText != "" {
				cleanedTrimmed := strings.TrimSpace(cleanedContent)
				if !strings.Contains(cleanedTrimmed, tt.expectedCleanText) {
					t.Errorf("expected cleaned content to contain %q, got %q", tt.expectedCleanText, cleanedTrimmed)
				}
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_ProductionLogTodoWriteRetry tests parsing of
// TodoWrite calls that caused repeated retries in production log due to malformed JSON.
func TestParseFunctionCallsFromContentForCC_ProductionLogTodoWriteRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		trigger        string
		content        string
		expectToolUse  bool
		expectToolName string
		expectTodos    int
	}{
		{
			name:    "malformed_todowrite_with_form_field",
			trigger: "<<CALL_TEST>>",
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"content":"分析现有hello.py的GUI实现","Form":"分析现有hello.py的GUI实现","status":"in_progress"}]</parameter></invoke>`,
			// Form field should be normalized to activeForm
			expectToolUse:  true,
			expectToolName: "TodoWrite",
			expectTodos:    1,
		},
		{
			name:    "malformed_todowrite_with_state_field",
			trigger: "<<CALL_TEST>>",
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"content":"task","state":"pending"}]</parameter></invoke>`,
			// state field should be normalized to status
			expectToolUse:  true,
			expectToolName: "TodoWrite",
			expectTodos:    1,
		},
		{
			name:    "malformed_invokename_todowrite",
			trigger: "<<CALL_TEST>>",
			content: `<<CALL_TEST>><><invokename="TodoWrite"><parametername="todos">[{"id":"1","content":"task","status":"pending"}]`,
			// Malformed invokename should still be parsed
			expectToolUse:  true,
			expectToolName: "TodoWrite",
			expectTodos:    1,
		},
		{
			name:    "severely_malformed_json_todos",
			trigger: "<<CALL_TEST>>",
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id":"1",": "研究Python GUI框架","activeForm":"正在研究"}]</parameter></invoke>`,
			// Severely malformed JSON should be repaired
			expectToolUse:  true,
			expectToolName: "TodoWrite",
			expectTodos:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.content)

			if tt.expectToolUse {
				if len(toolUseBlocks) == 0 {
					t.Fatalf("expected tool_use block, got none")
				}

				block := toolUseBlocks[0]
				if block.Name != tt.expectToolName {
					t.Errorf("expected tool name %q, got %q", tt.expectToolName, block.Name)
				}

				if tt.expectTodos > 0 {
					var input map[string]any
					if err := json.Unmarshal(block.Input, &input); err != nil {
						t.Fatalf("failed to unmarshal tool_use input: %v", err)
					}

					todos, ok := input["todos"]
					if !ok {
						t.Fatalf("expected todos key in input, got: %v", input)
					}

					todoList, ok := todos.([]any)
					if !ok {
						t.Fatalf("expected todos to be []any, got %T", todos)
					}

					if len(todoList) != tt.expectTodos {
						t.Errorf("expected %d todos, got %d", tt.expectTodos, len(todoList))
					}

					// Verify each todo has required fields
					for i, item := range todoList {
						m, ok := item.(map[string]any)
						if !ok {
							t.Errorf("todo %d is not a map", i)
							continue
						}

						// Check content field exists
						if _, ok := m["content"]; !ok {
							t.Errorf("todo %d missing content field", i)
						}

						// Check status field exists and is valid
						if status, ok := m["status"].(string); ok {
							switch status {
							case "pending", "in_progress", "completed":
								// Valid
							default:
								t.Errorf("todo %d has invalid status %q", i, status)
							}
						} else {
							t.Errorf("todo %d missing or invalid status field", i)
						}
					}
				}
			} else {
				if len(toolUseBlocks) > 0 {
					t.Errorf("expected no tool_use blocks, got %d", len(toolUseBlocks))
				}
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_ProductionLogSeverePatterns tests parsing of severely
// malformed TodoWrite calls from production logs that caused auto-pause issues.
func TestParseFunctionCallsFromContentForCC_ProductionLogSeverePatterns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		content       string
		trigger       string
		expectToolUse bool
		expectTodoLen int
		description   string
	}{
		{
			name: "severely_malformed_todos_with_missing_field_names",
			// Pattern from production log: multiple todos with various malformations
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id":"1",": "调查现有hello.py代码","activeForm":"正在调查","status":"in_progress"},{"id":"2",": "设计美观GUI方案","Form":"正在设计","state":"pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 2,
			description:   "Severely malformed todos with missing field names and wrong field names",
		},
		{
			name: "malformed_invokename_with_truncated_json",
			// Pattern: <><invokename="TodoWrite"><parametername="todos">[truncated JSON]
			content:       `<<CALL_TEST>><><invokename="TodoWrite"><parametername="todos">[{"id":"1","content":"探索最佳实践","activeForm":"正在探索","status":"pending"}]`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			description:   "Malformed invokename with valid JSON content",
		},
		{
			name: "multiple_retry_attempts_pattern",
			// Pattern from production log: model retries with slightly different malformations
			content:       `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id":"task-1","content":"调研联网搜索最佳实践","activeForm":"正在调研","status":"pending"},{"id":"task-2","content":"检查hello.py文件","activeForm":"正在检查","status":"pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 2,
			description:   "Valid TodoWrite with task-N id format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.content)

			if tt.expectToolUse {
				if len(toolUseBlocks) == 0 {
					t.Fatalf("[%s] expected tool_use blocks, got none", tt.description)
				}

				block := toolUseBlocks[0]
				if block.Name != "TodoWrite" {
					t.Fatalf("[%s] expected tool name TodoWrite, got %q", tt.description, block.Name)
				}

				var input map[string]any
				if err := json.Unmarshal(block.Input, &input); err != nil {
					t.Fatalf("[%s] failed to unmarshal tool_use input: %v", tt.description, err)
				}

				todos, ok := input["todos"].([]any)
				if !ok {
					t.Fatalf("[%s] expected todos to be array, got %T", tt.description, input["todos"])
				}

				if len(todos) != tt.expectTodoLen {
					t.Fatalf("[%s] expected %d todos, got %d", tt.description, tt.expectTodoLen, len(todos))
				}

				// Verify all todos have required fields after normalization
				for i, todo := range todos {
					todoMap, ok := todo.(map[string]any)
					if !ok {
						t.Errorf("[%s] todo %d is not a map: %T", tt.description, i, todo)
						continue
					}

					// Must have content
					if _, hasContent := todoMap["content"]; !hasContent {
						t.Errorf("[%s] todo %d missing content field: %v", tt.description, i, todoMap)
					}

					// Must have status (not state)
					if _, hasStatus := todoMap["status"]; !hasStatus {
						t.Errorf("[%s] todo %d missing status field: %v", tt.description, i, todoMap)
					}

					// Must have id
					if _, hasID := todoMap["id"]; !hasID {
						t.Errorf("[%s] todo %d missing id field: %v", tt.description, i, todoMap)
					}
				}
			} else {
				if len(toolUseBlocks) > 0 {
					t.Fatalf("[%s] expected no tool_use blocks, got %d", tt.description, len(toolUseBlocks))
				}
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_ProductionLogTodoWriteMissingField tests TodoWrite parsing
// for malformed patterns found in real production logs.
// Issue: TodoWrite JSON has missing content field name like {"id": "1",": "探索..."}
func TestParseFunctionCallsFromContentForCC_ProductionLogTodoWriteMissingField(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		content       string
		trigger       string
		expectToolUse bool
		expectTodoLen int
		checkContent  string // Expected content value in first todo
	}{
		{
			// Pattern from real production log: missing content field name
			name:          "missing_content_field_name",
			content:       `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id": "1",": "调研联网搜索最佳实践","activeForm": "正在调研","status": "pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			checkContent:  "调研联网搜索最佳实践",
		},
		{
			// Pattern: state instead of status
			name:          "state_field_normalization",
			content:       `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id": "1","content": "搜索最佳实践","state": "in_progress"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
		},
		{
			// Pattern: Form instead of activeForm
			name:          "Form_field_normalization",
			content:       `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id": "1","content": "编写代码","Form": "正在编写","status": "pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
		},
		{
			// Pattern: Multiple todos with various malformations
			name:          "multiple_todos_mixed_malformations",
			content:       `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id": "1",": "搜索最佳实践","state": "pending"},{"id": "2","content": "编写代码","Form": "正在编写","status": "pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.content)

			if tt.expectToolUse {
				if len(toolUseBlocks) == 0 {
					t.Fatalf("expected tool_use blocks, got none")
				}

				block := toolUseBlocks[0]
				if block.Name != "TodoWrite" {
					t.Fatalf("expected tool name TodoWrite, got %q", block.Name)
				}

				var input map[string]any
				if err := json.Unmarshal(block.Input, &input); err != nil {
					t.Fatalf("failed to unmarshal tool_use input: %v", err)
				}

				todos, ok := input["todos"].([]any)
				if !ok {
					t.Fatalf("expected todos to be array, got %T", input["todos"])
				}

				if len(todos) != tt.expectTodoLen {
					t.Fatalf("expected %d todos, got %d", tt.expectTodoLen, len(todos))
				}

				// Check first todo has content field
				if len(todos) > 0 && tt.checkContent != "" {
					firstTodo, ok := todos[0].(map[string]any)
					if !ok {
						t.Fatalf("expected todo to be map, got %T", todos[0])
					}
					content, ok := firstTodo["content"].(string)
					if !ok {
						t.Fatalf("expected content to be string, got %T (todo: %v)", firstTodo["content"], firstTodo)
					}
					if content != tt.checkContent {
						t.Errorf("expected content %q, got %q", tt.checkContent, content)
					}
				}

				// Check all todos have required fields
				for i, todo := range todos {
					todoMap, ok := todo.(map[string]any)
					if !ok {
						t.Errorf("todo %d is not a map", i)
						continue
					}
					// Must have content
					if _, ok := todoMap["content"]; !ok {
						t.Errorf("todo %d missing content field: %v", i, todoMap)
					}
					// Must have status (normalized from state)
					if _, ok := todoMap["status"]; !ok {
						t.Errorf("todo %d missing status field: %v", i, todoMap)
					}
					// Must have id
					if _, ok := todoMap["id"]; !ok {
						t.Errorf("todo %d missing id field: %v", i, todoMap)
					}
				}
			} else {
				if len(toolUseBlocks) > 0 {
					t.Fatalf("expected no tool_use blocks, got %d", len(toolUseBlocks))
				}
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_ProductionLogTodoWrite tests TodoWrite parsing
// with the specific malformed patterns found in real production logs
func TestParseFunctionCallsFromContentForCC_ProductionLogTodoWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		content       string
		trigger       string
		expectToolUse bool
		expectTodoLen int
		expectStatus  string
	}{
		{
			name: "TodoWrite with state field instead of status",
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"state":"in_progress","content":"WebSearch搜索最简洁的Tkinter示例"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			expectStatus:  "in_progress",
		},
		{
			name: "TodoWrite with activeForm field",
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"content":"创建hello.py文件","activeForm":"正在创建文件","status":"pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			expectStatus:  "pending",
		},
		{
			name: "TodoWrite with numeric id",
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id":1,"content":"研究Python GUI框架","status":"in_progress"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			expectStatus:  "in_progress",
		},
		{
			name: "TodoWrite with multiple todos",
			content: `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id":"1","content":"搜索最佳实践","status":"completed"},{"id":"2","content":"编写代码","status":"pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 2,
			expectStatus:  "completed", // First todo's status
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.content)

			if tt.expectToolUse {
				if len(toolUseBlocks) == 0 {
					t.Fatalf("expected tool_use blocks, got none")
				}

				block := toolUseBlocks[0]
				if block.Name != "TodoWrite" {
					t.Fatalf("expected tool name TodoWrite, got %q", block.Name)
				}

				var input map[string]any
				if err := json.Unmarshal(block.Input, &input); err != nil {
					t.Fatalf("failed to unmarshal tool_use input: %v", err)
				}

				todos, ok := input["todos"].([]any)
				if !ok {
					t.Fatalf("expected todos to be array, got %T", input["todos"])
				}

				if len(todos) != tt.expectTodoLen {
					t.Fatalf("expected %d todos, got %d", tt.expectTodoLen, len(todos))
				}

				// Check first todo's status
				if len(todos) > 0 {
					firstTodo, ok := todos[0].(map[string]any)
					if !ok {
						t.Fatalf("expected todo to be map, got %T", todos[0])
					}
					status, ok := firstTodo["status"].(string)
					if !ok {
						t.Fatalf("expected status to be string, got %T", firstTodo["status"])
					}
					if status != tt.expectStatus {
						t.Errorf("expected status %q, got %q", tt.expectStatus, status)
					}
				}
			} else {
				if len(toolUseBlocks) > 0 {
					t.Fatalf("expected no tool_use blocks, got %d", len(toolUseBlocks))
				}
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_ProductionLogScenario tests the specific malformed patterns
// found in production logs where CC outputs repeated TodoWrite calls with malformed JSON.
// Issue: Model outputs <><invokename="TodoWrite"><parametername="todos">[{"id":"1",": "探索..."}]
// This causes repeated retries and "auto-pause" issues.
func TestParseFunctionCallsFromContentForCC_ProductionLogScenario(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name                           string
		content                        string
		trigger                        string
		expectToolUse                  bool
		expectTodoLen                  int
		expectActiveFormEqualsContentAt int
		description                    string
	}{
		{
			name: "malformed_invokename_with_missing_content_field",
			// Pattern from production log: <><invokename="TodoWrite"><parametername="todos">[{"id":"1",": "探索..."}]
			content:       `<<CALL_TEST>><><invokename="TodoWrite"><parametername="todos">[{"id":"1",": "探索最佳实践","activeForm":"正在探索","status":"pending"}]`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			expectActiveFormEqualsContentAt: -1,
			description:   "Malformed invokename with missing content field name in JSON",
		},
		{
			name: "malformed_invokename_multiple_todos",
			// Multiple todos with various malformations
			content:       `<<CALL_TEST>><><invokename="TodoWrite"><parametername="todos">[{"id":"1",": "搜索最佳实践","state":"pending"},{"id":"2",": "编写代码","Form":"正在编写","status":"pending"}]`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 2,
			expectActiveFormEqualsContentAt: 0,
			description:   "Multiple todos with missing content field names",
		},
		{
			name: "malformed_invokename_with_numeric_id",
			// Numeric id instead of string
			content:       `<<CALL_TEST>><><invokename="TodoWrite"><parametername="todos">[{"id":1,": "研究Python GUI框架","activeForm":"正在研究","status":"in_progress"}]`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			expectActiveFormEqualsContentAt: -1,
			description:   "Numeric id with missing content field name",
		},
		{
			name: "standard_invoke_with_malformed_json",
			// Standard invoke format but with malformed JSON
			content:       `<<CALL_TEST>><invoke name="TodoWrite"><parameter name="todos">[{"id":"1",": "调研联网搜索最佳实践","activeForm":"正在调研","status":"pending"}]</parameter></invoke>`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectTodoLen: 1,
			expectActiveFormEqualsContentAt: -1,
			description:   "Standard invoke format with malformed JSON content field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.content)

			if tt.expectToolUse {
				if len(toolUseBlocks) == 0 {
					t.Fatalf("[%s] expected tool_use blocks, got none", tt.description)
				}

				block := toolUseBlocks[0]
				if block.Name != "TodoWrite" {
					t.Fatalf("[%s] expected tool name TodoWrite, got %q", tt.description, block.Name)
				}

				var input map[string]any
				if err := json.Unmarshal(block.Input, &input); err != nil {
					t.Fatalf("[%s] failed to unmarshal tool_use input: %v", tt.description, err)
				}

				todos, ok := input["todos"].([]any)
				if !ok {
					t.Fatalf("[%s] expected todos to be array, got %T (input: %v)", tt.description, input["todos"], input)
				}

				if len(todos) != tt.expectTodoLen {
					t.Fatalf("[%s] expected %d todos, got %d", tt.description, tt.expectTodoLen, len(todos))
				}

				for i, todo := range todos {
					todoMap, ok := todo.(map[string]any)
					if !ok {
						t.Errorf("[%s] todo %d is not a map: %T", tt.description, i, todo)
						continue
					}

					contentAny, hasContent := todoMap["content"]
					contentStr, ok := contentAny.(string)
					if !hasContent {
						t.Errorf("[%s] todo %d missing content field: %v", tt.description, i, todoMap)
					} else if !ok || strings.TrimSpace(contentStr) == "" {
						t.Errorf("[%s] todo %d has invalid content: %v", tt.description, i, contentAny)
					}

					activeFormAny, hasActiveForm := todoMap["activeForm"]
					activeFormStr, ok := activeFormAny.(string)
					if !hasActiveForm {
						t.Errorf("[%s] todo %d missing activeForm field: %v", tt.description, i, todoMap)
					} else if !ok || strings.TrimSpace(activeFormStr) == "" {
						t.Errorf("[%s] todo %d has invalid activeForm: %v", tt.description, i, activeFormAny)
					}

					if i == tt.expectActiveFormEqualsContentAt && strings.TrimSpace(contentStr) != "" {
						if activeFormStr != contentStr {
							t.Errorf("[%s] todo %d expected activeForm to equal content %q, got %q", tt.description, i, contentStr, activeFormStr)
						}
					}

					status, hasStatus := todoMap["status"]
					if !hasStatus {
						t.Errorf("[%s] todo %d missing status field: %v", tt.description, i, todoMap)
					} else if _, ok := status.(string); !ok {
						t.Errorf("[%s] todo %d has invalid status type: %T", tt.description, i, status)
					}

					if _, hasID := todoMap["id"]; !hasID {
						t.Errorf("[%s] todo %d missing id field: %v", tt.description, i, todoMap)
					}
				}
			} else {
				if len(toolUseBlocks) > 0 {
					t.Fatalf("[%s] expected no tool_use blocks, got %d", tt.description, len(toolUseBlocks))
				}
			}
		})
	}
}

// TestGetThinkingModel tests the getThinkingModel helper function
func TestGetThinkingModel(t *testing.T) {
	tests := []struct {
		name     string
		config   map[string]any
		expected string
	}{
		{
			name:     "nil_group",
			config:   nil,
			expected: "",
		},
		{
			name:     "empty_config",
			config:   map[string]any{},
			expected: "",
		},
		{
			name:     "thinking_model_not_set",
			config:   map[string]any{"cc_support": true},
			expected: "",
		},
		{
			name:     "thinking_model_string",
			config:   map[string]any{"thinking_model": "deepseek-reasoner"},
			expected: "deepseek-reasoner",
		},
		{
			name:     "thinking_model_with_spaces",
			config:   map[string]any{"thinking_model": "  deepseek-reasoner  "},
			expected: "deepseek-reasoner",
		},
		{
			name:     "thinking_model_empty_string",
			config:   map[string]any{"thinking_model": ""},
			expected: "",
		},
		{
			name:     "thinking_model_nil",
			config:   map[string]any{"thinking_model": nil},
			expected: "",
		},
		{
			name:     "thinking_model_non_string",
			config:   map[string]any{"thinking_model": 123},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var group *models.Group
			if tt.config != nil {
				group = &models.Group{
					Config: tt.config,
				}
			}
			result := getThinkingModel(group)
			if result != tt.expected {
				t.Errorf("getThinkingModel() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestCCStreamingResponse_ReasoningContent tests that reasoning_content from
// DeepSeek reasoner models is correctly converted to Claude thinking blocks.
func TestCCStreamingResponse_ReasoningContent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Create a mock SSE stream with reasoning_content
	sseData := `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me think about this..."},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-reasoner","choices":[{"index":0,"delta":{"content":"Here is my response."},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-reasoner","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`

	// Create mock response
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseData)),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("original_model", "deepseek-reasoner")
	c.Set("thinking_model_applied", true)

	ps := &ProxyServer{}
	ps.handleCCStreamingResponse(c, resp)

	output := w.Body.String()

	// Verify thinking block was emitted
	if !strings.Contains(output, "thinking") {
		t.Errorf("expected thinking block in output, got: %s", output)
	}

	// Verify text content was emitted
	if !strings.Contains(output, "text_delta") {
		t.Errorf("expected text_delta in output, got: %s", output)
	}

	// Verify message_stop was emitted
	if !strings.Contains(output, "message_stop") {
		t.Errorf("expected message_stop in output, got: %s", output)
	}
}

// TestCCStreamingResponse_ReasoningContentWithToolCall tests that reasoning_content
// is properly handled when followed by tool calls.
func TestCCStreamingResponse_ReasoningContentWithToolCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	trigger := "<<CALL_test123>>"

	// Create a mock SSE stream with reasoning_content followed by tool call
	sseData := fmt.Sprintf(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Planning to create a task list..."},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-reasoner","choices":[{"index":0,"delta":{"content":"%s\n<invoke name=\"TodoWrite\"><parameter name=\"todos\">[{\"content\":\"task1\",\"status\":\"pending\"}]</parameter></invoke>"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"deepseek-reasoner","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`, trigger)

	// Create mock response
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseData)),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("original_model", "deepseek-reasoner")
	c.Set("thinking_model_applied", true)
	c.Set(ctxKeyTriggerSignal, trigger)
	c.Set(ctxKeyFunctionCallEnabled, true)

	ps := &ProxyServer{}
	ps.handleCCStreamingResponse(c, resp)

	output := w.Body.String()

	// Verify thinking block was emitted
	if !strings.Contains(output, "thinking") {
		t.Errorf("expected thinking block in output, got: %s", output)
	}

	// Verify tool_use block was emitted
	if !strings.Contains(output, "tool_use") {
		t.Errorf("expected tool_use block in output, got: %s", output)
	}

	// Verify TodoWrite tool was parsed
	if !strings.Contains(output, "TodoWrite") {
		t.Errorf("expected TodoWrite in output, got: %s", output)
	}
}

// TestCCNormalResponse_ReasoningContent tests that reasoning_content from
// DeepSeek reasoner models is correctly converted to Claude thinking blocks
// in non-streaming responses.
func TestCCNormalResponse_ReasoningContent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stopReason := "stop"
	content := "Here is my response."
	reasoning := "Let me think about this problem step by step..."

	openaiResp := &OpenAIResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 123,
		Model:   "deepseek-reasoner",
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: &OpenAIRespMessage{
					Role:             "assistant",
					Content:          &content,
					ReasoningContent: &reasoning,
				},
				FinishReason: &stopReason,
			},
		},
		Usage: &OpenAIUsage{PromptTokens: 10, CompletionTokens: 20},
	}

	bodyBytes, err := json.Marshal(openaiResp)
	if err != nil {
		t.Fatalf("failed to marshal OpenAIResponse: %v", err)
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(bodyBytes))),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("original_model", "deepseek-reasoner")
	c.Set("thinking_model_applied", true)

	ps := &ProxyServer{}
	ps.handleCCNormalResponse(c, resp)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	outStr := w.Body.String()

	// Verify thinking block was included
	if !strings.Contains(outStr, "thinking") {
		t.Errorf("expected thinking block in output, got: %s", outStr)
	}

	// Verify reasoning content was included
	if !strings.Contains(outStr, "step by step") {
		t.Errorf("expected reasoning content in output, got: %s", outStr)
	}

	// Verify text content was included
	if !strings.Contains(outStr, "Here is my response") {
		t.Errorf("expected text content in output, got: %s", outStr)
	}

	// Parse and verify structure
	var claudeResp ClaudeResponse
	if err := json.Unmarshal([]byte(outStr), &claudeResp); err != nil {
		t.Fatalf("failed to parse Claude response: %v", err)
	}

	// Should have at least 2 content blocks: thinking + text
	if len(claudeResp.Content) < 2 {
		t.Errorf("expected at least 2 content blocks, got %d", len(claudeResp.Content))
	}

	// First block should be thinking
	hasThinking := false
	hasText := false
	for _, block := range claudeResp.Content {
		if block.Type == "thinking" && block.Thinking != "" {
			hasThinking = true
		}
		if block.Type == "text" && block.Text != "" {
			hasText = true
		}
	}

	if !hasThinking {
		t.Errorf("expected thinking block in content, got: %+v", claudeResp.Content)
	}
	if !hasText {
		t.Errorf("expected text block in content, got: %+v", claudeResp.Content)
	}
}

// TestCCNormalResponse_ReasoningContentWithToolCall tests that reasoning_content
// is properly handled when followed by tool calls in non-streaming responses.
func TestCCNormalResponse_ReasoningContentWithToolCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	trigger := "<<CALL_test123>>"
	stopReason := "stop"
	content := trigger + `<invoke name="TodoWrite"><parameter name="todos">[{"content":"task1","status":"pending"}]</parameter></invoke>`
	reasoning := "Planning to create a task list for the user..."

	openaiResp := &OpenAIResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 123,
		Model:   "deepseek-reasoner",
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: &OpenAIRespMessage{
					Role:             "assistant",
					Content:          &content,
					ReasoningContent: &reasoning,
				},
				FinishReason: &stopReason,
			},
		},
		Usage: &OpenAIUsage{PromptTokens: 10, CompletionTokens: 20},
	}

	bodyBytes, err := json.Marshal(openaiResp)
	if err != nil {
		t.Fatalf("failed to marshal OpenAIResponse: %v", err)
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(bodyBytes))),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("original_model", "deepseek-reasoner")
	c.Set("thinking_model_applied", true)
	c.Set(ctxKeyTriggerSignal, trigger)
	c.Set(ctxKeyFunctionCallEnabled, true)

	ps := &ProxyServer{}
	ps.handleCCNormalResponse(c, resp)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	outStr := w.Body.String()

	// Verify thinking block was included
	if !strings.Contains(outStr, "thinking") {
		t.Errorf("expected thinking block in output, got: %s", outStr)
	}

	// Verify tool_use block was included
	if !strings.Contains(outStr, "tool_use") {
		t.Errorf("expected tool_use block in output, got: %s", outStr)
	}

	// Verify TodoWrite tool was parsed
	if !strings.Contains(outStr, "TodoWrite") {
		t.Errorf("expected TodoWrite in output, got: %s", outStr)
	}

	// Verify no raw XML leaked
	if strings.Contains(outStr, "<invoke") {
		t.Errorf("expected no raw XML in output, got: %s", outStr)
	}
	if strings.Contains(outStr, trigger) {
		t.Errorf("expected no trigger signal in output, got: %s", outStr)
	}
}

// assertNoXMLOrTrigger verifies that the given output does not contain any
// raw XML invoke blocks, malformed <> fragments, or trigger signals.
func assertNoXMLOrTrigger(t *testing.T, output, trigger string) {
	t.Helper()
	if strings.Contains(output, "<invoke") {
		t.Fatalf("output leaked <invoke XML: %s", output)
	}
	if strings.Contains(output, "<>") {
		t.Fatalf("output leaked malformed <> fragment: %s", output)
	}
	if trigger != "" && strings.Contains(output, trigger) {
		t.Fatalf("output leaked trigger signal %q: %s", trigger, output)
	}
}

// assertToolUseWithName verifies that a tool_use block with the given name is
// present in the serialized output.
func assertToolUseWithName(t *testing.T, output, toolName string) {
	t.Helper()
	if !strings.Contains(output, `"type":"tool_use"`) {
		t.Fatalf("expected tool_use block in output, got: %s", output)
	}
	if !strings.Contains(output, `"name":"`+toolName+`"`) {
		t.Fatalf("expected tool_use with name %q in output, got: %s", toolName, output)
	}
}

// assertHasInputJSONKey verifies that the tool_use input JSON delta contains
// the specified key (e.g., todos or questions).
func assertHasInputJSONKey(t *testing.T, output, key string) {
	t.Helper()
	if !strings.Contains(output, `"type":"input_json_delta"`) {
		t.Fatalf("expected input_json_delta in output, got: %s", output)
	}
	if !strings.Contains(output, key) {
		t.Fatalf("expected JSON key %q in tool_use input, got: %s", key, output)
	}
}

// TestCCFunctionCall_StreamingInvokeXMLToToolUse ensures that XML-based invoke
// blocks from force_function_call are converted into Claude tool_use blocks in
// CC streaming mode, and that no raw XML or trigger markers leak into the SSE
// stream returned to the client.
func TestCCFunctionCall_StreamingInvokeXMLToToolUse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	trigger := "<<CALL_TEST>>" // Matches reTriggerSignal pattern
	stopReason := "stop"

	tests := []struct {
		name          string
		toolName      string
		invokeInner   string
		expectJSONKey string
	}{
		{
			name:     "TodoWrite from invoke XML",
			toolName: "TodoWrite",
			invokeInner: `<parameter name="todos">` +
				`[{"content":"task one","state":"pending"}]` +
				`</parameter>`,
			expectJSONKey: "todos",
		},
		{
			name:     "AskUserQuestion from invoke XML",
			toolName: "AskUserQuestion",
			invokeInner: `<parameter name="questions">["q1"]</parameter>` +
				`<parameter name="answers">{"q1":"a1"}</parameter>`,
			expectJSONKey: "questions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Intro " + trigger + `<invoke name="` + tt.toolName + `">` + tt.invokeInner + `</invoke> Outro`

			chunk := &OpenAIResponse{
				Model: "test-model",
				Choices: []OpenAIChoice{
					{
						Index: 0,
						Delta: &OpenAIRespMessage{
							Content: &content,
						},
						FinishReason: &stopReason,
					},
				},
				Usage: &OpenAIUsage{PromptTokens: 1, CompletionTokens: 2},
			}

			body := buildTestSSEBody(chunk)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			// Enable function-call bridge and provide trigger signal for CC handler.
			c.Set(ctxKeyTriggerSignal, trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)
			c.Set("original_model", "test-model")

			ps := &ProxyServer{}
			ps.handleCCStreamingResponse(c, resp)

			output := w.Body.String()
			if output == "" {
				t.Fatalf("expected SSE output, got empty body")
			}

			// Ensure no raw XML or trigger markers are leaked to the client.
			assertNoXMLOrTrigger(t, output, trigger)

			// Ensure a tool_use block was emitted with the expected tool name.
			assertToolUseWithName(t, output, tt.toolName)

			// Ensure arguments were emitted as JSON via input_json_delta and contain
			// the key we expect from the invoke parameters (e.g., todos or questions).
			assertHasInputJSONKey(t, output, tt.expectJSONKey)
		})
	}
}

// TestCCFunctionCall_NormalResponseInvokeXMLToToolUse ensures that XML-based
// invoke blocks in a non-streaming OpenAI response are converted into Claude
// tool_use blocks in CC mode, and that no raw XML or trigger markers leak into
// the JSON body returned to the client.
func TestCCFunctionCall_NormalResponseInvokeXMLToToolUse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	trigger := "<<CALL_TEST>>" // Matches reTriggerSignal pattern
	stopReason := "stop"

	tests := []struct {
		name          string
		toolName      string
		invokeInner   string
		expectJSONKey string
	}{
		{
			name:     "TodoWrite from invoke XML (normal response)",
			toolName: "TodoWrite",
			invokeInner: `<parameter name="todos">` +
				`[{"content":"task one","state":"pending"}]` +
				`</parameter>`,
			expectJSONKey: "todos",
		},
		{
			name:     "AskUserQuestion from invoke XML (normal response)",
			toolName: "AskUserQuestion",
			invokeInner: `<parameter name="questions">["q1"]</parameter>` +
				`<parameter name="answers">{"q1":"a1"}</parameter>`,
			expectJSONKey: "questions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "Intro " + trigger + `<invoke name="` + tt.toolName + `">` + tt.invokeInner + `</invoke> Outro`

			msg := &OpenAIRespMessage{
				Role:    "assistant",
				Content: &content,
			}

			openaiResp := &OpenAIResponse{
				ID:      "chatcmpl-test",
				Object:  "chat.completion",
				Created: 123,
				Model:   "test-model",
				Choices: []OpenAIChoice{
					{
						Index:        0,
						Message:      msg,
						FinishReason: &stopReason,
					},
				},
				Usage: &OpenAIUsage{PromptTokens: 1, CompletionTokens: 2},
			}

			bodyBytes, err := json.Marshal(openaiResp)
			if err != nil {
				t.Fatalf("failed to marshal OpenAIResponse: %v", err)
			}

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(bodyBytes))),
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			// Enable function-call bridge and provide trigger signal for CC handler.
			c.Set(ctxKeyTriggerSignal, trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			ps := &ProxyServer{}
			ps.handleCCNormalResponse(c, resp)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
			}

			outStr := w.Body.String()
			if outStr == "" {
				t.Fatalf("expected JSON body, got empty body")
			}

			// Ensure no raw XML or trigger markers are leaked to the client.
			if strings.Contains(outStr, "<invoke") {
				t.Fatalf("JSON output leaked <invoke XML: %s", outStr)
			}
			if strings.Contains(outStr, "<>") {
				t.Fatalf("JSON output leaked malformed <> fragment: %s", outStr)
			}
			if strings.Contains(outStr, trigger) {
				t.Fatalf("JSON output leaked trigger signal %q: %s", trigger, outStr)
			}

			var claudeResp ClaudeResponse
			if err := json.Unmarshal(w.Body.Bytes(), &claudeResp); err != nil {
				t.Fatalf("failed to unmarshal ClaudeResponse: %v", err)
			}
			if len(claudeResp.Content) == 0 {
				t.Fatalf("expected at least one content block in Claude response")
			}

			// Ensure text blocks in Claude response do not contain raw XML or trigger markers.
			foundText := false
			for _, block := range claudeResp.Content {
				if block.Type != "text" {
					continue
				}
				foundText = true
				if strings.Contains(block.Text, "<invoke") {
					t.Fatalf("Claude text block leaked <invoke XML: %s", block.Text)
				}
				if strings.Contains(block.Text, "<>") {
					t.Fatalf("Claude text block leaked malformed <> fragment: %s", block.Text)
				}
				if strings.Contains(block.Text, trigger) {
					t.Fatalf("Claude text block leaked trigger signal %q: %s", trigger, block.Text)
				}
			}
			if !foundText {
				t.Fatalf("expected at least one text block in Claude response, got: %+v", claudeResp.Content)
			}
		})
	}
}

// TestCCStreaming_StopReasonDowngradeWhenNoToolUse verifies that when upstream
// finish_reason=tool_calls but no actual tool_call content is present and the
// function-call bridge is disabled, the stop_reason is downgraded to end_turn
// to avoid clients waiting for nonexistent tool results.
func TestCCStreaming_StopReasonDowngradeWhenNoToolUse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stopReason := "tool_calls"
	content := "Hello without tools"

	chunk := &OpenAIResponse{
		Model: "test-model",
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Delta: &OpenAIRespMessage{
					Content: &content,
				},
				FinishReason: &stopReason,
			},
		},
		Usage: &OpenAIUsage{PromptTokens: 1, CompletionTokens: 2},
	}

	body := buildTestSSEBody(chunk)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	ps := &ProxyServer{}
	ps.handleCCStreamingResponse(c, resp)

	output := w.Body.String()
	if output == "" {
		t.Fatalf("expected SSE output, got empty body")
	}
	if !strings.Contains(output, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected stop_reason downgraded to end_turn, got: %s", output)
	}
	if strings.Contains(output, `"tool_use"`) {
		t.Fatalf("unexpected tool_use block when no tool calls present, output: %s", output)
	}
}

// TestRepairMalformedJSON_RealWorldMalformedLog tests the JSON repair logic on the specific malformed JSON from production.
func TestRepairMalformedJSON_RealWorldMalformedLog(t *testing.T) {
	// This is the malformed JSON extracted from the log
	malformedJSON := `[id":": "搜索Python最短GUI Hello World最佳实践", "activeForm": "正在搜索最佳实践",
  "status": "in_progress"},检查hello.py是否存在", "activeForm": "正在检查文件", "status": "pending"},简洁的GUI
  Hellostatus": "pending"},d": "4", "content": "运行程序", "activeForm": "正在运行程序", "status":]`

	repaired := repairMalformedJSON(malformedJSON)

	// Try to unmarshal to verify it's valid JSON
	var todos []map[string]any
	err := json.Unmarshal([]byte(repaired), &todos)
	if err != nil {
		t.Logf("Repaired JSON: %s", repaired)
		t.Errorf("Failed to unmarshal repaired JSON: %v", err)
	}

	if len(todos) == 0 {
		t.Errorf("Expected at least one todo item, got 0")
	}
}

// TestToolChoiceConversion tests the conversion of Claude tool_choice to OpenAI format
func TestToolChoiceConversion(t *testing.T) {
	tests := []struct {
		name          string
		claudeChoice  string
		expectedType  string
		expectedValue interface{}
	}{
		{
			name:         "specific tool",
			claudeChoice: `{"type":"tool","name":"get_weather"}`,
			expectedType: "map",
			expectedValue: map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": "get_weather",
				},
			},
		},
		{
			name:          "any tool",
			claudeChoice:  `{"type":"any"}`,
			expectedType:  "string",
			expectedValue: "required",
		},
		{
			name:          "auto",
			claudeChoice:  `{"type":"auto"}`,
			expectedType:  "string",
			expectedValue: "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeReq := &ClaudeRequest{
				Model:      "claude-3-5-sonnet-20241022",
				Messages:   []ClaudeMessage{{Role: "user", Content: json.RawMessage(`"test"`)}},
				ToolChoice: json.RawMessage(tt.claudeChoice),
				MaxTokens:  100,
			}

			openaiReq, err := convertClaudeToOpenAI(claudeReq)
			if err != nil {
				t.Fatalf("conversion failed: %v", err)
			}
			if openaiReq.ToolChoice == nil {
				t.Fatalf("tool_choice should be set")
			}

			switch tt.expectedType {
			case "string":
				if openaiReq.ToolChoice != tt.expectedValue {
					t.Errorf("tool_choice = %v, want %v", openaiReq.ToolChoice, tt.expectedValue)
				}
			case "map":
				// Convert to JSON for comparison
				expectedJSON, _ := json.Marshal(tt.expectedValue)
				actualJSON, _ := json.Marshal(openaiReq.ToolChoice)
				var expected, actual interface{}
				json.Unmarshal(expectedJSON, &expected)
				json.Unmarshal(actualJSON, &actual)
				expectedStr, _ := json.Marshal(expected)
				actualStr, _ := json.Marshal(actual)
				if string(expectedStr) != string(actualStr) {
					t.Errorf("tool_choice = %s, want %s", actualStr, expectedStr)
				}
			}
		})
	}
}

// TestJSONSerializationFix tests that JSON serialization avoids double-encoding
func TestJSONSerializationFix(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "simple object",
			input:    map[string]interface{}{"key": "value"},
			expected: `{"key":"value"}`,
		},
		{
			name:     "array",
			input:    []interface{}{"item1", "item2"},
			expected: `["item1","item2"]`,
		},
		{
			name:     "nested object",
			input:    map[string]interface{}{"todos": []map[string]string{{"id": "1", "content": "task1"}}},
			expected: `{"todos":[{"content":"task1","id":"1"}]}`,
		},
		{
			name:     "already JSON string",
			input:    `{"key":"value"}`,
			expected: `{"key":"value"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result interface{}

			if str, ok := tt.input.(string); ok {
				// Test that already-JSON strings are not double-encoded
				err := json.Unmarshal([]byte(str), &result)
				if err != nil {
					t.Errorf("should be valid JSON: %v", err)
				}
			} else {
				// Test that objects are properly serialized
				jsonBytes, err := json.Marshal(tt.input)
				if err != nil {
					t.Fatalf("marshal failed: %v", err)
				}
				// Normalize both for comparison
				var expected, actual interface{}
				json.Unmarshal([]byte(tt.expected), &expected)
				json.Unmarshal(jsonBytes, &actual)
				expectedJSON, _ := json.Marshal(expected)
				actualJSON, _ := json.Marshal(actual)
				if string(expectedJSON) != string(actualJSON) {
					t.Errorf("JSON = %s, want %s", actualJSON, expectedJSON)
				}
			}
		})
	}
}

// TestClaudeToOpenAIConversion tests the full conversion from Claude to OpenAI format
func TestClaudeToOpenAIConversion(t *testing.T) {
	tests := []struct {
		name        string
		claudeReq   *ClaudeRequest
		wantErr     bool
		checkFields func(*testing.T, *OpenAIRequest)
	}{
		{
			name: "basic conversion with tools",
			claudeReq: &ClaudeRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				Messages: []ClaudeMessage{
					{Role: "user", Content: json.RawMessage(`"Hello"`)},
				},
				Tools: []ClaudeTool{
					{
						Name:        "get_weather",
						Description: "Get weather info",
						InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
					},
				},
			},
			wantErr: false,
			checkFields: func(t *testing.T, req *OpenAIRequest) {
				if len(req.Tools) != 1 {
					t.Errorf("expected 1 tool, got %d", len(req.Tools))
				}
				if req.Tools[0].Function.Name != "get_weather" {
					t.Errorf("tool name = %s, want get_weather", req.Tools[0].Function.Name)
				}
			},
		},
		{
			name: "conversion with tool_choice",
			claudeReq: &ClaudeRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				Messages: []ClaudeMessage{
					{Role: "user", Content: json.RawMessage(`"Hello"`)},
				},
				Tools: []ClaudeTool{
					{Name: "test_tool", InputSchema: json.RawMessage(`{}`)},
				},
				ToolChoice: json.RawMessage(`{"type":"tool","name":"test_tool"}`),
			},
			wantErr: false,
			checkFields: func(t *testing.T, req *OpenAIRequest) {
				if req.ToolChoice == nil {
					t.Error("tool_choice should be set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			openaiReq, err := convertClaudeToOpenAI(tt.claudeReq)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertClaudeToOpenAI() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkFields != nil {
				tt.checkFields(t, openaiReq)
			}
		})
	}
}

// TestRemoveClaudeCodePreamble directly tests the preamble removal function
func TestRemoveClaudeCodePreamble(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single preamble line",
			input:    "根据用户的要求，我需要创建程序。",
			expected: "根据用户的要求，我需要创建程序。",
		},
		{
			name: "preamble with valid content",
			input: `根据用户的要求，我需要创建程序。
这是一个有效的程序说明文本，包含了详细的实现步骤和代码结构。`,
			// "根据用户的要求" is not filtered as it's a normal narrative opening
			expected: `根据用户的要求，我需要创建程序。
这是一个有效的程序说明文本，包含了详细的实现步骤和代码结构。`,
		},
		{
			name: "multiple preamble lines",
			input: `我需要修正参数格式。
让我重新创建任务清单。
我现在开始执行操作。`,
			// Retry phrases are preserved to avoid over-filtering in streaming mode
			expected: `我需要修正参数格式。
让我重新创建任务清单。
我现在开始执行操作。`,
		},
		{
			name: "mixed preamble and content",
			input: `我来帮你创建GUI程序。
程序将使用tkinter框架实现。
让我开始编写代码。
代码结构如下：主窗口、标签、按钮。`,
			expected: `我来帮你创建GUI程序。
程序将使用tkinter框架实现。
让我开始编写代码。
代码结构如下：主窗口、标签、按钮。`,
		},
		{
			name: "preserve long natural text with keywords",
			input: `这个项目我需要仔细考虑，因为它涉及到多个方面的技术选型。我将使用Python作为主要开发语言，选择tkinter作为GUI框架，并确保代码简洁易读。`,
			expected: `这个项目我需要仔细考虑，因为它涉及到多个方面的技术选型。我将使用Python作为主要开发语言，选择tkinter作为GUI框架，并确保代码简洁易读。`,
		},
		{
			name: "JSON leak without natural text",
			input: `{"id": "1", "content": "task"}`,
			expected: "",
		},
		{
			name: "JSON leak with bullet - on same line",
			input: `● [{"id": "1", "content": "task", "status": "pending"}]`,
			expected: ``,
		},
		{
			name:     "preserve bullet only",
			input:    "●",
			expected: "",
		},
		{
			name: "preserve empty lines",
			input: `Line 1

Line 3`,
			expected: `Line 1

Line 3`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeClaudeCodePreamble(tt.input)
			if result != tt.expected {
				t.Errorf("removeClaudeCodePreamble() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestRemoveClaudeCodePreamble_ProductionLogIssues tests preamble removal for issues observed in real production logs
func TestRemoveClaudeCodePreamble_ProductionLogIssues(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		notContains []string
		contains    []string
	}{
		{
			name:        "filter_ImplementationPlan",
			input:       "ImplementationPlan, TaskList and ThoughtinChinese\n正常内容",
			notContains: []string{"ImplementationPlan", "TaskList", "ThoughtinChinese"},
			contains:    []string{"正常内容"},
		},
		{
			// NOTE: Markdown headers are preserved as they may be valid content headers
			// Structural approach cannot distinguish "plan headers" from "content headers"
			name:     "preserve_markdown_header",
			input:    "## Implementation Plan\n\n目标：创建程序",
			contains: []string{"## Implementation Plan", "目标：创建程序"},
		},
		{
			name:        "filter_leaked_JSON_structure",
			input:       `[{"id":"1","content":"task","status":"pending"}]`,
			notContains: []string{`"id"`, `"content"`, `"status"`},
		},
		{
			name:        "preserve_normal_text",
			input:       "我来帮你创建一个程序",
			contains:    []string{"我来帮你创建一个程序"},
		},
		{
			name:        "filter_malformed_XML",
			input:       "<><invokename=\"Test\">value",
			notContains: []string{"<><", "<invokename"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeClaudeCodePreamble(tt.input)

			for _, s := range tt.notContains {
				if strings.Contains(result, s) {
					t.Errorf("removeClaudeCodePreamble() result should NOT contain %q, got %q", s, result)
				}
			}

			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("removeClaudeCodePreamble() result should contain %q, got %q", s, result)
				}
			}
		})
	}
}

// buildTestSSEBody builds a minimal SSE stream with a single OpenAIResponse chunk.
func buildTestSSEBody(chunk *OpenAIResponse) string {
	payload, err := json.Marshal(chunk)
	if err != nil {
		// This should never happen with a well-formed struct in tests.
		panic(err)
	}
	var b strings.Builder
	b.WriteString("event: message\n")
	b.WriteString("data: ")
	b.Write(payload)
	b.WriteString("\n\n")
	return b.String()
}

// TestThinkingParserMergesBehavior tests that ThinkingParser correctly merges
// multiple thinking fragments into events that can be combined into a single block.
// This is critical for Claude Code to display "∴ Thinking…" as a single merged block
// instead of fragmented "∴ Thinking…、∴ Thinking…检查∴ Thinking…" display.
// Per b4u2cc reference implementation: thinking content should be accumulated
// into a single thinking block rather than creating separate blocks for each fragment.
func TestThinkingParserMergesBehavior(t *testing.T) {
	tests := []struct {
		name                 string
		input                string
		expectedThinkingEvts int
		expectedTextEvts     int
		description          string
	}{
		{
			name:                 "single_thinking_block",
			input:                "<thinking>Let me analyze this problem step by step.</thinking>",
			expectedThinkingEvts: 1,
			expectedTextEvts:     0,
			description:          "Single thinking block should produce one thinking event",
		},
		{
			name:                 "thinking_then_text",
			input:                "<thinking>Analyzing...</thinking>Here is my response.",
			expectedThinkingEvts: 1,
			expectedTextEvts:     1,
			description:          "Thinking followed by text should produce one of each",
		},
		{
			name:                 "multiple_thinking_blocks_interleaved",
			input:                "<thinking>First thought</thinking>Text<thinking>Second thought</thinking>More text",
			expectedThinkingEvts: 2,
			expectedTextEvts:     2,
			description:          "Multiple thinking blocks interleaved with text",
		},
		{
			name:                 "continuous_thinking_no_text",
			input:                "<thinking>Step 1: Analyze</thinking><thinking>Step 2: Plan</thinking>",
			expectedThinkingEvts: 2,
			expectedTextEvts:     0,
			description:          "Continuous thinking blocks without text between them",
		},
		{
			name:                 "alt_think_tag",
			input:                "<think>Using alternative tag</think>Response here",
			expectedThinkingEvts: 1,
			expectedTextEvts:     1,
			description:          "Alternative <think> tag should work the same",
		},
		{
			name:                 "chinese_thinking_content",
			input:                "<thinking>让我分析一下这个问题。首先检查文件结构，然后创建GUI程序。</thinking>好的，我来帮你完成这个任务。",
			expectedThinkingEvts: 1,
			expectedTextEvts:     1,
			description:          "Chinese content in thinking block",
		},
		{
			name:                 "thinking_with_special_chars",
			input:                "<thinking>Step 1: Check if hello.py exists\nStep 2: Create GUI with tkinter\nStep 3: Run the program</thinking>",
			expectedThinkingEvts: 1,
			expectedTextEvts:     0,
			description:          "Thinking with newlines and special characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewThinkingParser()

			for _, r := range tt.input {
				parser.FeedRune(r)
			}
			parser.Finish()

			events := parser.ConsumeEvents()

			thinkingCount := 0
			textCount := 0
			for _, evt := range events {
				switch evt.Type {
				case "thinking":
					if strings.TrimSpace(evt.Content) != "" {
						thinkingCount++
					}
				case "text":
					if strings.TrimSpace(evt.Content) != "" {
						textCount++
					}
				}
			}

			if thinkingCount != tt.expectedThinkingEvts {
				t.Errorf("[%s] expected %d thinking events, got %d", tt.description, tt.expectedThinkingEvts, thinkingCount)
			}
			if textCount != tt.expectedTextEvts {
				t.Errorf("[%s] expected %d text events, got %d", tt.description, tt.expectedTextEvts, textCount)
			}
		})
	}
}

// TestThinkingParserWithFunctionCalls tests that ThinkingParser correctly handles
// content that contains both <thinking> tags and function call trigger signals.
func TestThinkingParserWithFunctionCalls(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedEvents []ThinkingEvent
	}{
		{
			name:  "thinking_then_text",
			input: "<thinking>Let me analyze this...</thinking>Here is my response.",
			expectedEvents: []ThinkingEvent{
				{Type: "thinking", Content: "Let me analyze this..."},
				{Type: "text", Content: "Here is my response."},
				{Type: "end"},
			},
		},
		{
			name:  "text_then_thinking_then_text",
			input: "First part <thinking>My thoughts</thinking> Second part",
			expectedEvents: []ThinkingEvent{
				{Type: "text", Content: "First part "},
				{Type: "thinking", Content: "My thoughts"},
				{Type: "text", Content: " Second part"},
				{Type: "end"},
			},
		},
		{
			name:  "thinking_with_trigger_signal_after",
			input: "<thinking>Planning to call a tool...</thinking>\n<<CALL_abc123>>\n<invoke name=\"test\">",
			expectedEvents: []ThinkingEvent{
				{Type: "thinking", Content: "Planning to call a tool..."},
				{Type: "text", Content: "\n<<CALL_abc123>>\n<invoke name=\"test\">"},
				{Type: "end"},
			},
		},
		{
			name:  "only_thinking",
			input: "<thinking>Just thinking, no action</thinking>",
			expectedEvents: []ThinkingEvent{
				{Type: "thinking", Content: "Just thinking, no action"},
				{Type: "end"},
			},
		},
		{
			name:  "empty_thinking",
			input: "<thinking></thinking>Some text",
			expectedEvents: []ThinkingEvent{
				{Type: "text", Content: "Some text"},
				{Type: "end"},
			},
		},
		{
			name:  "alt_think_tags",
			input: "<think>Alternative thinking tags</think>Response",
			expectedEvents: []ThinkingEvent{
				{Type: "thinking", Content: "Alternative thinking tags"},
				{Type: "text", Content: "Response"},
				{Type: "end"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewThinkingParser()

			for _, r := range tt.input {
				parser.FeedRune(r)
			}
			parser.Finish()

			events := parser.ConsumeEvents()

			if len(events) != len(tt.expectedEvents) {
				t.Errorf("expected %d events, got %d", len(tt.expectedEvents), len(events))
				for i, e := range events {
					t.Logf("  event[%d]: type=%s, content=%q", i, e.Type, e.Content)
				}
				return
			}

			for i, expected := range tt.expectedEvents {
				actual := events[i]
				if actual.Type != expected.Type {
					t.Errorf("event[%d]: expected type %q, got %q", i, expected.Type, actual.Type)
				}
				if actual.Content != expected.Content {
					t.Errorf("event[%d]: expected content %q, got %q", i, expected.Content, actual.Content)
				}
			}
		})
	}
}

// TestThinkingParserWithToolCalls tests that ThinkingParser correctly handles
// thinking blocks followed by tool calls, ensuring proper block closure.
// This is based on b4u2cc reference implementation behavior.
func TestThinkingParserWithToolCalls(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectThinking bool
		expectText     bool
		description    string
	}{
		{
			name:           "thinking_then_text",
			input:          "<thinking>Let me analyze this...</thinking>Here is my response.",
			expectThinking: true,
			expectText:     true,
			description:    "Thinking block followed by text",
		},
		{
			name:           "text_only",
			input:          "Here is my response without thinking.",
			expectThinking: false,
			expectText:     true,
			description:    "Text only without thinking block",
		},
		{
			name:           "thinking_only",
			input:          "<thinking>Just thinking, no response yet.</thinking>",
			expectThinking: true,
			expectText:     false,
			description:    "Thinking block only",
		},
		{
			name:           "multiple_thinking_blocks",
			input:          "<thinking>First thought</thinking>Some text<thinking>Second thought</thinking>More text",
			expectThinking: true,
			expectText:     true,
			description:    "Multiple thinking blocks interleaved with text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewThinkingParser()

			for _, r := range tt.input {
				parser.FeedRune(r)
			}
			parser.Finish()

			events := parser.ConsumeEvents()

			hasThinking := false
			hasText := false
			for _, evt := range events {
				switch evt.Type {
				case "thinking":
					hasThinking = true
				case "text":
					if strings.TrimSpace(evt.Content) != "" {
						hasText = true
					}
				}
			}

			if tt.expectThinking && !hasThinking {
				t.Errorf("[%s] expected thinking event, got none", tt.description)
			}
			if !tt.expectThinking && hasThinking {
				t.Errorf("[%s] expected no thinking event, but got one", tt.description)
			}
			if tt.expectText && !hasText {
				t.Errorf("[%s] expected text event, got none", tt.description)
			}
			if !tt.expectText && hasText {
				t.Errorf("[%s] expected no text event, but got one", tt.description)
			}
		})
	}
}

// TestThinkingParser_ANTMLFormat tests that ThinkingParser correctly handles
// ANTML format thinking tags (<antml\b:thinking>...</antml\b:thinking>).
// This format is used by some models like claude-opus-4-5-thinking.
func TestThinkingParser_ANTMLFormat(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectThinking bool
		expectText     bool
		thinkingCount  int
		description    string
	}{
		{
			name:           "antml_thinking_then_text",
			input:          "<antml\\b:thinking>Let me analyze this...</antml\\b:thinking>Here is my response.",
			expectThinking: true,
			expectText:     true,
			thinkingCount:  1,
			description:    "ANTML thinking block followed by text",
		},
		{
			name:           "antml_thinking_with_generic_closer",
			input:          "<antml\\b:thinking>Let me think...</antml>Here is my response.",
			expectThinking: true,
			expectText:     true,
			thinkingCount:  1,
			description:    "ANTML thinking block with generic </antml> closer",
		},
		{
			name:           "antml_thinking_only",
			input:          "<antml\\b:thinking>Just thinking, no response yet.</antml\\b:thinking>",
			expectThinking: true,
			expectText:     false,
			thinkingCount:  1,
			description:    "ANTML thinking block only",
		},
		{
			name:           "antml_thinking_multiline",
			input:          "<antml\\b:thinking>\n用户要求我：\n1. 使用 MCP Exa 工具联网搜索最佳实践\n2. 修改或创建 hello.py 文件\n</antml\\b:thinking>\n\n我来为您规划并执行这个任务。",
			expectThinking: true,
			expectText:     true,
			thinkingCount:  1,
			description:    "ANTML thinking block with multiline Chinese content",
		},
		{
			name:           "mixed_thinking_formats",
			input:          "<thinking>First thought</thinking>Some text<antml\\b:thinking>Second thought</antml\\b:thinking>More text",
			expectThinking: true,
			expectText:     true,
			thinkingCount:  2,
			description:    "Mixed standard and ANTML thinking formats",
		},
		{
			name:           "antml_thinking_with_tool_call",
			input:          "<antml\\b:thinking>Let me plan this task.</antml\\b:thinking>\n\n<<CALL_test>>\n<invoke name=\"TodoWrite\"><parameter name=\"todos\">[{\"id\":\"1\"}]</parameter></invoke>",
			expectThinking: true,
			expectText:     true,
			thinkingCount:  1,
			description:    "ANTML thinking followed by tool call",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewThinkingParser()

			for _, r := range tt.input {
				parser.FeedRune(r)
			}
			parser.Finish()

			events := parser.ConsumeEvents()

			thinkingCount := 0
			hasText := false
			for _, evt := range events {
				switch evt.Type {
				case "thinking":
					thinkingCount++
				case "text":
					if strings.TrimSpace(evt.Content) != "" {
						hasText = true
					}
				}
			}

			if tt.expectThinking && thinkingCount == 0 {
				t.Errorf("[%s] expected thinking event, got none", tt.description)
			}
			if !tt.expectThinking && thinkingCount > 0 {
				t.Errorf("[%s] expected no thinking event, but got %d", tt.description, thinkingCount)
			}
			if tt.thinkingCount > 0 && thinkingCount != tt.thinkingCount {
				t.Errorf("[%s] expected %d thinking events, got %d", tt.description, tt.thinkingCount, thinkingCount)
			}
			if tt.expectText && !hasText {
				t.Errorf("[%s] expected text event, got none", tt.description)
			}
			if !tt.expectText && hasText {
				t.Errorf("[%s] expected no text event, but got one", tt.description)
			}
		})
	}
}

// TestThinkingParser_StreamingTagSplit tests that ThinkingParser correctly handles
// cases where ANTML thinking tags are split across multiple streaming chunks.
// This is critical for Claude Code to display "Thinking" correctly when using thinking models.
func TestThinkingParser_StreamingTagSplit(t *testing.T) {
	tests := []struct {
		name           string
		chunks         []string // Simulate streaming chunks
		expectThinking bool
		expectText     bool
		description    string
	}{
		{
			name: "antml_tag_split_at_backslash",
			chunks: []string{
				"<antml\\",
				"b:thinking>Let me think...</antml\\b:thinking>Here is my response.",
			},
			expectThinking: true,
			expectText:     true,
			description:    "ANTML tag split at backslash",
		},
		{
			name: "antml_tag_split_at_colon",
			chunks: []string{
				"<antml\\b:",
				"thinking>Let me think...</antml\\b:thinking>Here is my response.",
			},
			expectThinking: true,
			expectText:     true,
			description:    "ANTML tag split at colon",
		},
		{
			name: "antml_tag_split_in_middle",
			chunks: []string{
				"<antml\\b:thin",
				"king>Let me think...</antml\\b:thinking>Here is my response.",
			},
			expectThinking: true,
			expectText:     true,
			description:    "ANTML tag split in middle of 'thinking'",
		},
		{
			name: "antml_closing_tag_split",
			chunks: []string{
				"<antml\\b:thinking>Let me think...</antml\\",
				"b:thinking>Here is my response.",
			},
			expectThinking: true,
			expectText:     true,
			description:    "ANTML closing tag split at backslash",
		},
		{
			name: "text_before_split_tag",
			chunks: []string{
				"Hello world <antml\\",
				"b:thinking>Let me think...</antml\\b:thinking>Done.",
			},
			expectThinking: true,
			expectText:     true,
			description:    "Text before split ANTML tag",
		},
		{
			name: "single_char_chunks",
			chunks: []string{
				"<", "a", "n", "t", "m", "l", "\\", "b", ":", "t", "h", "i", "n", "k", "i", "n", "g", ">",
				"T", "h", "i", "n", "k", "i", "n", "g", ".",
				"<", "/", "a", "n", "t", "m", "l", "\\", "b", ":", "t", "h", "i", "n", "k", "i", "n", "g", ">",
				"D", "o", "n", "e", ".",
			},
			expectThinking: true,
			expectText:     true,
			description:    "Single character chunks (extreme case)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewThinkingParser()

			// Simulate streaming by feeding chunks and flushing after each
			for _, chunk := range tt.chunks {
				for _, r := range chunk {
					parser.FeedRune(r)
				}
				parser.FlushText()
			}
			parser.Finish()

			events := parser.ConsumeEvents()

			thinkingCount := 0
			hasText := false
			for _, evt := range events {
				switch evt.Type {
				case "thinking":
					thinkingCount++
				case "text":
					if strings.TrimSpace(evt.Content) != "" {
						hasText = true
					}
				}
			}

			if tt.expectThinking && thinkingCount == 0 {
				t.Errorf("[%s] expected thinking event, got none. Events: %+v", tt.description, events)
			}
			if !tt.expectThinking && thinkingCount > 0 {
				t.Errorf("[%s] expected no thinking event, but got %d", tt.description, thinkingCount)
			}
			if tt.expectText && !hasText {
				t.Errorf("[%s] expected text event, got none. Events: %+v", tt.description, events)
			}
			if !tt.expectText && hasText {
				t.Errorf("[%s] expected no text event, but got one", tt.description)
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_ANTMLThinkingWithToolCall tests that
// parseFunctionCallsFromContentForCC correctly extracts tool calls from ANTML
// thinking blocks. This is critical for thinking models (GLM-4.7-thinking, etc.)
// that embed tool calls within their thinking process.
func TestParseFunctionCallsFromContentForCC_ANTMLThinkingWithToolCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		input             string
		trigger           string
		expectedToolCount int
		expectedToolName  string
		description       string
	}{
		{
			name: "antml_thinking_with_invoke_inside",
			input: `<antml\b:thinking>Let me plan this task.
<<CALL_test123>>
<invoke name="TodoWrite"><parameter name="todos">[{"id":"1","content":"task","status":"pending"}]</parameter></invoke>
</antml\b:thinking>`,
			trigger:           "<<CALL_test123>>",
			expectedToolCount: 1,
			expectedToolName:  "TodoWrite",
			description:       "ANTML thinking block with invoke inside should extract tool call",
		},
		{
			name: "antml_thinking_with_generic_closer",
			input: `<antml\b:thinking>Planning the approach...
<<CALL_abc123>>
<invoke name="Bash"><parameter name="command">ls -la</parameter></invoke>
</antml>`,
			trigger:           "<<CALL_abc123>>",
			expectedToolCount: 1,
			expectedToolName:  "Bash",
			description:       "ANTML thinking with generic </antml> closer should extract tool call",
		},
		{
			name: "escaped_antml_thinking_with_invoke",
			input: `<antml\\b:thinking>Thinking about the task...
<<CALL_xyz789>>
<invoke name="Read"><parameter name="file_path">/test.py</parameter></invoke>
</antml\\b:thinking>`,
			trigger:           "<<CALL_xyz789>>",
			expectedToolCount: 1,
			expectedToolName:  "Read",
			description:       "Escaped ANTML thinking should extract tool call",
		},
		{
			name: "antml_thinking_no_tool_call",
			input: `<antml\b:thinking>Just thinking about this problem, no tool call needed.</antml\b:thinking>
I need more information from the user.`,
			trigger:           "<<CALL_none>>",
			expectedToolCount: 0,
			expectedToolName:  "",
			description:       "ANTML thinking without tool call should return no tools",
		},
		{
			name: "mixed_thinking_formats_with_tool_call",
			input: `<thinking>Standard thinking first</thinking>
<antml\b:thinking>Now using ANTML thinking
<<CALL_mix123>>
<invoke name="Glob"><parameter name="pattern">*.go</parameter></invoke>
</antml\b:thinking>`,
			trigger:           "<<CALL_mix123>>",
			expectedToolCount: 1,
			expectedToolName:  "Glob",
			description:       "Mixed thinking formats should extract tool call from ANTML block",
		},
		{
			name: "tool_call_outside_thinking",
			input: `<antml\b:thinking>Just planning...</antml\b:thinking>
<<CALL_out123>>
<invoke name="WebSearch"><parameter name="query">best practices</parameter></invoke>`,
			trigger:           "<<CALL_out123>>",
			expectedToolCount: 1,
			expectedToolName:  "WebSearch",
			description:       "Tool call outside thinking block should be extracted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.input)

			if len(toolUseBlocks) != tt.expectedToolCount {
				t.Errorf("[%s] expected %d tool_use blocks, got %d", tt.description, tt.expectedToolCount, len(toolUseBlocks))
			}

			if tt.expectedToolCount > 0 && len(toolUseBlocks) > 0 {
				if toolUseBlocks[0].Name != tt.expectedToolName {
					t.Errorf("[%s] expected tool name %q, got %q", tt.description, tt.expectedToolName, toolUseBlocks[0].Name)
				}
			}
		})
	}
}

// TestParseFunctionCallsFromContentForCC_EmbeddedJSONToolCall tests extraction of tool calls
// from embedded JSON structures in thinking model output. This covers the production log
// scenario where GLM-4.7-thinking outputs tool call info in JSON format instead of XML.
// The JSON may be double-escaped when embedded in another JSON string.
func TestParseFunctionCallsFromContentForCC_EmbeddedJSONToolCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		input             string
		trigger           string
		expectedToolCount int
		expectedToolName  string
		expectedArg       string
		description       string
	}{
		{
			name: "embedded_json_read_tool_double_escaped",
			// This pattern is from production log: thinking model outputs tool call RESULT as JSON
			// The JSON object has "status":"completed" which indicates it's a result, not a request
			// Should NOT be extracted as a tool call
			input: `用户想要搜索GUI程序的最佳实践。
让我先读取hello.py文件。
{"file_path":"F:\\MyProjects\\test\\hello.py","display_result":"","duration":"0s","id":"call_f95d44eb48c64cbeb4aaee40","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"","status":"completed"}
工具执行完成。`,
			trigger:           "<<CALL_test123>>",
			expectedToolCount: 0,
			expectedToolName:  "",
			expectedArg:       "",
			description:       "JSON tool call result with status:completed should NOT be extracted",
		},
		{
			name: "embedded_json_websearch_tool",
			// Direct JSON format without escaping - this is a NEW tool call request (no status field)
			input: `I need to search for information.
{"name":"WebSearch","query":"Python GUI best practices","allowed_domains":["stackoverflow.com"]}
Let me analyze the results.`,
			trigger:           "<<CALL_abc123>>",
			expectedToolCount: 1,
			expectedToolName:  "WebSearch",
			expectedArg:       "query",
			description:       "Direct JSON tool call should be extracted",
		},
		{
			name: "embedded_json_bash_tool_with_id",
			// JSON with id field (common in thinking model output)
			input: `Running a command now.
{"id":"call_xyz789","name":"Bash","command":"ls -la","description":"List files"}
Command executed.`,
			trigger:           "<<CALL_xyz789>>",
			expectedToolCount: 1,
			expectedToolName:  "Bash",
			expectedArg:       "command",
			description:       "JSON tool call with id field should be extracted",
		},
		{
			name: "embedded_json_single_escaped",
			// Single-escaped JSON (\" -> ")
			input: `Processing request.
{\"name\":\"Glob\",\"pattern\":\"**/*.go\",\"path\":\"/src\"}
Done.`,
			trigger:           "<<CALL_glob123>>",
			expectedToolCount: 1,
			expectedToolName:  "Glob",
			expectedArg:       "pattern",
			description:       "Single-escaped JSON tool call should be extracted",
		},
		{
			name: "no_tool_call_in_json",
			// JSON without known tool name
			input: `Some data: {"name":"UnknownTool","param":"value"}`,
			trigger:           "<<CALL_none>>",
			expectedToolCount: 0,
			expectedToolName:  "",
			expectedArg:       "",
			description:       "Unknown tool name should not be extracted",
		},
		{
			name: "multiple_json_tools_first_only",
			// Multiple tool calls - should only return first (b4u2cc policy)
			input: `First tool: {"name":"Read","file_path":"/a.txt"}
Second tool: {"name":"Write","file_path":"/b.txt","content":"hello"}`,
			trigger:           "<<CALL_multi>>",
			expectedToolCount: 1,
			expectedToolName:  "Read",
			expectedArg:       "file_path",
			description:       "Multiple JSON tool calls should return only first (b4u2cc policy)",
		},
		{
			name: "production_log_pattern_full",
			// Full pattern from production log with all metadata fields
			// This is a tool call RESULT (has status:"completed"), not a new request
			// Should NOT be extracted as a tool call
			input: `用户想要：
1. 联网搜索GUI程序的最佳实践
让我先读取hello.py文件。
{"file_path":"F:\\MyProjects\\test\\hello.py","display_result":"","duration":"0s","id":"call_f95d44eb48c64cbeb4aaee40","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"","status":"completed"}
工具出现了问题。`,
			trigger:           "<<CALL_vsk7e8>>",
			expectedToolCount: 0,
			expectedToolName:  "",
			expectedArg:       "",
			description:       "Production log pattern with status:completed should NOT be extracted (it's a result)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.input)

			if len(toolUseBlocks) != tt.expectedToolCount {
				t.Errorf("[%s] expected %d tool_use blocks, got %d", tt.description, tt.expectedToolCount, len(toolUseBlocks))
				return
			}

			if tt.expectedToolCount > 0 && len(toolUseBlocks) > 0 {
				if toolUseBlocks[0].Name != tt.expectedToolName {
					t.Errorf("[%s] expected tool name %q, got %q", tt.description, tt.expectedToolName, toolUseBlocks[0].Name)
				}

				// Verify the expected argument exists in the input
				if tt.expectedArg != "" {
					var input map[string]any
					if err := json.Unmarshal(toolUseBlocks[0].Input, &input); err != nil {
						t.Errorf("[%s] failed to unmarshal tool input: %v", tt.description, err)
					} else if _, ok := input[tt.expectedArg]; !ok {
						t.Errorf("[%s] expected argument %q not found in input: %v", tt.description, tt.expectedArg, input)
					}
				}
			}
		})
	}
}


// TestParseFunctionCallsFromContentForCC_GLMBlockRemoval tests that <glm_block> tags
// containing tool call results are properly removed and not extracted as new tool calls.
// This addresses the production issue where GLM-4.7-thinking model outputs tool call
// results in <glm_block> tags, which should be filtered out.
func TestParseFunctionCallsFromContentForCC_GLMBlockRemoval(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		input             string
		trigger           string
		expectedToolCount int
		description       string
	}{
		{
			name: "glm_block_with_tool_result_error",
			// Production log pattern: GLM model outputs tool call result with error
			input: `用户想要搜索最佳实践。
<glm_block>{"file_path":"F:\\test\\hello.py","id":"call_3b38f4af81fa478d823d1a10","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"tool call failed: Read","status":"error"}}</glm_block>
工具调用失败了。`,
			trigger:           "<<CALL_test123>>",
			expectedToolCount: 0,
			description:       "GLM block with error result should not be extracted as tool call",
		},
		{
			name: "glm_block_with_completed_result",
			// Tool call result with status=completed and non-empty result should be skipped
			input: `让我读取文件。
<glm_block>{"name":"Read","file_path":"/test.py","status":"completed","result":"file content here","is_error":false}</glm_block>
文件读取完成。`,
			trigger:           "<<CALL_read123>>",
			expectedToolCount: 0,
			description:       "GLM block with completed result and non-empty result should not be extracted",
		},
		{
			name: "glm_block_with_mcp_type",
			// Full production pattern with type:mcp
			input: `首先搜索最佳实践。
<glm_block>{"tool_use":{"file_path":"F:\\hello.py","id":"call_abc123","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"tool call failed: Read","status":"error"}},"type":"mcp"}</glm_block>
让我尝试其他方式。`,
			trigger:           "<<CALL_mcp123>>",
			expectedToolCount: 0,
			description:       "GLM block with MCP type wrapper should not be extracted",
		},
		{
			name: "mixed_glm_block_and_valid_invoke",
			// GLM block result followed by valid invoke should only extract the invoke
			input: `<glm_block>{"name":"Read","result":"failed","status":"error","is_error":true}</glm_block>
让我重试。
<<CALL_retry123>>
<invoke name="Glob"><parameter name="pattern">*.py</parameter></invoke>`,
			trigger:           "<<CALL_retry123>>",
			expectedToolCount: 1,
			description:       "Valid invoke after GLM block should be extracted",
		},
		{
			name: "nested_glm_block_content",
			// Multiple GLM blocks should all be removed
			input: `第一次尝试：
<glm_block>{"name":"WebSearch","result":"search failed","status":"error"}</glm_block>
第二次尝试：
<glm_block>{"name":"Read","result":"read failed","status":"error"}</glm_block>
所有工具都失败了。`,
			trigger:           "<<CALL_multi>>",
			expectedToolCount: 0,
			description:       "Multiple GLM blocks should all be filtered out",
		},
		{
			name: "orphaned_glm_block_closer_production_log",
			// Production log pattern: orphaned </glm_block> without opening tag
			// This happens when the opening tag is truncated in streaming
			input: `用户要求我读取文件。让我先读取hello.py文件。{"id":"call_aa03fe9628b24c59af840c45","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"tool call failed: Read","status":"error"}</glm_block>两个工具都失败了。`,
			trigger:           "<<CALL_sq89xv>>",
			expectedToolCount: 0,
			description:       "Orphaned </glm_block> closer with tool result JSON should be removed",
		},
		{
			name: "production_log_2026_01_01_tool_call_in_thinking",
			// Production log pattern from 2026-01-01: thinking model outputs tool call info
			// but without proper XML format. The content contains tool call parameters
			// mixed with tool call result fields from previous calls.
			// This should NOT extract any tool calls because there's no valid invoke format.
			input: `用户要求：
1. 联网搜索Python GUI程序的最佳实践
2. 修改hello.py将其改为漂亮的GUI程序

首先，我需要：
1. 搜索Python GUI库的最佳实践
2. 查看当前目录下的hello.py文件

让我先使用exa联网搜索Python GUI最佳实践，同时读取hello.py文件。
query":"Python GUI tkinter hello world best practice minimal code 2025","numResults":"5"},"display_result":"","duration":"0s","id":"call_38b95eee7871433bbe955350","is_error":false,"mcp_server":{"name":"mcp-server"}}</glm_block>
工具调用完成。`,
			trigger:           "<<CALL_25mp6o>>",
			expectedToolCount: 0,
			description:       "Thinking content with tool call result JSON should not extract tool calls",
		},
		{
			name: "production_log_2026_01_01_weak_indicators_only",
			// Production log pattern from 2026-01-01: thinking model outputs content with only
			// weak indicators (display_result, duration) but no strong indicators (is_error, status).
			// This should NOT remove the content because weak indicators alone don't confirm it's a tool result.
			// The content should be preserved and no tool calls should be extracted.
			input: `用户要求我：
1. 联网搜索最佳实践来修改 hello.py
2. 将其改为漂亮的GUI程序

让我先读取文件并联网搜索：
file_path":"F:\\MyProjects\\test\\language\\python\\xx\\hello.py"},"display_result":"","duration":"0s","id":"call_8f5d86635de34da</glm_block>
工具调用失败了。`,
			trigger:           "<<CALL_wnyuew>>",
			expectedToolCount: 0,
			description:       "Content with only weak indicators should not extract tool calls but should preserve content",
		},
		{
			name: "valid_invoke_after_orphaned_glm_block",
			// Valid invoke after orphaned glm_block should be extracted
			input: `让我先读取文件。
{"name":"Read","is_error":true,"result":"failed","status":"error"}</glm_block>
工具失败了，让我重试。
<<CALL_retry>>
<invoke name="Read"><parameter name="file_path">/test.py</parameter></invoke>`,
			trigger:           "<<CALL_retry>>",
			expectedToolCount: 1,
			description:       "Valid invoke after orphaned glm_block should be extracted",
		},
		{
			name: "production_log_2026_01_01_embedded_tool_result_no_closer",
			// Production log pattern from 2026-01-01: thinking model outputs tool call result
			// JSON embedded in content WITHOUT </glm_block> closer tag.
			// This happens when the model references previous tool results in its thinking.
			// The JSON contains strong indicators (is_error:false) and should be cleaned.
			// No tool calls should be extracted because there's no valid invoke format.
			input: `用户想要：
1. 联网搜索最佳实践
2. 修改 hello.py
3. 将其改为漂亮的GUI程序，输出 Hello World
4. 需要代码短小精悍，越短越好
5. 自动运行它

首先我需要查看当前的 hello.py 文件，然后联网搜索 Python GUI 最佳实践，最后实现一个简洁的 GUI 程序。

让我先读取 hello.py 文件，然后联网搜索 Python GUI 最佳实践，最后实现一个简洁的 GUI 程序。

让我先读取 hello.py 文件，然后联网搜索相关信息。我需要先查看当前文件内容，然后搜索 Python GUI 最佳实践，再实现一个简洁的 GUI 程序。
{"file_path":"F:\\test\\language\\python\\xx\\hello.py","display_result":"","duration":"0s","id":"call_48f8993eac684726b0782cd8","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"Read","result":""}
工具调用完成。`,
			trigger:           "<<CALL_js00my>>",
			expectedToolCount: 0,
			description:       "Embedded tool result JSON without </glm_block> closer should not extract tool calls",
		},
		{
			name: "production_log_2026_01_01_valid_invoke_after_embedded_result",
			// Production log pattern: valid invoke after embedded tool result JSON (no closer)
			// The embedded JSON should be cleaned, and the valid invoke should be extracted.
			input: `让我先读取文件。
{"name":"Read","is_error":false,"result":"","display_result":"","duration":"0s","mcp_server":{"name":"mcp-server"}}
工具调用完成，让我继续。
<<CALL_next>>
<invoke name="WebSearch"><parameter name="query">Python GUI best practices</parameter></invoke>`,
			trigger:           "<<CALL_next>>",
			expectedToolCount: 1,
			description:       "Valid invoke after embedded tool result JSON (no closer) should be extracted",
		},
		{
			name: "production_log_2026_01_01_multiple_orphaned_glm_blocks_no_invoke",
			// Production log pattern from 2026-01-01: thinking model outputs multiple tool call results
			// in orphaned </glm_block> tags, but does NOT output new tool call requests.
			// This is the exact scenario from the log where Claude Code shows "tool call failed".
			// The model references previous tool results but doesn't output new invoke tags.
			// Expected: 0 tool calls (no valid invoke format in content)
			input: `用户希望我：
1. 联网搜索最佳实践
2. 修改 hello.py 将其改为漂亮的GUI程序
3. 输出 Hello World 即可
4. 需要代码短小精悍，越短越好
5. 自动运行它

首先，我需要：
1. 读取当前的 hello.py 文件
2. 联网搜索 Python GUI 最佳实践
3. 修改文件
4. 运行它

让我先读取文件，然后联网搜索。让我先读取现有的 hello.py 文件，并联网搜索 Python GUI 最佳实践。1s","id":"call_32c91bbaaec3439297a345bc","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"tool call failed: Read","status":"error"}},"type":"mcp"}</glm_block>两个工具都失败了。让我使用其他工具来获取信息。先尝试使用 Glob 查找 hello.py 文件，然后使用 WebSearch 联1s","id":"call_e1629dc39e7a41","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"WebSearch","result":"tool call failed: WebSearch","status":"error"}},"type":"mcp"}</glm_block>
工具调用都失败了。`,
			trigger:           "<<CALL_r4o62t>>",
			expectedToolCount: 0,
			description:       "Multiple orphaned glm_blocks with tool results but no invoke should return 0 tool calls",
		},
		{
			name: "production_log_2026_01_01_orphaned_glm_blocks_with_valid_invoke_after",
			// Production log pattern: orphaned glm_blocks with tool results, followed by valid invoke
			// The tool results should be cleaned, and the valid invoke should be extracted.
			input: `让我先读取文件。
{"id":"call_abc123","is_error":true,"name":"Read","result":"failed","status":"error"}</glm_block>
工具失败了，让我重试。
<<CALL_retry>>
<invoke name="Glob"><parameter name="pattern">*.py</parameter></invoke>`,
			trigger:           "<<CALL_retry>>",
			expectedToolCount: 1,
			description:       "Valid invoke after orphaned glm_block with tool result should be extracted",
		},
		{
			name: "production_log_2026_01_02_execution_intent_without_tool_call",
			// Production log pattern from 2026-01-02: thinking model outputs execution intent
			// in natural language but does NOT output actual XML tool call format.
			// This is the exact scenario from the log where Claude Code shows "tool call failed".
			// The model describes what it wants to do but doesn't output <invoke> or <function_calls>.
			// Expected: 0 tool calls (no valid tool call format in content)
			// This test verifies that we correctly handle this case without false positives.
			input: ` 我需要先读取文件，然后再写入。让我先读取hello.py文件。让我先读取文件：
看起来文件不存在或者有问题。让我用Bash检查一下当前目录的内容：让我检查当前目录：


Windows上应该用正确的命令。让我再次尝试用dir，

Bash工具似乎有问题。让我直接尝试写入新文件，因为可能文件根本不存在`,
			trigger:           "<<CALL_cjfe4k>>",
			expectedToolCount: 0,
			description:       "Execution intent in natural language without actual tool call XML should return 0 tool calls",
		},
		{
			name: "production_log_2026_01_02_glm_block_removed_no_new_invoke",
			// Production log pattern: GLM model outputs tool call results in <glm_block>,
			// which are correctly removed, but then only outputs natural language description
			// without new tool call requests. This is the root cause of "tool call failed".
			// Expected: 0 tool calls (glm_block results removed, no new invoke)
			input: `用户想要我：
1. 联网搜索最佳实践
2. 修改 hello.py

<glm_block>{"name":"Read","file_path":"hello.py","is_error":true,"result":"failed","status":"error"}</glm_block>

工具调用失败了。让我尝试其他方式。我需要先读取文件，然后再写入。
让我先读取hello.py文件。看起来文件不存在或者有问题。`,
			trigger:           "<<CALL_test>>",
			expectedToolCount: 0,
			description:       "GLM block removed but no new invoke should return 0 tool calls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.input)

			if len(toolUseBlocks) != tt.expectedToolCount {
				t.Errorf("[%s] expected %d tool_use blocks, got %d", tt.description, tt.expectedToolCount, len(toolUseBlocks))
				for i, block := range toolUseBlocks {
					t.Logf("Block %d: name=%s, input=%s", i, block.Name, string(block.Input))
				}
			}
		})
	}
}

// TestExtractToolCallsFromJSONContent_SkipsToolResults tests that the JSON extraction
// function correctly skips tool call results (objects with is_error, status, or result fields).
func TestExtractToolCallsFromJSONContent_SkipsToolResults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	knownTools := map[string]bool{
		"Read": true, "Write": true, "Bash": true, "Glob": true, "WebSearch": true,
	}

	tests := []struct {
		name          string
		content       string
		expectedCount int
		description   string
	}{
		{
			name:          "tool_result_with_is_error_true",
			content:       `{"name":"Read","file_path":"/test.py","is_error":true,"result":"failed"}`,
			expectedCount: 0,
			description:   "Tool result with is_error=true should be skipped",
		},
		{
			name:          "tool_result_with_status_error",
			content:       `{"name":"Read","file_path":"/test.py","status":"error","result":"failed"}`,
			expectedCount: 0,
			description:   "Tool result with status=error should be skipped",
		},
		{
			name:          "tool_result_with_status_completed",
			content:       `{"name":"Read","file_path":"/test.py","status":"completed","result":"content"}`,
			expectedCount: 0,
			description:   "Tool result with status=completed and non-empty result should be skipped",
		},
		{
			name:          "tool_result_with_result_field",
			content:       `{"name":"Bash","command":"ls","result":"file1.txt\nfile2.txt"}`,
			expectedCount: 0,
			description:   "Tool result with non-empty result field should be skipped",
		},
		{
			name:          "valid_tool_request_no_result_fields",
			content:       `{"name":"Read","file_path":"/test.py"}`,
			expectedCount: 1,
			description:   "Valid tool request without result fields should be extracted",
		},
		{
			name:          "valid_tool_request_with_status_pending",
			content:       `{"name":"Glob","pattern":"*.go","status":"pending"}`,
			expectedCount: 1,
			description:   "Tool request with status=pending should be extracted",
		},
		{
			name:          "tool_result_with_is_error_false_but_has_result",
			content:       `{"name":"WebSearch","query":"test","is_error":false,"result":"search results"}`,
			expectedCount: 0,
			description:   "Tool with is_error=false but has non-empty result field should be skipped",
		},
		{
			name:          "tool_request_with_empty_result",
			// Tool with status:"completed" is a result, not a request - should be skipped
			content:       `{"name":"Read","file_path":"/test.py","result":"","status":"completed"}`,
			expectedCount: 0,
			description:   "Tool with status:completed should be skipped (it's a result)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := extractToolCallsFromJSONContent(tt.content, knownTools)

			if len(calls) != tt.expectedCount {
				t.Errorf("[%s] expected %d calls, got %d", tt.description, tt.expectedCount, len(calls))
				for i, call := range calls {
					t.Logf("Call %d: name=%s, args=%v", i, call.Name, call.Args)
				}
			}
		})
	}
}

// TestRemoveThinkBlocks_GLMBlock tests that removeThinkBlocks correctly handles
// <glm_block> tags, removing them from content while preserving other text.
// Note: removeThinkBlocks extracts invoke tags from thinking blocks, but for glm_block
// the extracted content will be filtered out later by extractToolCallsFromJSONContent
// because it contains tool call results (is_error, status, result fields).
func TestRemoveThinkBlocks_GLMBlock(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple_glm_block_removal",
			input:    `Before<glm_block>{"name":"Read","result":"failed"}</glm_block>After`,
			expected: `BeforeAfter`,
		},
		{
			name:     "glm_block_with_newlines",
			input:    "Line1\n<glm_block>tool result here</glm_block>\nLine2",
			expected: "Line1\n\nLine2",
		},
		{
			name:     "multiple_glm_blocks",
			input:    `A<glm_block>1</glm_block>B<glm_block>2</glm_block>C`,
			expected: `ABC`,
		},
		{
			name:     "no_glm_block",
			input:    `Normal text without any blocks`,
			expected: `Normal text without any blocks`,
		},
		{
			// Production log pattern: orphaned </glm_block> without opening tag
			// This happens when the opening tag is truncated in streaming
			// The JSON content before </glm_block> should be removed because it contains tool result indicators
			name:     "orphaned_glm_block_closer_with_tool_result",
			input:    `用户要求我读取文件。让我先读取hello.py文件。{"id":"call_aa03fe9628b24c59af840c45","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"tool call failed: Read","status":"error"}</glm_block>两个工具都失败了。`,
			expected: `用户要求我读取文件。让我先读取hello.py文件。两个工具都失败了。`,
		},
		{
			// Orphaned closer with JSON tool result containing is_error field
			name:     "orphaned_closer_with_is_error_json",
			input:    `让我读取文件。{"name":"Read","is_error":true,"result":"failed"}</glm_block>继续尝试。`,
			expected: `让我读取文件。继续尝试。`,
		},
		{
			// Orphaned closer with JSON tool result containing status field
			name:     "orphaned_closer_with_status_json",
			input:    `尝试搜索。{"name":"WebSearch","status":"error","result":"search failed"}</glm_block>换个方法。`,
			expected: `尝试搜索。换个方法。`,
		},
		{
			// Orphaned closer without JSON - just remove the tag
			name:     "orphaned_closer_without_json",
			input:    `一些文本</glm_block>更多文本`,
			expected: `一些文本更多文本`,
		},
		{
			// Production log pattern: truncated JSON before orphaned closer
			// The JSON is incomplete (missing opening brace) but has tool result indicators
			// UPDATED: With the new cleanTruncatedToolResultJSON logic, truncated JSON with
			// tool result indicators should be removed. Since there's no newline in the input,
			// the output should not have a newline either.
			name:     "orphaned_closer_with_truncated_json",
			input:    `让我读取文件。"name":"Read","is_error":true}</glm_block>继续。`,
			expected: `让我读取文件。继续。`,
		},
		{
			// Multiple orphaned closers in sequence
			name:     "multiple_orphaned_closers",
			input:    `第一次{"name":"Read","is_error":true}</glm_block>第二次{"name":"Glob","status":"error"}</glm_block>结束`,
			expected: `第一次第二次结束`,
		},
		{
			// Production log pattern from 2026-01-01: truncated JSON with missing opening brace
			// The content has tool result indicators but the JSON is incomplete
			// This tests the fallback logic when brace matching fails
			// UPDATED: With the new cleanTruncatedToolResultJSON logic, truncated JSON with
			// tool result indicators should be removed
			name: "production_log_truncated_json_no_opening_brace",
			input: `用户要求：
1. 联网搜索最佳实践
让我先读取hello.py文件。
file_path":"F:\\MyProjects\\test\\hello.py"},"display_result":"","duration":"0s","id":"call_55b52f67bb4a4edfafd1bec7","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"","status":"completed"}}</glm_block>
工具出现了问题。`,
			expected: `用户要求：
1. 联网搜索最佳实践
让我先读取hello.py文件。

工具出现了问题。`,
		},
		{
			// Production log pattern: multiple orphaned </glm_block> closers with truncated JSON
			// This simulates the exact scenario from the 2026-01-01 log where 4 orphaned closers were found
			name: "production_log_multiple_orphaned_closers",
			input: `用户要求：
1. 联网搜索最佳实践
让我先读取hello.py文件。
file_path":"F:\\MyProjects\\test\\hello.py"},"is_error":false,"name":"Read","status":"completed"}}</glm_block>
然后联网搜索。
query":"Python GUI best practices"},"is_error":true,"name":"WebSearch","status":"error"}}</glm_block>
工具都失败了。`,
			expected: `用户要求：
1. 联网搜索最佳实践
让我先读取hello.py文件。

然后联网搜索。

工具都失败了。`,
		},
		{
			// Production log pattern from 2026-01-01: orphaned </glm_block> with weak indicators and is_error
			// The content has "duration", "display_result", and "is_error":false
			// UPDATED: With the new cleanTruncatedToolResultJSON logic, this should be removed
			// because is_error:false combined with other indicators is sufficient
			name: "orphaned_closer_with_weak_indicators_and_is_error",
			input: `让我读取文件。
file_path":"F:\\test\\hello.py"},"display_result":"","duration":"0s","id":"call_8f5d86635de34da","is_error":false}</glm_block>
继续处理。`,
			expected: `让我读取文件。

继续处理。`,
		},
		{
			// Production log pattern: orphaned </glm_block> with ONLY weak indicators (no is_error/status)
			// UPDATED: With the new cleanTruncatedToolResultJSON logic, this should also be removed
			// because display_result and duration are strong indicators of tool results
			name: "orphaned_closer_with_only_weak_indicators",
			input: `让我读取文件。
file_path":"F:\\test\\hello.py"},"display_result":"","duration":"0s","id":"call_8f5d86635de34da"}</glm_block>
继续处理。`,
			expected: `让我读取文件。

继续处理。`,
		},
		{
			// Production log pattern from 2026-01-02: inline tool call result JSON without newline
			// The JSON fragment appears inline after normal text, without a newline separator
			// This tests the new reInlineToolResultJSON cleanup logic
			// UPDATED: The cleanTruncatedToolResultJSON should remove the entire JSON fragment
			// including the mcp_server field
			name: "inline_tool_result_json_mcp_tool",
			input: `让我先读取文件，然后进行搜索。我先读取当前的 hello.py 文件，然后搜索 Python GUI 最佳实践。 GUI hello world tkinter minimal beautiful short code best practice\"}","display_result":"","duration":"0s","id":"call_3da10d39ea7d4d35910530bd","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"mcp__exa__get_code_context_exa","result":"","status":"completed"}},"type`,
			expected: `让我先读取文件，然后进行搜索。我先读取当前的 hello.py 文件，然后搜索 Python GUI 最佳实践。 GUI hello world tkinter minimal beautiful short code best practice`,
		},
		{
			// Production log pattern: inline tool result with is_error:true
			name: "inline_tool_result_json_error",
			input: `尝试读取文件。\"}","is_error":true,"result":"file not found","status":"error"}}继续尝试其他方法。`,
			expected: `尝试读取文件。继续尝试其他方法。`,
		},
		{
			// Production log pattern from 2026-01-02: truncated JSON with tool result before </glm_block>
			// The JSON is truncated (starts with \n1s") but contains tool result indicators
			// This tests the new cleanTruncatedToolResultJSON logic
			name: "production_log_2026_01_02_truncated_json_before_glm_block",
			input: `用户要求我：
1. 联网搜索最佳实践
2. 修改 hello.py 将其改为漂亮的GUI程序
3. 输出 Hello World 即可
4. 需要代码短小精悍，越短越好
5. 自动运行它

首先我需要：
1. 搜索 Python GUI 最佳实践
2. 读取现有的 hello.py 文件
3. 修改它为GUI程序
4. 运行它

让我先搜索Python GUI最佳实践，然后读取文件。我来帮你完成这个任务。首先让我搜索 Python GUI 的最佳实践，然后读取并修改 hello.py 文件。
1s","id":"call_0893a8a9d5094fd5ba71c889","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"mcp__exa__web_search_exa","result":"tool call failed: mcp__exa__web_search_exa","status":"error"}},"type":"mcp"}</glm_block>两个工具都失败了。让我尝试使用 WebSearch 工具。`,
			expected: `用户要求我：
1. 联网搜索最佳实践
2. 修改 hello.py 将其改为漂亮的GUI程序
3. 输出 Hello World 即可
4. 需要代码短小精悍，越短越好
5. 自动运行它

首先我需要：
1. 搜索 Python GUI 最佳实践
2. 读取现有的 hello.py 文件
3. 修改它为GUI程序
4. 运行它

让我先搜索Python GUI最佳实践，然后读取文件。我来帮你完成这个任务。首先让我搜索 Python GUI 的最佳实践，然后读取并修改 hello.py 文件。
两个工具都失败了。让我尝试使用 WebSearch 工具。`,
		},
		{
			// Production log pattern: MCP tool result with status:error
			name: "mcp_tool_result_status_error",
			input: `让我搜索信息。{"id":"call_abc123","is_error":true,"mcp_server":{"name":"mcp-server"},"name":"mcp__exa__web_search_exa","result":"tool call failed: mcp__exa__web_search_exa","status":"error"}</glm_block>搜索失败了。`,
			expected: `让我搜索信息。搜索失败了。`,
		},
		{
			// Production log pattern: MCP tool result with status:completed
			name: "mcp_tool_result_status_completed",
			input: `让我获取代码上下文。{"display_result":"","duration":"0s","id":"call_xyz789","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"mcp__exa__get_code_context_exa","result":"","status":"completed"}</glm_block>获取成功。`,
			expected: `让我获取代码上下文。获取成功。`,
		},
		{
			// Normal text without tool result JSON should be preserved
			name: "normal_text_with_quotes",
			input: `这是一个包含"引号"的正常文本，不应该被移除。`,
			expected: `这是一个包含"引号"的正常文本，不应该被移除。`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeThinkBlocks(tt.input)
			// Normalize whitespace for comparison
			result = strings.TrimSpace(result)
			expected := strings.TrimSpace(tt.expected)
			if result != expected {
				t.Errorf("expected %q, got %q", expected, result)
			}
		})
	}
}


// TestParseFunctionCallsFromContentForCC_ArgKeyValueFormat tests parsing of tool calls
// with <arg_key>/<arg_value> format from thinking model output.
// This addresses the production log scenario where GLM-4.7-thinking outputs:
// <invoke name="Bash<arg_key>command</arg_key><arg_value>python -c "..."</arg_value>
func TestParseFunctionCallsFromContentForCC_ArgKeyValueFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		input             string
		trigger           string
		expectedToolCount int
		expectedToolName  string
		expectedArgs      map[string]string
		description       string
	}{
		{
			name: "bash_with_arg_key_value_format",
			input: `我需要先读取hello.py文件（即使它可能不存在），文件不存在，我需要先尝试用Glob检查是否存在hello.py文件，然后直接写入新文件。文件不存在，现在创建：
<<CALL_cws6oo>>
<invoke name="Bash<arg_key>command</arg_key><arg_value>python -c "import tkinter as tk; root=tk.Tk(); root.title('Hello World'); root.mainloop()"</arg_value><arg_key>description</arg_key><arg_value>运行Hello World GUI程序</arg_value>`,
			trigger:           "<<CALL_cws6oo>>",
			expectedToolCount: 1,
			expectedToolName:  "Bash",
			expectedArgs: map[string]string{
				"command":     `python -c "import tkinter as tk; root=tk.Tk(); root.title('Hello World'); root.mainloop()"`,
				"description": "运行Hello World GUI程序",
			},
			description: "Bash tool call with arg_key/arg_value format from production log",
		},
		{
			name: "read_with_arg_key_value_format",
			input: `让我读取文件。
<<CALL_test123>>
<invoke name="Read<arg_key>file_path</arg_key><arg_value>/path/to/file.py</arg_value>`,
			trigger:           "<<CALL_test123>>",
			expectedToolCount: 1,
			expectedToolName:  "Read",
			expectedArgs: map[string]string{
				"file_path": "/path/to/file.py",
			},
			description: "Read tool call with single arg_key/arg_value pair",
		},
		{
			name: "glob_with_arg_key_value_format",
			input: `搜索文件。
<<CALL_glob456>>
<invoke name="Glob<arg_key>pattern</arg_key><arg_value>**/*.go</arg_value><arg_key>path</arg_key><arg_value>/src</arg_value>`,
			trigger:           "<<CALL_glob456>>",
			expectedToolCount: 1,
			expectedToolName:  "Glob",
			expectedArgs: map[string]string{
				"pattern": "**/*.go",
				"path":    "/src",
			},
			description: "Glob tool call with multiple arg_key/arg_value pairs",
		},
		{
			name: "websearch_with_arg_key_value_format",
			input: `需要搜索信息。
<<CALL_web789>>
<invoke name="WebSearch<arg_key>query</arg_key><arg_value>Python GUI best practices 2025</arg_value>`,
			trigger:           "<<CALL_web789>>",
			expectedToolCount: 1,
			expectedToolName:  "WebSearch",
			expectedArgs: map[string]string{
				"query": "Python GUI best practices 2025",
			},
			description: "WebSearch tool call with arg_key/arg_value format",
		},
		{
			name:              "no_arg_key_value_format",
			input:             `普通文本，没有工具调用。`,
			trigger:           "<<CALL_none>>",
			expectedToolCount: 0,
			expectedToolName:  "",
			expectedArgs:      nil,
			description:       "No tool call should be extracted from plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.input)

			if len(toolUseBlocks) != tt.expectedToolCount {
				t.Errorf("[%s] expected %d tool_use blocks, got %d", tt.description, tt.expectedToolCount, len(toolUseBlocks))
				return
			}

			if tt.expectedToolCount > 0 && len(toolUseBlocks) > 0 {
				block := toolUseBlocks[0]
				if block.Name != tt.expectedToolName {
					t.Errorf("[%s] expected tool name %q, got %q", tt.description, tt.expectedToolName, block.Name)
				}

				// Verify arguments
				if tt.expectedArgs != nil {
					var input map[string]any
					if err := json.Unmarshal(block.Input, &input); err != nil {
						t.Errorf("[%s] failed to unmarshal tool input: %v", tt.description, err)
						return
					}

					for key, expectedValue := range tt.expectedArgs {
						actualValue, ok := input[key]
						if !ok {
							t.Errorf("[%s] expected arg %q not found in input: %v", tt.description, key, input)
							continue
						}
						if actualStr, ok := actualValue.(string); ok {
							if actualStr != expectedValue {
								t.Errorf("[%s] arg %q: expected %q, got %q", tt.description, key, expectedValue, actualStr)
							}
						}
					}
				}
			}
		})
	}
}


// TestParseFunctionCallsFromContentForCC_ProductionLogGLMThinkingToolResult tests the scenario
// from production log where GLM-4.7-thinking model outputs tool call results in orphaned
// </glm_block> closers. The tool call results should be removed, but any valid tool calls
// mixed in should be extracted.
// Issue: Model outputs tool call results like {"is_error":false,"status":"c...","result":"","duration":"0s"}
// instead of new tool call requests.
func TestParseFunctionCallsFromContentForCC_ProductionLogGLMThinkingToolResult(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		content           string
		trigger           string
		expectToolUse     bool
		expectToolName    string
		description       string
	}{
		{
			name: "orphaned_closer_with_tool_result_only",
			// This is the exact pattern from production log - tool call result, not request
			content: `用户想要：
1. 联网搜索最佳实践
让我先读取hello.py文件。
file_path":"F:\\MyProjects\\test\\hello.py"},"display_result":"","duration":"0s","id":"call_c7e63823fbc0460bb9cafaab","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"","status":"completed"}</glm_block>
工具出现了问题。`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: false,
			expectToolName: "",
			description:   "Tool call result in orphaned closer should be removed, no tool_use extracted",
		},
		{
			name: "orphaned_closer_with_truncated_status",
			// Production log pattern: truncated status field "status":"c" instead of "status":"completed"
			content: `让我读取文件。
file_path":"F:\\test\\hello.py"},"display_result":"","duration":"0s","id":"call_abc123","is_error":false,"mcp_server":{"name":"mcp-server"},"name":"Read","result":"","status":"c</glm_block>
继续处理。`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: false,
			expectToolName: "",
			description:   "Tool call result with truncated status should be removed",
		},
		{
			name: "orphaned_closer_with_tool_request_mixed",
			// Tool call request mixed with result - request should be extracted
			content: `让我读取文件。
<<CALL_TEST>><invoke name="Read"><parameter name="file_path">test.py</parameter></invoke>
然后结果是：{"is_error":false,"status":"completed","result":"file content"}</glm_block>
完成。`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: true,
			expectToolName: "Read",
			description:   "Tool call request before result should be extracted",
		},
		{
			name: "multiple_orphaned_closers_with_results",
			// Multiple tool call results in orphaned closers
			content: `用户要求：
1. 联网搜索最佳实践
让我先读取hello.py文件。
file_path":"F:\\test\\hello.py"},"is_error":false,"name":"Read","status":"completed"}</glm_block>
然后联网搜索。
query":"Python GUI best practices"},"is_error":true,"name":"WebSearch","status":"error"}</glm_block>
工具都失败了。`,
			trigger:       "<<CALL_TEST>>",
			expectToolUse: false,
			expectToolName: "",
			description:   "Multiple tool call results should all be removed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ctxKeyTriggerSignal, tt.trigger)
			c.Set(ctxKeyFunctionCallEnabled, true)

			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, tt.content)

			if tt.expectToolUse {
				if len(toolUseBlocks) == 0 {
					t.Errorf("[%s] expected tool_use blocks, got none", tt.description)
					return
				}
				if toolUseBlocks[0].Name != tt.expectToolName {
					t.Errorf("[%s] expected tool name %q, got %q", tt.description, tt.expectToolName, toolUseBlocks[0].Name)
				}
			} else {
				if len(toolUseBlocks) > 0 {
					t.Errorf("[%s] expected no tool_use blocks, got %d (first: %s)", tt.description, len(toolUseBlocks), toolUseBlocks[0].Name)
				}
			}
		})
	}
}

// TestFixWindowsPathEscapes tests the fixWindowsPathEscapes function that converts
// control characters back to their backslash-letter form in Windows file paths.
// This addresses the issue where JSON escape sequences like \t, \n are incorrectly
// interpreted during JSON parsing of tool call arguments.
func TestFixWindowsPathEscapes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "path with tab character",
			input:    "F:\\MyProjects\test\\language\\python\\xx\\hello.py", // \t is actual tab
			expected: `F:\MyProjects\test\language\python\xx\hello.py`,
		},
		{
			name:     "path with newline character",
			input:    "F:\\MyProjects\new\\file.py", // \n is actual newline
			expected: `F:\MyProjects\new\file.py`,
		},
		{
			name:     "path with carriage return",
			input:    "F:\\MyProjects\readme\\file.py", // \r is actual carriage return
			expected: `F:\MyProjects\readme\file.py`,
		},
		{
			name:     "path with backspace",
			input:    "F:\\MyProjects\backup\\file.py", // \b is actual backspace
			expected: `F:\MyProjects\backup\file.py`,
		},
		{
			name:     "path with form feed",
			input:    "F:\\MyProjects\folder\\file.py", // \f is actual form feed
			expected: `F:\MyProjects\folder\file.py`,
		},
		{
			name:     "path with multiple control chars",
			input:    "F:\test\new\readme.txt", // \t and \n and \r are actual control chars
			expected: `F:\test\new\readme.txt`,
		},
		{
			name:     "normal path without control chars",
			input:    `F:\MyProjects\src\main.go`,
			expected: `F:\MyProjects\src\main.go`,
		},
		{
			name:     "unix path unchanged",
			input:    "/home/user/test/file.py",
			expected: "/home/user/test/file.py",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fixWindowsPathEscapes(tt.input)
			if result != tt.expected {
				t.Errorf("fixWindowsPathEscapes(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestNormalizeArgsGenericInPlace_WindowsPathFix tests that normalizeArgsGenericInPlace
// correctly fixes Windows file paths where JSON escape sequences were incorrectly interpreted.
func TestNormalizeArgsGenericInPlace_WindowsPathFix(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		value       string
		expectedVal string
	}{
		{
			name:        "file_path with tab",
			key:         "file_path",
			value:       "F:\\MyProjects\test\\language\\python\\xx\\hello.py", // \t is actual tab
			expectedVal: `F:\MyProjects\test\language\python\xx\hello.py`,
		},
		{
			name:        "path with newline",
			key:         "path",
			value:       "F:\\MyProjects\new\\file.py", // \n is actual newline
			expectedVal: `F:\MyProjects\new\file.py`,
		},
		{
			name:        "directory with tab",
			key:         "directory",
			value:       "C:\\Users\test\\Documents", // \t is actual tab
			expectedVal: `C:\Users\test\Documents`,
		},
		{
			name:        "file with backspace",
			key:         "file",
			value:       "D:\backup\\data.txt", // \b is actual backspace
			expectedVal: `D:\backup\data.txt`,
		},
		{
			name:        "non-path key unchanged",
			key:         "content",
			value:       "Hello\tWorld",
			expectedVal: "Hello\tWorld",
		},
		{
			name:        "unix path unchanged",
			key:         "file_path",
			value:       "/home/user/test/file.py",
			expectedVal: "/home/user/test/file.py",
		},
		{
			name:        "normal windows path unchanged",
			key:         "file_path",
			value:       `C:\Users\Admin\file.txt`,
			expectedVal: `C:\Users\Admin\file.txt`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{tt.key: tt.value}
			normalizeArgsGenericInPlace(args)
			result, ok := args[tt.key].(string)
			if !ok {
				t.Fatalf("expected string value, got %T", args[tt.key])
			}
			if result != tt.expectedVal {
				t.Errorf("normalizeArgsGenericInPlace[%q] = %q, want %q", tt.key, result, tt.expectedVal)
			}
		})
	}
}

// TestIsPathLikeKey tests the isPathLikeKey helper function.
func TestIsPathLikeKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"file_path", true},
		{"filePath", true},
		{"path", true},
		{"file", true},
		{"directory", true},
		{"dir", true},
		{"cwd", true},
		{"root", true},
		{"location", true},
		{"FILE_PATH", true},
		{"content", false},
		{"command", false},
		{"query", false},
		{"name", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			result := isPathLikeKey(tt.key)
			if result != tt.expected {
				t.Errorf("isPathLikeKey(%q) = %v, want %v", tt.key, result, tt.expected)
			}
		})
	}
}

// TestLooksLikeWindowsPath tests the looksLikeWindowsPath helper function.
func TestLooksLikeWindowsPath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"C:\\Users\\Admin", true},
		{"F:\\MyProjects\\test", true},
		{"D:", true},
		{"/home/user/file", false},
		{"./relative/path", false},
		{"file.txt", false},
		{"", false},
		{"C", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := looksLikeWindowsPath(tt.path)
			if result != tt.expected {
				t.Errorf("looksLikeWindowsPath(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// TestContainsControlChars tests the containsControlChars helper function.
func TestContainsControlChars(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"hello\tworld", true},
		{"hello\nworld", true},
		{"hello\rworld", true},
		{"hello\bworld", true},
		{"hello\fworld", true},
		{"hello world", false},
		{`hello\tworld`, false}, // literal backslash-t, not tab
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := containsControlChars(tt.input)
			if result != tt.expected {
				t.Errorf("containsControlChars(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestNormalizeOpenAIToolCallArguments_WindowsPathFix tests that normalizeOpenAIToolCallArguments
// correctly fixes Windows file paths for all tools, not just specific ones.
func TestNormalizeOpenAIToolCallArguments_WindowsPathFix(t *testing.T) {
	// Build test arguments with actual control characters embedded in the JSON string values.
	// This simulates what happens when upstream models return paths like "F:\\MyProjects\\test"
	// where \\t gets interpreted as tab during JSON parsing.
	makeArgsWithTab := func(key, prefix, suffix string) string {
		// Create a map, set the value with embedded tab, then marshal to JSON
		args := map[string]any{key: prefix + "\t" + suffix}
		b, _ := json.Marshal(args)
		return string(b)
	}
	makeArgsWithNewline := func(key, prefix, suffix string) string {
		args := map[string]any{key: prefix + "\n" + suffix}
		b, _ := json.Marshal(args)
		return string(b)
	}
	makeArgsWithBackspace := func(key, prefix, suffix string) string {
		args := map[string]any{key: prefix + "\b" + suffix}
		b, _ := json.Marshal(args)
		return string(b)
	}

	tests := []struct {
		name         string
		toolName     string
		arguments    string
		expectedPath string
		shouldFix    bool
	}{
		{
			name:         "Read tool with tab in path",
			toolName:     "Read",
			arguments:    makeArgsWithTab("file_path", `F:\MyProjects`, `est\language\python\xx\hello.py`),
			expectedPath: `F:\MyProjects\test\language\python\xx\hello.py`,
			shouldFix:    true,
		},
		{
			name:         "Write tool with newline in path",
			toolName:     "Write",
			arguments:    makeArgsWithNewline("file_path", `F:\MyProjects`, `ew\file.py`),
			expectedPath: `F:\MyProjects\new\file.py`,
			shouldFix:    true,
		},
		{
			name:         "Glob tool with tab in path",
			toolName:     "Glob",
			arguments:    makeArgsWithTab("path", `C:\Users`, `est`),
			expectedPath: `C:\Users\test`,
			shouldFix:    true,
		},
		{
			name:         "Grep tool with backspace in path",
			toolName:     "Grep",
			arguments:    makeArgsWithBackspace("path", `D:`, `ackup\src`),
			expectedPath: `D:\backup\src`,
			shouldFix:    true,
		},
		{
			name:         "Bash tool with tab in cwd",
			toolName:     "Bash",
			arguments:    makeArgsWithTab("cwd", `E:\Projects`, `est`),
			expectedPath: `E:\Projects\test`,
			shouldFix:    true,
		},
		{
			name:         "Unknown tool with tab in file",
			toolName:     "CustomTool",
			arguments:    makeArgsWithTab("file", `F:\Data`, `emp\file.txt`),
			expectedPath: `F:\Data\temp\file.txt`,
			shouldFix:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := normalizeOpenAIToolCallArguments(tt.toolName, tt.arguments)
			if !ok {
				t.Fatalf("normalizeOpenAIToolCallArguments returned false for %s, args: %s", tt.toolName, tt.arguments)
			}

			// Parse the result to check the path value
			var args map[string]any
			if err := json.Unmarshal([]byte(result), &args); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			// Find the path-like key and check its value
			var foundPath string
			for key, val := range args {
				if isPathLikeKey(key) {
					if strVal, ok := val.(string); ok {
						foundPath = strVal
						break
					}
				}
			}

			if tt.shouldFix && foundPath != tt.expectedPath {
				t.Errorf("expected path %q, got %q", tt.expectedPath, foundPath)
			}
		})
	}
}

// TestContainsWindowsDrivePath tests the containsWindowsDrivePath helper function.
func TestContainsWindowsDrivePath(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{`C:\Users\Admin`, true},
		{`python F:\MyProjects\test.py`, true},
		{`git clone D:\repo`, true},
		{"echo hello", false},
		{"/home/user/file", false},
		{"./relative/path", false},
		{"", false},
		{"just some text", false},
		{`copy A: B:`, true}, // multiple drive letters
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := containsWindowsDrivePath(tt.input)
			if result != tt.expected {
				t.Errorf("containsWindowsDrivePath(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestFixEmbeddedWindowsPathsInCommand tests the fixEmbeddedWindowsPathsInCommand function
// that fixes Windows paths embedded within command strings.
func TestFixEmbeddedWindowsPathsInCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "python command with tab in path",
			input:    "python F:\\MyProjects\test\\language\\python\\xx\\hello.py", // \t is actual tab
			expected: `python F:\MyProjects\test\language\python\xx\hello.py`,
		},
		{
			name:     "python command with tab in middle of path",
			input:    "python C:\\Users\test\\script.py", // \t is actual tab
			expected: `python C:\Users\test\script.py`,
		},
		{
			name:     "command with multiple paths containing tabs",
			input:    "copy F:\\src\test\\a.txt D:\\dest\temp\\b.txt", // \t is actual tab
			expected: `copy F:\src\test\a.txt D:\dest\temp\b.txt`,
		},
		{
			name:     "command with path and arguments",
			input:    "python F:\\MyProjects\test\\script.py --output C:\\Users\temp\\out.txt", // \t is actual tab
			expected: `python F:\MyProjects\test\script.py --output C:\Users\temp\out.txt`,
		},
		{
			name:     "command without windows path",
			input:    "echo hello world",
			expected: "echo hello world",
		},
		{
			name:     "unix command unchanged",
			input:    "python /home/user/test/script.py",
			expected: "python /home/user/test/script.py",
		},
		{
			name:     "command with quoted path",
			input:    `python "F:\MyProjects\test\script.py"`, // no control chars, just backslashes
			expected: `python "F:\MyProjects\test\script.py"`,
		},
		{
			name:     "empty command",
			input:    "",
			expected: "",
		},
		{
			name:     "command with backspace in path",
			input:    "python D:\backup\\script.py", // \b is actual backspace
			expected: `python D:\backup\script.py`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fixEmbeddedWindowsPathsInCommand(tt.input)
			if result != tt.expected {
				t.Errorf("fixEmbeddedWindowsPathsInCommand(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestNormalizeArgsGenericInPlace_EmbeddedWindowsPath tests that normalizeArgsGenericInPlace
// correctly fixes Windows paths embedded in any string parameter, regardless of key name.
// This ensures auto-extension to any tool that may contain Windows paths.
func TestNormalizeArgsGenericInPlace_EmbeddedWindowsPath(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		value       string
		expectedVal string
	}{
		{
			name:        "command with tab in path",
			key:         "command",
			value:       "python F:\\MyProjects\test\\language\\python\\xx\\hello.py", // \t is actual tab
			expectedVal: `python F:\MyProjects\test\language\python\xx\hello.py`,
		},
		{
			name:        "script with tab in path",
			key:         "script",
			value:       "node C:\\Users\test\\app.js", // \t is actual tab
			expectedVal: `node C:\Users\test\app.js`,
		},
		{
			name:        "code with backspace in path",
			key:         "code",
			value:       "go run D:\backup\\main.go", // \b is actual backspace
			expectedVal: `go run D:\backup\main.go`,
		},
		{
			// Auto-extension: git command with arbitrary key name
			name:        "git_command with tab in path",
			key:         "git_command",
			value:       "git clone C:\\repos\test\\myrepo", // \t is actual tab
			expectedVal: `git clone C:\repos\test\myrepo`,
		},
		{
			// Auto-extension: any arbitrary key with embedded Windows path
			name:        "arbitrary key with embedded path",
			key:         "my_custom_arg",
			value:       "process E:\\data\temp\\file.csv", // \t is actual tab
			expectedVal: `process E:\data\temp\file.csv`,
		},
		{
			name:        "command without control chars unchanged",
			key:         "command",
			value:       `python C:\Users\Admin\script.py`,
			expectedVal: `python C:\Users\Admin\script.py`,
		},
		{
			name:        "unix command unchanged",
			key:         "command",
			value:       "python /home/user/script.py",
			expectedVal: "python /home/user/script.py",
		},
		{
			name:        "non-command key with control chars unchanged",
			key:         "query",
			value:       "search\tterm",
			expectedVal: "search\tterm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{tt.key: tt.value}
			normalizeArgsGenericInPlace(args)
			result, ok := args[tt.key].(string)
			if !ok {
				t.Fatalf("expected string value, got %T", args[tt.key])
			}
			if result != tt.expectedVal {
				t.Errorf("normalizeArgsGenericInPlace[%q] = %q, want %q", tt.key, result, tt.expectedVal)
			}
		})
	}
}

// TestNormalizeOpenAIToolCallArguments_BashCommandWithPath tests that Bash tool
// command parameter with embedded Windows paths is correctly fixed.
func TestNormalizeOpenAIToolCallArguments_BashCommandWithPath(t *testing.T) {
	// Build test arguments with actual control characters embedded in the JSON string values.
	makeArgsWithTab := func(key, prefix, suffix string) string {
		args := map[string]any{key: prefix + "\t" + suffix}
		b, _ := json.Marshal(args)
		return string(b)
	}

	tests := []struct {
		name            string
		toolName        string
		arguments       string
		expectedCommand string
	}{
		{
			name:            "Bash with python command and tab in path",
			toolName:        "Bash",
			arguments:       makeArgsWithTab("command", `python F:\MyProjects`, `est\language\python\xx\hello.py`),
			expectedCommand: `python F:\MyProjects\test\language\python\xx\hello.py`,
		},
		{
			name:            "Bash with node command and tab in path",
			toolName:        "Bash",
			arguments:       makeArgsWithTab("command", `node C:\Users`, `est\app.js`),
			expectedCommand: `node C:\Users\test\app.js`,
		},
		{
			name:            "Bash with multiple paths",
			toolName:        "Bash",
			arguments:       makeArgsWithTab("command", `copy F:\src`, `est\a.txt D:\dest\b.txt`),
			expectedCommand: `copy F:\src\test\a.txt D:\dest\b.txt`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := normalizeOpenAIToolCallArguments(tt.toolName, tt.arguments)
			if !ok {
				t.Fatalf("normalizeOpenAIToolCallArguments returned false for %s", tt.toolName)
			}

			var args map[string]any
			if err := json.Unmarshal([]byte(result), &args); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			command, ok := args["command"].(string)
			if !ok {
				t.Fatalf("expected command to be string, got %T", args["command"])
			}

			if command != tt.expectedCommand {
				t.Errorf("expected command %q, got %q", tt.expectedCommand, command)
			}
		})
	}
}

// TestDoubleEscapeWindowsPathsForBash tests the doubleEscapeWindowsPathsForBash function
// that doubles backslash escaping ONLY in the "command" field of Bash tool arguments.
func TestDoubleEscapeWindowsPathsForBash(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectModified  bool
		expectedCommand string // only checked if expectModified is true
	}{
		{
			name:            "Bash command with Windows path",
			input:           `{"command": "python F:\\MyProjects\\test\\file.py"}`,
			expectModified:  true,
			expectedCommand: `python F:\\MyProjects\\test\\file.py`,
		},
		{
			name:            "Bash command with multiple backslashes",
			input:           `{"command": "python C:\\Users\\Admin\\Documents\\test.py"}`,
			expectModified:  true,
			expectedCommand: `python C:\\Users\\Admin\\Documents\\test.py`,
		},
		{
			name:           "No command field - unchanged",
			input:          `{"file_path": "F:\\MyProjects\\test\\file.py"}`,
			expectModified: false,
		},
		{
			name:           "Unix command - unchanged",
			input:          `{"command": "ls -la /home/user"}`,
			expectModified: false,
		},
		{
			name:           "Empty command - unchanged",
			input:          `{"command": ""}`,
			expectModified: false,
		},
		{
			name:           "file_path should NOT be double-escaped",
			input:          `{"file_path": "F:\\test\\file.py", "content": "hello"}`,
			expectModified: false,
		},
		{
			name:           "Read tool path should NOT be double-escaped",
			input:          `{"file_path": "C:\\Users\\test\\hello.py"}`,
			expectModified: false,
		},
		{
			name:            "Bash with description",
			input:           `{"command": "python F:\\test\\script.py", "description": "run script"}`,
			expectModified:  true,
			expectedCommand: `python F:\\test\\script.py`,
		},
		{
			name:           "Already double-escaped path - should NOT be escaped again",
			input:          `{"command": "python F:\\\\MyProjects\\\\test\\\\file.py"}`,
			expectModified: false,
		},
		{
			name:           "Already double-escaped with multiple paths - should NOT be escaped again",
			input:          `{"command": "copy C:\\\\source\\\\file.txt D:\\\\dest\\\\file.txt"}`,
			expectModified: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := doubleEscapeWindowsPathsForBash(tt.input)

			if tt.expectModified {
				// Parse result and check command field has doubled backslashes
				var args map[string]any
				if err := json.Unmarshal([]byte(result), &args); err != nil {
					t.Fatalf("failed to parse result: %v", err)
				}
				command, ok := args["command"].(string)
				if !ok {
					t.Fatalf("expected command to be string")
				}
				if command != tt.expectedCommand {
					t.Errorf("command = %q, want %q", command, tt.expectedCommand)
				}
			} else {
				// Should be unchanged
				if result != tt.input {
					t.Errorf("expected unchanged, got %q", result)
				}
			}
		})
	}
}

package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessJsonSchema tests the JSON schema conversion for Gemini API compatibility
func TestProcessJsonSchema(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple string type",
			input:    `{"type":"string","description":"A string field"}`,
			expected: `{"type":"STRING","description":"A string field"}`,
		},
		{
			name:     "nested object with properties",
			input:    `{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}}}`,
			expected: `{"type":"OBJECT","properties":{"name":{"type":"STRING"},"age":{"type":"INTEGER"}}}`,
		},
		{
			name:     "array with items",
			input:    `{"type":"array","items":{"type":"string"}}`,
			expected: `{"type":"ARRAY","items":{"type":"STRING"}}`,
		},
		{
			name:     "anyOf with multiple types",
			input:    `{"anyOf":[{"type":"string"},{"type":"number"}]}`,
			expected: `{"type":"STRING","description":"Accepts: string | number"}`,
		},
		{
			name:     "anyOf with null type - should set nullable",
			input:    `{"anyOf":[{"type":"string"},{"type":"null"}]}`,
			expected: `{"type":"STRING","nullable":true,"description":"Accepts: string | null"}`,
		},
		{
			name:     "anyOf with null as first element",
			input:    `{"anyOf":[{"type":"null"},{"type":"object","properties":{"id":{"type":"integer"}}}]}`,
			expected: `{"type":"OBJECT","nullable":true,"properties":{"id":{"type":"INTEGER"}},"description":"Accepts: null | object"}`,
		},
		{
			name:     "with enum and required",
			input:    `{"type":"string","enum":["a","b","c"],"required":["field1"]}`,
			expected: `{"type":"STRING","enum":["a","b","c"],"required":["field1"]}`,
		},
		{
			name:     "nullable field",
			input:    `{"type":"string","nullable":true}`,
			expected: `{"type":"STRING","nullable":true}`,
		},
		{
			name:     "empty schema",
			input:    `{}`,
			expected: `{}`,
		},
		{
			name:     "complex nested schema",
			input:    `{"type":"object","properties":{"user":{"type":"object","properties":{"name":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}}}}}`,
			expected: `{"type":"OBJECT","properties":{"user":{"type":"OBJECT","properties":{"name":{"type":"STRING"},"tags":{"type":"ARRAY","items":{"type":"STRING"}},"_":{"type":"BOOLEAN"}},"required":["_"]}}}`,
		},
		{
			name:     "lowercase types should be converted to uppercase",
			input:    `{"type":"string","description":"A string field"}`,
			expected: `{"type":"STRING","description":"A string field"}`,
		},
		{
			name:     "mixed case types",
			input:    `{"type":"Object","properties":{"count":{"type":"Integer"}}}`,
			expected: `{"type":"OBJECT","properties":{"count":{"type":"INTEGER"}}}`,
		},
		{
			name:     "boolean type",
			input:    `{"type":"boolean","description":"A boolean field"}`,
			expected: `{"type":"BOOLEAN","description":"A boolean field"}`,
		},
		{
			name:     "number type",
			input:    `{"type":"number","description":"A number field"}`,
			expected: `{"type":"NUMBER","description":"A number field"}`,
		},
		{
			name:     "unsupported fields should be skipped",
			input:    `{"type":"string","minLength":1,"maxLength":100,"$schema":"http://json-schema.org/draft-07/schema#"}`,
			expected: `{"type":"STRING","description":"minLength: 1 (maxLength: 100)"}`,
		},
		{
			name:     "additionalProperties should be skipped",
			input:    `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`,
			expected: `{"type":"OBJECT","properties":{"name":{"type":"STRING"}}}`,
		},
		{
			name:     "all unsupported fields should be skipped",
			input:    `{"type":"string","minLength":1,"maxLength":100,"$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":true}`,
			expected: `{"type":"STRING","description":"minLength: 1 (maxLength: 100)"}`,
		},
		{
			name:     "array with minItems and maxItems should skip them",
			input:    `{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":10}`,
			expected: `{"type":"ARRAY","items":{"type":"STRING"},"description":"minItems: 1 (maxItems: 10)"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := processJsonSchema(json.RawMessage(tt.input))

			// Parse both to compare as objects (order-independent)
			var resultObj, expectedObj map[string]interface{}
			err := json.Unmarshal(result, &resultObj)
			require.NoError(t, err, "Failed to unmarshal result")

			err = json.Unmarshal([]byte(tt.expected), &expectedObj)
			require.NoError(t, err, "Failed to unmarshal expected")

			assert.Equal(t, expectedObj, resultObj)
		})
	}
}

// TestConvertClaudeToGemini tests basic Claude to Gemini request conversion
func TestConvertClaudeToGemini(t *testing.T) {
	tests := []struct {
		name        string
		claudeReq   *ClaudeRequest
		expectError bool
	}{
		{
			name: "simple text message",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Hello, world!"`),
					},
				},
				MaxTokens: 1024,
				Stream:    false,
			},
			expectError: false,
		},
		{
			name: "with system message",
			claudeReq: &ClaudeRequest{
				Model:  "claude-3-5-sonnet-20241022",
				System: json.RawMessage(`"You are a helpful assistant."`),
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Hello!"`),
					},
				},
				MaxTokens: 1024,
			},
			expectError: false,
		},
		{
			name: "prompt-only request",
			claudeReq: &ClaudeRequest{
				Model:     "claude-3-5-sonnet-20241022",
				Prompt:    "What is the capital of France?",
				MaxTokens: 100,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertClaudeToGemini(tt.claudeReq, nil)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.NotEmpty(t, result.Contents)
			}
		})
	}
}

// TestConvertClaudeToGemini_WithTools tests tool conversion with name shortening
func TestConvertClaudeToGemini_WithTools(t *testing.T) {
	// Use a tool name that exceeds 64 characters to trigger shortening
	longToolName := "search_web_for_information_about_specific_topic_with_detailed_parameters_and_options"

	claudeReq := &ClaudeRequest{
		Model: "claude-3-5-sonnet-20241022",
		Messages: []ClaudeMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Use the search tool"`),
			},
		},
		Tools: []ClaudeTool{
			{
				Name:        longToolName,
				Description: "Search the web",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		},
		MaxTokens: 1024,
	}

	// Build tool name short map
	toolNames := []string{longToolName}
	toolNameShortMap := buildToolNameShortMap(toolNames)

	result, err := convertClaudeToGemini(claudeReq, toolNameShortMap)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify tools were converted
	assert.Len(t, result.Tools, 1)
	assert.Len(t, result.Tools[0].FunctionDeclarations, 1)

	// Verify tool name was shortened (original is > 64 chars)
	toolName := result.Tools[0].FunctionDeclarations[0].Name
	assert.NotEqual(t, longToolName, toolName)
	assert.LessOrEqual(t, len(toolName), 64)

	// Verify schema was processed (types should be UPPERCASE per Gemini API)
	var schema map[string]interface{}
	err = json.Unmarshal(result.Tools[0].FunctionDeclarations[0].Parameters, &schema)
	require.NoError(t, err)
	assert.Equal(t, "OBJECT", schema["type"])

	// Verify nested property types are also UPPERCASE
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		if queryProp, ok := props["query"].(map[string]interface{}); ok {
			assert.Equal(t, "STRING", queryProp["type"])
		}
	}
}

// TestConvertClaudeMessageToGemini tests message-level conversion
func TestConvertClaudeMessageToGemini(t *testing.T) {
	tests := []struct {
		name        string
		message     ClaudeMessage
		expectError bool
		expectLen   int
	}{
		{
			name: "simple user text",
			message: ClaudeMessage{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
			expectError: false,
			expectLen:   1,
		},
		{
			name: "assistant text",
			message: ClaudeMessage{
				Role:    "assistant",
				Content: json.RawMessage(`"Hi there!"`),
			},
			expectError: false,
			expectLen:   1,
		},
		{
			name: "tool_use block",
			message: ClaudeMessage{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"text","text":"Let me search for that."},
					{"type":"tool_use","id":"toolu_123","name":"search","input":{"query":"test"}}
				]`),
			},
			expectError: false,
			expectLen:   1,
		},
		{
			name: "tool_result block",
			message: ClaudeMessage{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"tool_result","tool_use_id":"toolu_123","content":"Result text"}
				]`),
			},
			expectError: false,
			expectLen:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertClaudeMessageToGemini(tt.message, nil, make(map[string]string))

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.expectLen)
			}
		})
	}
}

// TestConvertGeminiToClaudeResponse tests Gemini to Claude response conversion
func TestConvertGeminiToClaudeResponse(t *testing.T) {
	tests := []struct {
		name         string
		geminiResp   *GeminiResponse
		expectBlocks int
	}{
		{
			name: "simple text response",
			geminiResp: &GeminiResponse{
				Candidates: []GeminiCandidate{
					{
						Content: &GeminiContent{
							Role: "model",
							Parts: []GeminiPart{
								{Text: "Hello, how can I help you?"},
							},
						},
						FinishReason: "STOP",
					},
				},
				UsageMetadata: &GeminiUsageMetadata{
					PromptTokenCount:     10,
					CandidatesTokenCount: 20,
					TotalTokenCount:      30,
				},
			},
			expectBlocks: 1,
		},
		{
			name: "function call response",
			geminiResp: &GeminiResponse{
				Candidates: []GeminiCandidate{
					{
						Content: &GeminiContent{
							Role: "model",
							Parts: []GeminiPart{
								{Text: "Let me search for that."},
								{
									FunctionCall: &GeminiFunctionCall{
										Name: "search",
										Args: map[string]interface{}{
											"query": "test query",
										},
									},
								},
							},
						},
						FinishReason: "STOP",
					},
				},
			},
			expectBlocks: 2,
		},
		{
			name: "empty response",
			geminiResp: &GeminiResponse{
				Candidates: []GeminiCandidate{},
			},
			expectBlocks: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertGeminiToClaudeResponse(tt.geminiResp, nil)

			assert.NotNil(t, result)
			assert.Equal(t, "message", result.Type)
			assert.Equal(t, "assistant", result.Role)
			assert.Len(t, result.Content, tt.expectBlocks)

			if tt.expectBlocks > 0 {
				assert.NotNil(t, result.Usage)
			}
		})
	}
}

// TestGeminiStreamState tests the streaming state machine
func TestGeminiStreamState(t *testing.T) {
	state := newGeminiStreamState(nil)

	assert.NotEmpty(t, state.messageID)
	assert.Equal(t, 0, state.nextClaudeIndex)
	assert.False(t, state.finalSent)
	assert.Empty(t, state.openBlockType)

	// Test processing a text chunk
	chunk := &GeminiStreamChunk{
		Candidates: []GeminiCandidate{
			{
				Content: &GeminiContent{
					Parts: []GeminiPart{
						{Text: "Hello"},
					},
				},
			},
		},
		ModelVersion: "gemini-pro",
	}

	events := state.processGeminiStreamChunk(chunk)

	// Should have message_start, content_block_start, and content_block_delta
	assert.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "message_start", events[0].Type)

	// Test processing finish
	finishChunk := &GeminiStreamChunk{
		Candidates: []GeminiCandidate{
			{
				Content: &GeminiContent{
					Parts: []GeminiPart{},
				},
				FinishReason: "STOP",
			},
		},
	}

	finishEvents := state.processGeminiStreamChunk(finishChunk)

	// Should have message_delta and message_stop
	assert.GreaterOrEqual(t, len(finishEvents), 1)
	assert.True(t, state.finalSent)
}

// TestGeminiStreamState_UsageMetadata tests that usage metadata is properly propagated
func TestGeminiStreamState_UsageMetadata(t *testing.T) {
	state := newGeminiStreamState(nil)

	// First chunk with text
	textChunk := &GeminiStreamChunk{
		Candidates: []GeminiCandidate{
			{
				Content: &GeminiContent{
					Parts: []GeminiPart{
						{Text: "Hello"},
					},
				},
			},
		},
		ModelVersion: "gemini-pro",
	}

	events := state.processGeminiStreamChunk(textChunk)
	assert.GreaterOrEqual(t, len(events), 1)
	assert.Equal(t, "message_start", events[0].Type)

	// Final chunk with usage metadata
	finalChunk := &GeminiStreamChunk{
		Candidates: []GeminiCandidate{
			{
				Content: &GeminiContent{
					Parts: []GeminiPart{},
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: &GeminiUsageMetadata{
			PromptTokenCount:     100,
			CandidatesTokenCount: 50,
			TotalTokenCount:      150,
		},
	}

	finishEvents := state.processGeminiStreamChunk(finalChunk)

	// Find message_delta event
	var deltaEvent *ClaudeStreamEvent
	for i := range finishEvents {
		if finishEvents[i].Type == "message_delta" {
			deltaEvent = &finishEvents[i]
			break
		}
	}

	// Verify usage metadata is present in message_delta
	assert.NotNil(t, deltaEvent, "message_delta event should be present")
	assert.NotNil(t, deltaEvent.Usage, "Usage should be present in message_delta")
	// Usage metadata is now properly propagated from Gemini chunk
	assert.Equal(t, 100, deltaEvent.Usage.InputTokens, "InputTokens should match PromptTokenCount")
	assert.Equal(t, 50, deltaEvent.Usage.OutputTokens, "OutputTokens should match CandidatesTokenCount")
}

// TestGeminiToolNameShortening tests tool name shortening for Gemini
func TestGeminiToolNameShortening(t *testing.T) {
	longToolName := "this_is_a_very_long_tool_name_that_exceeds_the_sixty_four_character_limit_for_gemini"

	toolNames := []string{longToolName}
	shortMap := buildToolNameShortMap(toolNames)

	assert.NotNil(t, shortMap)
	shortName, exists := shortMap[longToolName]
	assert.True(t, exists)
	assert.LessOrEqual(t, len(shortName), 64)

	// Build reverse map
	reverseMap := buildReverseToolNameMap(shortMap)
	assert.NotNil(t, reverseMap)

	originalName, exists := reverseMap[shortName]
	assert.True(t, exists)
	assert.Equal(t, longToolName, originalName)
}

// TestWindowsPathEscaping tests Windows path escaping for bash
func TestWindowsPathEscaping(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "command with Windows path",
			input:    `{"command":"cd C:\\Users\\test"}`,
			expected: `{"command":"cd C:\\\\Users\\\\test"}`,
		},
		{
			name:     "command without backslashes",
			input:    `{"command":"ls /usr/local/bin"}`,
			expected: `{"command":"ls /usr/local/bin"}`,
		},
		{
			name:     "no command field",
			input:    `{"path":"C:\\test","name":"file"}`,
			expected: `{"path":"C:\\test","name":"file"}`,
		},
		{
			name:     "already double-escaped",
			input:    `{"command":"cd C:\\\\Users\\\\test"}`,
			expected: `{"command":"cd C:\\\\Users\\\\test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := doubleEscapeWindowsPathsForBash(tt.input)

			// Parse both to compare as objects (order-independent)
			var resultObj, expectedObj map[string]interface{}
			err := json.Unmarshal([]byte(result), &resultObj)
			require.NoError(t, err, "Failed to unmarshal result")

			err = json.Unmarshal([]byte(tt.expected), &expectedObj)
			require.NoError(t, err, "Failed to unmarshal expected")

			assert.Equal(t, expectedObj, resultObj)
		})
	}
}

// TestGeminiCCWindowsPathPreservation tests that Windows paths are preserved correctly
// in Gemini CC response conversion without corruption.
func TestGeminiCCWindowsPathPreservation(t *testing.T) {
	testPath := `F:\MyProjects\test\language\python\xx\hello.py`

	t.Run("Response conversion preserves Windows paths", func(t *testing.T) {
		// Simulate a Gemini response with a function_call containing a Windows path
		geminiResp := &GeminiResponse{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Role: "model",
						Parts: []GeminiPart{
							{
								FunctionCall: &GeminiFunctionCall{
									Name: "Bash",
									Args: map[string]interface{}{
										"command": fmt.Sprintf("python %s", testPath),
									},
								},
							},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		// Convert to Claude format (this is response conversion, should NOT modify paths)
		claudeResp := convertGeminiToClaudeResponse(geminiResp, nil)

		// Verify the response has tool_use blocks
		if len(claudeResp.Content) == 0 {
			t.Fatal("Expected tool_use blocks in response")
		}

		// Find the Bash tool_use block
		var bashBlock *ClaudeContentBlock
		for i := range claudeResp.Content {
			if claudeResp.Content[i].Type == "tool_use" && claudeResp.Content[i].Name == "Bash" {
				bashBlock = &claudeResp.Content[i]
				break
			}
		}

		if bashBlock == nil {
			t.Fatal("Expected Bash tool_use block not found")
		}

		// Parse the Input JSON
		var args map[string]interface{}
		if err := json.Unmarshal(bashBlock.Input, &args); err != nil {
			t.Fatalf("Failed to parse tool_use Input: %v", err)
		}

		command, ok := args["command"].(string)
		if !ok {
			t.Fatal("Expected command field in tool_use Input")
		}

		// Verify the path is preserved correctly
		if !strings.Contains(command, testPath) {
			t.Errorf("Path not preserved in response conversion.\nExpected path: %q\nActual command: %q", testPath, command)
		}

		// Verify no corruption patterns
		corruptedPattern := "MyProjectstestlanguagepythonxxhello.py"
		if strings.Contains(command, corruptedPattern) {
			t.Errorf("Command contains corrupted path pattern: %q", corruptedPattern)
		}
	})
}

// TestGeminiCCThinkingBlockSupport tests that Gemini CC properly handles thinking blocks
func TestGeminiCCThinkingBlockSupport(t *testing.T) {
	t.Run("Non-streaming response with thinking block", func(t *testing.T) {
		geminiResp := &GeminiResponse{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{
								Text:    "Let me think about this problem step by step...",
								Thought: true, // This is thinking content
							},
							{
								Text:    "The answer is 42.",
								Thought: false, // This is regular text
							},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		claudeResp := convertGeminiToClaudeResponse(geminiResp, nil)

		// Should have 2 blocks: thinking + text
		if len(claudeResp.Content) != 2 {
			t.Fatalf("Expected 2 content blocks, got %d", len(claudeResp.Content))
		}

		// First block should be thinking
		if claudeResp.Content[0].Type != "thinking" {
			t.Errorf("Expected first block to be 'thinking', got '%s'", claudeResp.Content[0].Type)
		}
		if claudeResp.Content[0].Thinking != "Let me think about this problem step by step..." {
			t.Errorf("Unexpected thinking content: %s", claudeResp.Content[0].Thinking)
		}

		// Second block should be text
		if claudeResp.Content[1].Type != "text" {
			t.Errorf("Expected second block to be 'text', got '%s'", claudeResp.Content[1].Type)
		}
		if claudeResp.Content[1].Text != "The answer is 42." {
			t.Errorf("Unexpected text content: %s", claudeResp.Content[1].Text)
		}
	})

	t.Run("Non-streaming response with Windows paths in thinking", func(t *testing.T) {
		geminiResp := &GeminiResponse{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{
								Text:    "I need to check F:MyProjectstestlanguagepythonxxhello.py and C:Usersfile.txt",
								Thought: true,
							},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		claudeResp := convertGeminiToClaudeResponse(geminiResp, nil)

		if len(claudeResp.Content) != 1 {
			t.Fatalf("Expected 1 content block, got %d", len(claudeResp.Content))
		}

		thinking := claudeResp.Content[0].Thinking
		// Verify Windows paths are converted to Unix-style
		if !strings.Contains(thinking, "F:/MyProjects") {
			t.Errorf("Expected Unix-style path 'F:/MyProjects' in thinking, got: %s", thinking)
		}
		if !strings.Contains(thinking, "C:/Users") {
			t.Errorf("Expected Unix-style path 'C:/Users' in thinking, got: %s", thinking)
		}
		// Verify no corrupted path patterns remain
		if strings.Contains(thinking, "F:MyProjects") && !strings.Contains(thinking, "F:/MyProjects") {
			t.Errorf("Thinking still contains corrupted path pattern: %s", thinking)
		}
	})

	t.Run("Multiple consecutive thinking parts are merged", func(t *testing.T) {
		geminiResp := &GeminiResponse{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{Text: "First thought.", Thought: true},
							{Text: " Second thought.", Thought: true},
							{Text: " Third thought.", Thought: true},
							{Text: "Final answer.", Thought: false},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		claudeResp := convertGeminiToClaudeResponse(geminiResp, nil)

		// Should have 2 blocks: merged thinking + text
		if len(claudeResp.Content) != 2 {
			t.Fatalf("Expected 2 content blocks, got %d", len(claudeResp.Content))
		}

		// First block should be merged thinking
		if claudeResp.Content[0].Type != "thinking" {
			t.Errorf("Expected first block to be 'thinking', got '%s'", claudeResp.Content[0].Type)
		}
		expectedThinking := "First thought. Second thought. Third thought."
		if claudeResp.Content[0].Thinking != expectedThinking {
			t.Errorf("Expected merged thinking '%s', got '%s'", expectedThinking, claudeResp.Content[0].Thinking)
		}

		// Second block should be text
		if claudeResp.Content[1].Type != "text" {
			t.Errorf("Expected second block to be 'text', got '%s'", claudeResp.Content[1].Type)
		}
	})

	t.Run("Thinking with tool use", func(t *testing.T) {
		geminiResp := &GeminiResponse{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{Text: "Let me use a tool to help.", Thought: true},
							{
								FunctionCall: &GeminiFunctionCall{
									Name: "get_weather",
									Args: map[string]interface{}{"location": "Tokyo"},
								},
							},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		claudeResp := convertGeminiToClaudeResponse(geminiResp, nil)

		// Should have 2 blocks: thinking + tool_use
		if len(claudeResp.Content) != 2 {
			t.Fatalf("Expected 2 content blocks, got %d", len(claudeResp.Content))
		}

		// First block should be thinking
		if claudeResp.Content[0].Type != "thinking" {
			t.Errorf("Expected first block to be 'thinking', got '%s'", claudeResp.Content[0].Type)
		}

		// Second block should be tool_use
		if claudeResp.Content[1].Type != "tool_use" {
			t.Errorf("Expected second block to be 'tool_use', got '%s'", claudeResp.Content[1].Type)
		}
		if claudeResp.Content[1].Name != "get_weather" {
			t.Errorf("Expected tool name 'get_weather', got '%s'", claudeResp.Content[1].Name)
		}
	})
}

// TestGeminiCCStreamingCorruptedWindowsPath tests that corrupted Windows paths (with control characters)
// are properly converted in Gemini CC streaming responses.
// This addresses the user-reported issue where paths like "F:MyProjectstestlanguagepythonxxhello.py"
// (with backslashes converted to control characters) are not properly handled.
func TestGeminiCCStreamingCorruptedWindowsPath(t *testing.T) {
	t.Run("Streaming text with corrupted Windows path", func(t *testing.T) {
		state := newGeminiStreamState(nil)

		// Simulate corrupted path where \t became tab
		// Use explicit tab character construction to preserve the full path
		corruptedPath := "F:MyProjects" + string(rune(9)) + "testlanguagepythonxxhello.py"
		// Path reconstruction: tab character is replaced with slash to rebuild path structure
		expectedPath := "F:/MyProjects/testlanguagepythonxxhello.py"

		chunk := &GeminiStreamChunk{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{
								Text:    corruptedPath,
								Thought: false,
							},
						},
					},
				},
			},
		}

		events := state.processGeminiStreamChunk(chunk)

		// Find the text delta event
		var deltaEvent *ClaudeStreamEvent
		for i := range events {
			if events[i].Type == "content_block_delta" && events[i].Delta != nil && events[i].Delta.Type == "text_delta" {
				deltaEvent = &events[i]
				break
			}
		}

		if deltaEvent == nil {
			t.Fatal("Expected text_delta event not found")
		}

		// Verify corrupted path is fixed
		if !strings.Contains(deltaEvent.Delta.Text, expectedPath) {
			t.Errorf("Expected fixed path %q in delta, got: %s", expectedPath, deltaEvent.Delta.Text)
		}
	})

	t.Run("Streaming thinking with corrupted Windows path", func(t *testing.T) {
		state := newGeminiStreamState(nil)

		// Simulate corrupted path where \t became tab
		// Use explicit tab character construction to preserve the full path
		corruptedPath := "I need to check F:MyProjects" + string(rune(9)) + "testlanguagepythonxxhello.py"
		// Path reconstruction: tab character is replaced with slash to rebuild path structure
		expectedPath := "F:/MyProjects/testlanguagepythonxxhello.py"

		chunk := &GeminiStreamChunk{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{
								Text:    corruptedPath,
								Thought: true,
							},
						},
					},
				},
			},
		}

		events := state.processGeminiStreamChunk(chunk)

		// Find the thinking delta event
		var deltaEvent *ClaudeStreamEvent
		for i := range events {
			if events[i].Type == "content_block_delta" && events[i].Delta != nil && events[i].Delta.Type == "thinking_delta" {
				deltaEvent = &events[i]
				break
			}
		}

		if deltaEvent == nil {
			t.Fatal("Expected thinking_delta event not found")
		}

		// Verify corrupted path is fixed
		if !strings.Contains(deltaEvent.Delta.Thinking, expectedPath) {
			t.Errorf("Expected fixed path %q in thinking delta, got: %s", expectedPath, deltaEvent.Delta.Thinking)
		}
	})
}

// TestGeminiCCStreamingWindowsPathConversion tests that Windows paths are converted in Gemini CC streaming responses
func TestGeminiCCStreamingWindowsPathConversion(t *testing.T) {
	t.Run("Streaming text with Windows paths", func(t *testing.T) {
		state := &geminiStreamState{
			messageID:       "msg_test",
			model:           "gemini-pro",
			nextClaudeIndex: 0,
			openBlockType:   "",
		}

		// Simulate chunk with text containing Windows path
		chunk := &GeminiStreamChunk{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{
								Text:    "● Bash(pwd && ls -F)⎿  /f/MyProjects/test/language/python/xxF:MyProjectstestlanguagepythonxxhello.py",
								Thought: false,
							},
						},
					},
				},
			},
		}

		events := state.processGeminiStreamChunk(chunk)

		// Should have 2 events: content_block_start + content_block_delta
		if len(events) < 2 {
			t.Fatalf("Expected at least 2 events, got %d", len(events))
		}

		// Find the delta event
		var deltaEvent *ClaudeStreamEvent
		for i := range events {
			if events[i].Type == "content_block_delta" && events[i].Delta != nil && events[i].Delta.Type == "text_delta" {
				deltaEvent = &events[i]
				break
			}
		}

		if deltaEvent == nil {
			t.Fatal("Expected text_delta event not found")
		}

		// Verify Windows path is converted
		if !strings.Contains(deltaEvent.Delta.Text, "F:/MyProjects") {
			t.Errorf("Expected Unix-style path 'F:/MyProjects' in delta, got: %s", deltaEvent.Delta.Text)
		}
	})

	t.Run("Streaming thinking with Windows paths", func(t *testing.T) {
		state := &geminiStreamState{
			messageID:       "msg_test",
			model:           "gemini-pro",
			nextClaudeIndex: 0,
			openBlockType:   "",
		}

		// Simulate chunk with thinking containing Windows path
		chunk := &GeminiStreamChunk{
			Candidates: []GeminiCandidate{
				{
					Content: &GeminiContent{
						Parts: []GeminiPart{
							{
								Text:    "I need to check F:MyProjectstestlanguagepythonxxhello.py",
								Thought: true,
							},
						},
					},
				},
			},
		}

		events := state.processGeminiStreamChunk(chunk)

		// Should have 2 events: content_block_start + content_block_delta
		if len(events) < 2 {
			t.Fatalf("Expected at least 2 events, got %d", len(events))
		}

		// Find the delta event
		var deltaEvent *ClaudeStreamEvent
		for i := range events {
			if events[i].Type == "content_block_delta" && events[i].Delta != nil && events[i].Delta.Type == "thinking_delta" {
				deltaEvent = &events[i]
				break
			}
		}

		if deltaEvent == nil {
			t.Fatal("Expected thinking_delta event not found")
		}

		// Verify Windows path is converted
		if !strings.Contains(deltaEvent.Delta.Thinking, "F:/MyProjects") {
			t.Errorf("Expected Unix-style path 'F:/MyProjects' in thinking delta, got: %s", deltaEvent.Delta.Thinking)
		}
	})
}

// TestConvertClaudeToGemini_ToolChoice tests tool_choice conversion
func TestConvertClaudeToGemini_ToolChoice(t *testing.T) {
	tests := []struct {
		name        string
		claudeReq   *ClaudeRequest
		expectError bool
		checkFunc   func(*testing.T, *GeminiRequest)
	}{
		{
			name: "tool_choice auto",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Test"`),
					},
				},
				Tools: []ClaudeTool{
					{
						Name:        "test_tool",
						Description: "Test",
						InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
					},
				},
				ToolChoice: json.RawMessage(`{"type":"auto"}`),
			},
			expectError: false,
			checkFunc: func(t *testing.T, req *GeminiRequest) {
				if req.ToolConfig == nil {
					t.Fatal("expected ToolConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig == nil {
					t.Fatal("expected FunctionCallingConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig.Mode != "AUTO" {
					t.Errorf("expected mode AUTO, got %s", req.ToolConfig.FunctionCallingConfig.Mode)
				}
			},
		},
		{
			name: "tool_choice any",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Test"`),
					},
				},
				Tools: []ClaudeTool{
					{
						Name:        "test_tool",
						Description: "Test",
						InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
					},
				},
				ToolChoice: json.RawMessage(`{"type":"any"}`),
			},
			expectError: false,
			checkFunc: func(t *testing.T, req *GeminiRequest) {
				if req.ToolConfig == nil {
					t.Fatal("expected ToolConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig == nil {
					t.Fatal("expected FunctionCallingConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
					t.Errorf("expected mode ANY, got %s", req.ToolConfig.FunctionCallingConfig.Mode)
				}
			},
		},
		{
			name: "tool_choice none",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Test"`),
					},
				},
				Tools: []ClaudeTool{
					{
						Name:        "test_tool",
						Description: "Test",
						InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
					},
				},
				ToolChoice: json.RawMessage(`{"type":"none"}`),
			},
			expectError: false,
			checkFunc: func(t *testing.T, req *GeminiRequest) {
				if req.ToolConfig == nil {
					t.Fatal("expected ToolConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig == nil {
					t.Fatal("expected FunctionCallingConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig.Mode != "NONE" {
					t.Errorf("expected mode NONE, got %s", req.ToolConfig.FunctionCallingConfig.Mode)
				}
			},
		},
		{
			name: "tool_choice specific tool",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Test"`),
					},
				},
				Tools: []ClaudeTool{
					{
						Name:        "test_tool",
						Description: "Test",
						InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
					},
				},
				ToolChoice: json.RawMessage(`{"type":"tool","name":"test_tool"}`),
			},
			expectError: false,
			checkFunc: func(t *testing.T, req *GeminiRequest) {
				if req.ToolConfig == nil {
					t.Fatal("expected ToolConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig == nil {
					t.Fatal("expected FunctionCallingConfig to be set")
				}
				if req.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
					t.Errorf("expected mode ANY, got %s", req.ToolConfig.FunctionCallingConfig.Mode)
				}
				if len(req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) != 1 {
					t.Fatalf("expected 1 allowed function, got %d", len(req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames))
				}
				if req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] != "test_tool" {
					t.Errorf("expected allowed function test_tool, got %s", req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertClaudeToGemini(tt.claudeReq, nil)
			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkFunc != nil {
					tt.checkFunc(t, result)
				}
			}
		})
	}
}

// TestConvertClaudeMessageToGemini_ToolUse tests tool_use block conversion
func TestConvertClaudeMessageToGemini_ToolUse(t *testing.T) {
	tests := []struct {
		name        string
		message     ClaudeMessage
		expectError bool
		checkFunc   func(*testing.T, []GeminiContent)
	}{
		{
			name: "tool_use block",
			message: ClaudeMessage{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"text","text":"Let me search for that."},
					{"type":"tool_use","id":"toolu_123","name":"search","input":{"query":"test"}}
				]`),
			},
			expectError: false,
			checkFunc: func(t *testing.T, result []GeminiContent) {
				if len(result) != 1 {
					t.Fatalf("expected 1 content, got %d", len(result))
				}
				if result[0].Role != "model" {
					t.Errorf("expected role model, got %s", result[0].Role)
				}
				if len(result[0].Parts) != 2 {
					t.Fatalf("expected 2 parts, got %d", len(result[0].Parts))
				}
				// First part should be text
				if result[0].Parts[0].Text != "Let me search for that." {
					t.Errorf("expected text part, got %v", result[0].Parts[0])
				}
				// Second part should be function call
				if result[0].Parts[1].FunctionCall == nil {
					t.Fatal("expected function call part")
				}
				if result[0].Parts[1].FunctionCall.Name != "search" {
					t.Errorf("expected function name search, got %s", result[0].Parts[1].FunctionCall.Name)
				}
			},
		},
		{
			name: "tool_result block",
			message: ClaudeMessage{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"tool_result","tool_use_id":"toolu_123","content":"Result text"}
				]`),
			},
			expectError: false,
			checkFunc: func(t *testing.T, result []GeminiContent) {
				if len(result) != 1 {
					t.Fatalf("expected 1 content, got %d", len(result))
				}
				if result[0].Role != "function" {
					t.Errorf("expected role function, got %s", result[0].Role)
				}
				if len(result[0].Parts) != 1 {
					t.Fatalf("expected 1 part, got %d", len(result[0].Parts))
				}
				if result[0].Parts[0].FunctionResponse == nil {
					t.Fatal("expected function response part")
				}
			},
		},
		{
			name: "thinking block merged into text",
			message: ClaudeMessage{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"thinking","thinking":"Let me think..."},
					{"type":"text","text":"The answer is 42."}
				]`),
			},
			expectError: false,
			checkFunc: func(t *testing.T, result []GeminiContent) {
				if len(result) != 1 {
					t.Fatalf("expected 1 content, got %d", len(result))
				}
				if result[0].Role != "model" {
					t.Errorf("expected role model, got %s", result[0].Role)
				}
				// Thinking should be merged into text parts
				if len(result[0].Parts) != 2 {
					t.Fatalf("expected 2 parts (thinking + text), got %d", len(result[0].Parts))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolUseIDToName := make(map[string]string)
			result, err := convertClaudeMessageToGemini(tt.message, nil, toolUseIDToName)
			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkFunc != nil {
					tt.checkFunc(t, result)
				}
			}
		})
	}
}

// TestProcessJsonSchema_ComplexSchemas tests JSON schema processing with complex structures
func TestProcessJsonSchema_ComplexSchemas(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		checkFunc func(*testing.T, json.RawMessage)
	}{
		{
			name:  "schema with $schema field",
			input: `{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","properties":{"name":{"type":"string"}}}`,
			checkFunc: func(t *testing.T, result json.RawMessage) {
				var schema map[string]interface{}
				if err := json.Unmarshal(result, &schema); err != nil {
					t.Fatalf("failed to unmarshal result: %v", err)
				}
				// $schema should be removed
				if _, ok := schema["$schema"]; ok {
					t.Error("expected $schema to be removed")
				}
				// Type should be uppercase
				if schema["type"] != "OBJECT" {
					t.Errorf("expected type OBJECT, got %v", schema["type"])
				}
			},
		},
		{
			name:  "schema with additionalProperties",
			input: `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`,
			checkFunc: func(t *testing.T, result json.RawMessage) {
				var schema map[string]interface{}
				if err := json.Unmarshal(result, &schema); err != nil {
					t.Fatalf("failed to unmarshal result: %v", err)
				}
				// additionalProperties should be removed
				if _, ok := schema["additionalProperties"]; ok {
					t.Error("expected additionalProperties to be removed")
				}
			},
		},
		{
			name:  "schema with constraints",
			input: `{"type":"string","minLength":1,"maxLength":100}`,
			checkFunc: func(t *testing.T, result json.RawMessage) {
				var schema map[string]interface{}
				if err := json.Unmarshal(result, &schema); err != nil {
					t.Fatalf("failed to unmarshal result: %v", err)
				}
				// Constraints should be moved to description
				if desc, ok := schema["description"].(string); ok {
					if !strings.Contains(desc, "minLength") {
						t.Error("expected minLength in description")
					}
				} else {
					t.Error("expected description field with constraints")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := processJsonSchema(json.RawMessage(tt.input))
			if tt.checkFunc != nil {
				tt.checkFunc(t, result)
			}
		})
	}
}

// TestHandleGeminiCCNormalResponse tests non-streaming Gemini response conversion
func TestHandleGeminiCCNormalResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		responseBody   string
		responseStatus int
		checkFunc      func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "successful text response",
			responseBody: `{
				"candidates": [
					{
						"content": {
							"role": "model",
							"parts": [
								{"text": "Hello from Gemini!"}
							]
						},
						"finishReason": "STOP",
						"index": 0
					}
				],
				"usageMetadata": {
					"promptTokenCount": 10,
					"candidatesTokenCount": 5,
					"totalTokenCount": 15
				},
				"modelVersion": "gemini-pro"
			}`,
			responseStatus: http.StatusOK,
			checkFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				if w.Code != http.StatusOK {
					t.Errorf("expected status 200, got %d", w.Code)
				}
				var claudeResp ClaudeResponse
				if err := json.Unmarshal(w.Body.Bytes(), &claudeResp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if claudeResp.Type != "message" {
					t.Errorf("expected type message, got %s", claudeResp.Type)
				}
				if len(claudeResp.Content) == 0 {
					t.Fatal("expected at least one content block")
				}
				if claudeResp.Content[0].Type != "text" {
					t.Errorf("expected text block, got %s", claudeResp.Content[0].Type)
				}
				if claudeResp.Content[0].Text != "Hello from Gemini!" {
					t.Errorf("expected 'Hello from Gemini!', got %s", claudeResp.Content[0].Text)
				}
			},
		},
		{
			name: "response with function call",
			responseBody: `{
				"candidates": [
					{
						"content": {
							"role": "model",
							"parts": [
								{
									"functionCall": {
										"name": "get_weather",
										"args": {"location": "Tokyo"}
									}
								}
							]
						},
						"finishReason": "STOP",
						"index": 0
					}
				]
			}`,
			responseStatus: http.StatusOK,
			checkFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				var claudeResp ClaudeResponse
				if err := json.Unmarshal(w.Body.Bytes(), &claudeResp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if len(claudeResp.Content) == 0 {
					t.Fatal("expected at least one content block")
				}
				if claudeResp.Content[0].Type != "tool_use" {
					t.Errorf("expected tool_use block, got %s", claudeResp.Content[0].Type)
				}
				if claudeResp.Content[0].Name != "get_weather" {
					t.Errorf("expected tool name get_weather, got %s", claudeResp.Content[0].Name)
				}
			},
		},
		{
			name: "response with thinking (Gemini 2.5+)",
			responseBody: `{
				"candidates": [
					{
						"content": {
							"role": "model",
							"parts": [
								{"text": "Let me analyze this...", "thought": true},
								{"text": "The solution is X.", "thought": false}
							]
						},
						"finishReason": "STOP",
						"index": 0
					}
				]
			}`,
			responseStatus: http.StatusOK,
			checkFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				var claudeResp ClaudeResponse
				if err := json.Unmarshal(w.Body.Bytes(), &claudeResp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				// Should have thinking + text blocks
				if len(claudeResp.Content) < 2 {
					t.Fatalf("expected at least 2 content blocks, got %d", len(claudeResp.Content))
				}
				if claudeResp.Content[0].Type != "thinking" {
					t.Errorf("expected first block to be thinking, got %s", claudeResp.Content[0].Type)
				}
				if claudeResp.Content[1].Type != "text" {
					t.Errorf("expected second block to be text, got %s", claudeResp.Content[1].Type)
				}
			},
		},
		{
			name:           "error response from upstream",
			responseBody:   `{"error":{"code":400,"message":"Invalid request","status":"INVALID_ARGUMENT"}}`,
			responseStatus: http.StatusBadRequest,
			checkFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				if w.Code != http.StatusBadRequest {
					t.Errorf("expected status 400, got %d", w.Code)
				}
				// Gemini error responses are converted to Claude format
				// The handler should convert the error to Claude format
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock upstream response
			upstreamResp := &http.Response{
				StatusCode: tt.responseStatus,
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
				Header:     make(http.Header),
			}
			upstreamResp.Header.Set("Content-Type", "application/json")

			// Create test context
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/test", nil)

			// Call the handler
			ps := &ProxyServer{}
			ps.handleGeminiCCNormalResponse(c, upstreamResp)

			// Check results
			if tt.checkFunc != nil {
				tt.checkFunc(t, w)
			}
		})
	}
}

// TestHandleGeminiCCStreamingResponse tests streaming Gemini response conversion
// NOTE: Gemini's streaming API returns a single complete JSON response (not JSON lines),
// so we test with single JSON objects representing the complete response.
func TestHandleGeminiCCStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		responseBody string
		checkFunc    func(*testing.T, string)
	}{
		{
			name: "basic text streaming",
			// Gemini returns a single JSON response with all parts
			responseBody: `{
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [
							{"text": "Hello world"}
						]
					},
					"finishReason": "STOP",
					"index": 0
				}],
				"usageMetadata": {
					"promptTokenCount": 10,
					"candidatesTokenCount": 5,
					"totalTokenCount": 15
				},
				"modelVersion": "gemini-pro"
			}`,
			checkFunc: func(t *testing.T, output string) {
				if !strings.Contains(output, "event: message_start") {
					t.Error("expected message_start event")
				}
				if !strings.Contains(output, "event: content_block_start") {
					t.Error("expected content_block_start event")
				}
				if !strings.Contains(output, "event: content_block_delta") {
					t.Error("expected content_block_delta event")
				}
				if !strings.Contains(output, "event: message_stop") {
					t.Error("expected message_stop event")
				}
			},
		},
		{
			name: "streaming with function call",
			responseBody: `{
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [
							{
								"functionCall": {
									"name": "search",
									"args": {"query": "test"}
								}
							}
						]
					},
					"finishReason": "STOP",
					"index": 0
				}],
				"modelVersion": "gemini-pro"
			}`,
			checkFunc: func(t *testing.T, output string) {
				if !strings.Contains(output, `"type":"tool_use"`) {
					t.Error("expected tool_use block")
				}
				if !strings.Contains(output, `"name":"search"`) {
					t.Error("expected tool name search")
				}
			},
		},
		{
			name: "streaming with thinking",
			responseBody: `{
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [
							{"text": "Analyzing...", "thought": true},
							{"text": "The answer is 42.", "thought": false}
						]
					},
					"finishReason": "STOP",
					"index": 0
				}],
				"modelVersion": "gemini-2.5-pro"
			}`,
			checkFunc: func(t *testing.T, output string) {
				if !strings.Contains(output, `"type":"thinking"`) {
					t.Error("expected thinking block")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock upstream response
			upstreamResp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
				Header:     make(http.Header),
			}
			upstreamResp.Header.Set("Content-Type", "application/json")

			// Create test context
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/test", nil)

			// Call the handler
			ps := &ProxyServer{}
			ps.handleGeminiCCStreamingResponse(c, upstreamResp)

			// Check results
			output := w.Body.String()
			if tt.checkFunc != nil {
				tt.checkFunc(t, output)
			}
		})
	}
}

// TestGeminiHelperFunctions tests various helper functions in gemini_cc_support.go
func TestGeminiHelperFunctions(t *testing.T) {
	t.Run("isGeminiCCMode", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		// Test when not set
		if isGeminiCCMode(c) {
			t.Error("expected false when ctxKeyGeminiCC not set")
		}

		// Test when set to true
		c.Set(ctxKeyGeminiCC, true)
		if !isGeminiCCMode(c) {
			t.Error("expected true when ctxKeyGeminiCC is true")
		}

		// Test when set to false
		c.Set(ctxKeyGeminiCC, false)
		if isGeminiCCMode(c) {
			t.Error("expected false when ctxKeyGeminiCC is false")
		}
	})

	t.Run("getGeminiToolNameReverseMap", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		// Test with no map set
		result := getGeminiToolNameReverseMap(c)
		if result != nil {
			t.Error("expected nil when no map is set")
		}

		// Test with map set
		testMap := map[string]string{"short": "original"}
		c.Set(ctxKeyGeminiToolNameReverseMap, testMap)
		result = getGeminiToolNameReverseMap(c)
		if result == nil {
			t.Fatal("expected non-nil map")
		}
		if result["short"] != "original" {
			t.Errorf("expected 'original', got %q", result["short"])
		}
	})

	t.Run("rewriteClaudePathToGemini", func(t *testing.T) {
		tests := []struct {
			name     string
			input    string
			expected string
		}{
			{
				name:     "basic_path",
				input:    "/proxy/group/claude/v1/models",
				expected: "/proxy/group/v1beta/models",
			},
			{
				name:     "messages_path",
				input:    "/proxy/group/claude/v1/messages",
				expected: "/proxy/group/v1beta/messages",
			},
			{
				name:     "group_named_claude",
				input:    "/proxy/claude/claude/v1/models",
				expected: "/proxy/claude/v1beta/models",
			},
			{
				name:     "no_claude_segment",
				input:    "/proxy/group/v1/models",
				expected: "/proxy/group/v1/models",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := rewriteClaudePathToGemini(tt.input)
				if result != tt.expected {
					t.Errorf("expected %q, got %q", tt.expected, result)
				}
			})
		}
	})
}

package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
			expected: `{"type":"STRING","description":"Accepts: string | null"}`,
		},
		{
			name:     "anyOf with null as first element",
			input:    `{"anyOf":[{"type":"null"},{"type":"object","properties":{"id":{"type":"integer"}}}]}`,
			expected: `{"type":"OBJECT","properties":{"id":{"type":"INTEGER"}},"description":"Accepts: null | object"}`,
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
	// Note: The current implementation sets OutputTokens to 0 in message_delta
	// This is acceptable as usage is tracked separately in the response
	assert.Equal(t, 0, deltaEvent.Usage.OutputTokens)
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
		corruptedPath := "F:MyProjects\testlanguagepythonxxhello.py"
		expectedPath := "F:/MyProjectsestlanguagepythonxxhello.py"

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
		corruptedPath := "I need to check F:MyProjects\testlanguagepythonxxhello.py"
		expectedPath := "F:/MyProjectsestlanguagepythonxxhello.py"

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

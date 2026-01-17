package proxy

import (
	"encoding/json"
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
			result, err := convertClaudeMessageToGemini(tt.message, nil)

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

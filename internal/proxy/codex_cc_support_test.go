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

// TestCodexCCWindowsPathPreservation tests that Windows paths are preserved correctly
// in Codex CC response conversion without corruption.
func TestCodexCCWindowsPathPreservation(t *testing.T) {
	testPath := `F:\MyProjects\test\language\python\xx\hello.py`

	t.Run("Response conversion preserves Windows paths", func(t *testing.T) {
		// Create proper JSON string for Arguments
		argsMap := map[string]interface{}{
			"command": fmt.Sprintf("python %s", testPath),
		}
		argsJSON, _ := json.Marshal(argsMap)

		// Simulate a Codex response with a function_call containing a Windows path
		codexResp := &CodexResponse{
			ID:     "resp_test123",
			Object: "response",
			Status: "completed",
			Model:  "gpt-4o",
			Output: []CodexOutputItem{
				{
					Type:      "function_call",
					CallID:    "call_abc123",
					Name:      "Bash",
					Arguments: string(argsJSON),
				},
			},
		}

		// Convert to Claude format (this is response conversion, should NOT modify paths)
		claudeResp := convertCodexToClaudeResponse(codexResp, nil)

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

// TestCodexCCThinkingBlockWindowsPathConversion tests that Windows paths in Codex reasoning/thinking blocks are converted to Unix-style
func TestCodexCCThinkingBlockWindowsPathConversion(t *testing.T) {
	// Simulate Codex response with reasoning block containing Windows paths
	codexResp := map[string]interface{}{
		"id": "resp_123",
		"output": []interface{}{
			map[string]interface{}{
				"type": "reasoning",
				"summary": []interface{}{
					map[string]interface{}{
						"type": "summary_text",
						"text": "I need to check the file at F:MyProjectstestlanguagepythonxxhello.py and also C:Usersfile.txt",
					},
				},
			},
			map[string]interface{}{
				"type": "message",
				"content": []interface{}{
					map[string]interface{}{
						"type": "output_text",
						"text": "Let me help you with that.",
					},
				},
			},
		},
	}

	// Convert to Claude response
	claudeResp := convertCodexToClaudeResponseFromMap(codexResp, nil)

	// Find thinking block
	var thinkingBlock *ClaudeContentBlock
	for i := range claudeResp.Content {
		if claudeResp.Content[i].Type == "thinking" {
			thinkingBlock = &claudeResp.Content[i]
			break
		}
	}

	if thinkingBlock == nil {
		t.Fatal("Expected thinking block not found")
	}

	// Verify Windows paths are converted to Unix-style
	expectedPaths := []string{"F:/MyProjects", "C:/Users"}
	for _, expectedPath := range expectedPaths {
		if !strings.Contains(thinkingBlock.Thinking, expectedPath) {
			t.Errorf("Expected Unix-style path %q in thinking block, got: %q", expectedPath, thinkingBlock.Thinking)
		}
	}

	// Verify no corrupted path patterns remain
	corruptedPatterns := []string{"F:MyProjects", "C:Users"}
	for _, pattern := range corruptedPatterns {
		// Allow the pattern if it's followed by a slash (already converted)
		if strings.Contains(thinkingBlock.Thinking, pattern) && !strings.Contains(thinkingBlock.Thinking, pattern+"/") {
			t.Errorf("Thinking block still contains corrupted path pattern %q: %q", pattern, thinkingBlock.Thinking)
		}
	}
}

// Helper function to convert Codex response map to Claude response
func convertCodexToClaudeResponseFromMap(codexResp map[string]interface{}, reverseToolNameMap map[string]string) *ClaudeResponse {
	// This is a simplified version for testing
	claudeResp := &ClaudeResponse{
		ID:      "msg_test",
		Type:    "message",
		Role:    "assistant",
		Content: make([]ClaudeContentBlock, 0),
	}

	output, ok := codexResp["output"].([]interface{})
	if !ok {
		return claudeResp
	}

	for _, itemInterface := range output {
		item, ok := itemInterface.(map[string]interface{})
		if !ok {
			continue
		}

		itemType, _ := item["type"].(string)
		switch itemType {
		case "reasoning":
			// Convert reasoning to thinking block
			var thinkingText strings.Builder
			if summary, ok := item["summary"].([]interface{}); ok {
				for _, summaryItemInterface := range summary {
					if summaryItem, ok := summaryItemInterface.(map[string]interface{}); ok {
						if summaryItem["type"] == "summary_text" {
							if text, ok := summaryItem["text"].(string); ok {
								thinkingText.WriteString(text)
							}
						}
					}
				}
			}
			if thinkingText.Len() > 0 {
				// Convert Windows paths to Unix-style for Claude Code compatibility
				thinking := convertWindowsPathsInToolResult(thinkingText.String())
				claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
					Type:     "thinking",
					Thinking: thinking,
				})
			}
		case "message":
			if content, ok := item["content"].([]interface{}); ok {
				for _, contentInterface := range content {
					if contentItem, ok := contentInterface.(map[string]interface{}); ok {
						if contentItem["type"] == "output_text" {
							if text, ok := contentItem["text"].(string); ok {
								// Convert Windows paths to Unix-style for Claude Code compatibility
								text = convertWindowsPathsInToolResult(text)
								claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
									Type: "text",
									Text: text,
								})
							}
						}
					}
				}
			}
		}
	}

	return claudeResp
}

// TestCodexCCStreamingWindowsPathConversion tests that Windows paths are converted in Codex CC streaming responses
func TestCodexCCStreamingWindowsPathConversion(t *testing.T) {
	t.Run("Streaming text delta with Windows paths", func(t *testing.T) {
		state := &codexStreamState{
			messageID:       "msg_test",
			model:           "gpt-4",
			nextClaudeIndex: 0,
			openBlockType:   "",
		}

		// Simulate text delta event with Windows path
		event := &CodexStreamEvent{
			Type:  "response.output_text.delta",
			Delta: "● Bash(pwd && ls -F)⎿  /f/MyProjects/test/language/python/xxF:MyProjectstestlanguagepythonxxhello.py",
		}

		events := state.processCodexStreamEvent(event)

		// Should have 2 events: content_block_start + content_block_delta
		if len(events) < 2 {
			t.Fatalf("Expected at least 2 events, got %d", len(events))
		}

		// Find the delta event
		var deltaEvent *ClaudeStreamEvent
		for i := range events {
			if events[i].Type == "content_block_delta" {
				deltaEvent = &events[i]
				break
			}
		}

		if deltaEvent == nil {
			t.Fatal("Expected content_block_delta event not found")
		}

		if deltaEvent.Delta == nil {
			t.Fatal("Delta is nil")
		}

		// Verify Windows path is converted
		if !strings.Contains(deltaEvent.Delta.Text, "F:/MyProjects") {
			t.Errorf("Expected Unix-style path 'F:/MyProjects' in delta, got: %s", deltaEvent.Delta.Text)
		}
	})

	t.Run("Streaming thinking delta with Windows paths", func(t *testing.T) {
		state := &codexStreamState{
			messageID:       "msg_test",
			model:           "gpt-4",
			nextClaudeIndex: 0,
			openBlockType:   "",
			inThinkingBlock: false,
		}

		// Simulate thinking delta event with Windows path
		event := &CodexStreamEvent{
			Type:  "response.reasoning_summary_text.delta",
			Delta: "I need to check F:MyProjectstestlanguagepythonxxhello.py",
		}

		events := state.processCodexStreamEvent(event)

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

// TestCodexCCCorruptedWindowsPathConversion tests that corrupted Windows paths (with control characters)
// are properly converted in Codex CC for both streaming and non-streaming.
func TestCodexCCCorruptedWindowsPathConversion(t *testing.T) {
	// Simulate corrupted path where \t became tab
	// Use explicit tab character construction to preserve the full path
	corruptedPath := "F:MyProjects" + string(rune(9)) + "testlanguagepythonxxhello.py"
	// Path reconstruction: tab character is replaced with slash to rebuild path structure
	expectedPath := "F:/MyProjects/testlanguagepythonxxhello.py"

	t.Run("Non-streaming with corrupted path", func(t *testing.T) {
		codexResp := map[string]interface{}{
			"id": "resp_123",
			"output": []interface{}{
				map[string]interface{}{
					"type": "message",
					"content": []interface{}{
						map[string]interface{}{
							"type": "output_text",
							"text": corruptedPath,
						},
					},
				},
			},
		}

		claudeResp := convertCodexToClaudeResponseFromMap(codexResp, nil)

		if len(claudeResp.Content) == 0 {
			t.Fatal("Expected at least one content block")
		}

		textBlock := claudeResp.Content[0]
		if textBlock.Type != "text" {
			t.Fatalf("Expected text block, got %s", textBlock.Type)
		}

		// Verify corrupted path is fixed
		if !strings.Contains(textBlock.Text, expectedPath) {
			t.Errorf("Expected fixed path %q in text, got: %s", expectedPath, textBlock.Text)
		}
	})

	t.Run("Streaming with corrupted path", func(t *testing.T) {
		state := &codexStreamState{
			messageID:       "msg_test",
			model:           "gpt-4",
			nextClaudeIndex: 0,
			openBlockType:   "",
		}

		event := &CodexStreamEvent{
			Type:  "response.output_text.delta",
			Delta: corruptedPath,
		}

		events := state.processCodexStreamEvent(event)

		var deltaEvent *ClaudeStreamEvent
		for i := range events {
			if events[i].Type == "content_block_delta" {
				deltaEvent = &events[i]
				break
			}
		}

		if deltaEvent == nil || deltaEvent.Delta == nil {
			t.Fatal("Expected content_block_delta event not found")
		}

		// Verify corrupted path is fixed
		if !strings.Contains(deltaEvent.Delta.Text, expectedPath) {
			t.Errorf("Expected fixed path %q in delta, got: %s", expectedPath, deltaEvent.Delta.Text)
		}
	})
}

// TestConvertClaudeToCodex tests the basic Claude to Codex conversion
func TestConvertClaudeToCodex(t *testing.T) {
	tests := []struct {
		name        string
		claudeReq   *ClaudeRequest
		customInstr string
		group       *models.Group
		expectError bool
		checkFunc   func(*testing.T, *CodexRequest)
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
			customInstr: "",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				if req.Model != "claude-3-5-sonnet-20241022" {
					t.Errorf("expected model claude-3-5-sonnet-20241022, got %s", req.Model)
				}
				if req.Instructions == "" {
					t.Error("expected non-empty instructions")
				}
				var inputItems []interface{}
				if err := json.Unmarshal(req.Input, &inputItems); err != nil {
					t.Fatalf("failed to unmarshal input: %v", err)
				}
				if len(inputItems) == 0 {
					t.Error("expected at least one input item")
				}
			},
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
			customInstr: "",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				var inputItems []interface{}
				if err := json.Unmarshal(req.Input, &inputItems); err != nil {
					t.Fatalf("failed to unmarshal input: %v", err)
				}
				// Should have system message + user message
				if len(inputItems) < 2 {
					t.Errorf("expected at least 2 input items (system + user), got %d", len(inputItems))
				}
			},
		},
		{
			name: "with tools",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Use the search tool"`),
					},
				},
				Tools: []ClaudeTool{
					{
						Name:        "web_search",
						Description: "Search the web",
						InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
					},
				},
				MaxTokens: 1024,
			},
			customInstr: "",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				if len(req.Tools) != 1 {
					t.Fatalf("expected 1 tool, got %d", len(req.Tools))
				}
				if req.Tools[0].Name != "web_search" {
					t.Errorf("expected tool name web_search, got %s", req.Tools[0].Name)
				}
			},
		},
		{
			name: "with custom instructions",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Test"`),
					},
				},
				MaxTokens: 1024,
			},
			customInstr: "Custom instructions for testing",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				if req.Instructions != "Custom instructions for testing" {
					t.Errorf("expected custom instructions, got %s", req.Instructions)
				}
			},
		},
		{
			name: "with tool choice auto",
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
				MaxTokens:  1024,
			},
			customInstr: "",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				if req.ToolChoice != "auto" {
					t.Errorf("expected tool_choice auto, got %v", req.ToolChoice)
				}
			},
		},
		{
			name: "with tool choice any",
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
				MaxTokens:  1024,
			},
			customInstr: "",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				if req.ToolChoice != "required" {
					t.Errorf("expected tool_choice required, got %v", req.ToolChoice)
				}
			},
		},
		{
			name: "with tool choice none",
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
				MaxTokens:  1024,
			},
			customInstr: "",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				if req.ToolChoice != "none" {
					t.Errorf("expected tool_choice none, got %v", req.ToolChoice)
				}
			},
		},
		{
			name: "with thinking enabled",
			claudeReq: &ClaudeRequest{
				Model: "claude-3-5-sonnet-20241022",
				Messages: []ClaudeMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Test"`),
					},
				},
				Thinking: &ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: 5000,
				},
				MaxTokens: 1024,
			},
			customInstr: "",
			group:       nil,
			expectError: false,
			checkFunc: func(t *testing.T, req *CodexRequest) {
				if req.Reasoning == nil {
					t.Fatal("expected reasoning to be set")
				}
				if req.Reasoning.Effort == "" {
					t.Error("expected reasoning effort to be set")
				}
				if req.Reasoning.Summary != "auto" {
					t.Errorf("expected reasoning summary auto, got %s", req.Reasoning.Summary)
				}
				if req.Store == nil || *req.Store != false {
					t.Error("expected store to be false")
				}
				if len(req.Include) == 0 {
					t.Error("expected include to contain reasoning.encrypted_content")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertClaudeToCodex(tt.claudeReq, tt.customInstr, tt.group)
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

// TestBuildToolNameShortMap tests the buildToolNameShortMap function with various scenarios
func TestBuildToolNameShortMap(t *testing.T) {
	tests := []struct {
		name      string
		toolNames []string
		checkFunc func(*testing.T, map[string]string)
	}{
		{
			name:      "empty list",
			toolNames: []string{},
			checkFunc: func(t *testing.T, result map[string]string) {
				if result != nil {
					t.Errorf("expected nil for empty list, got %v", result)
				}
			},
		},
		{
			name:      "all names within limit",
			toolNames: []string{"short", "name", "test"},
			checkFunc: func(t *testing.T, result map[string]string) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				for orig, short := range result {
					if orig != short {
						t.Errorf("expected identity mapping for short name %q, got %q", orig, short)
					}
					if len(short) > 64 {
						t.Errorf("shortened name %q exceeds 64 chars", short)
					}
				}
			},
		},
		{
			name: "one name exceeds limit",
			toolNames: []string{
				"this_is_a_very_long_tool_name_that_exceeds_the_sixty_four_character_limit_for_codex",
			},
			checkFunc: func(t *testing.T, result map[string]string) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				if len(result) != 1 {
					t.Fatalf("expected 1 mapping, got %d", len(result))
				}
				for orig, short := range result {
					if len(short) > 64 {
						t.Errorf("shortened name %q exceeds 64 chars", short)
					}
					if orig == short {
						t.Error("expected name to be shortened")
					}
				}
			},
		},
		{
			name: "mcp prefix tool name",
			toolNames: []string{
				"mcp__server_name__very_long_tool_name_that_needs_to_be_shortened_to_fit_within_limit",
			},
			checkFunc: func(t *testing.T, result map[string]string) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				for _, short := range result {
					if len(short) > 64 {
						t.Errorf("shortened name %q exceeds 64 chars", short)
					}
					if !strings.HasPrefix(short, "mcp__") {
						t.Errorf("expected shortened name to preserve mcp__ prefix, got %q", short)
					}
				}
			},
		},
		{
			name: "duplicate names",
			toolNames: []string{
				"test_tool",
				"test_tool",
				"another_tool",
			},
			checkFunc: func(t *testing.T, result map[string]string) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				// Duplicates should be skipped, so we should have 2 unique mappings
				if len(result) != 2 {
					t.Errorf("expected 2 unique mappings, got %d", len(result))
				}
			},
		},
		{
			name: "collision after shortening",
			toolNames: []string{
				"this_is_a_very_long_tool_name_that_will_be_shortened_version_one",
				"this_is_a_very_long_tool_name_that_will_be_shortened_version_two",
			},
			checkFunc: func(t *testing.T, result map[string]string) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				if len(result) != 2 {
					t.Fatalf("expected 2 mappings, got %d", len(result))
				}
				// Check that shortened names are unique
				seen := make(map[string]bool)
				for _, short := range result {
					if seen[short] {
						t.Errorf("duplicate shortened name %q", short)
					}
					seen[short] = true
					if len(short) > 64 {
						t.Errorf("shortened name %q exceeds 64 chars", short)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildToolNameShortMap(tt.toolNames)
			if tt.checkFunc != nil {
				tt.checkFunc(t, result)
			}
		})
	}
}

// TestBuildReverseToolNameMap tests the buildReverseToolNameMap function
func TestBuildReverseToolNameMap(t *testing.T) {
	tests := []struct {
		name      string
		shortMap  map[string]string
		checkFunc func(*testing.T, map[string]string)
	}{
		{
			name:     "empty map",
			shortMap: map[string]string{},
			checkFunc: func(t *testing.T, result map[string]string) {
				if len(result) != 0 {
					t.Errorf("expected empty reverse map, got %d entries", len(result))
				}
			},
		},
		{
			name: "identity mappings",
			shortMap: map[string]string{
				"tool1": "tool1",
				"tool2": "tool2",
			},
			checkFunc: func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Fatalf("expected 2 entries, got %d", len(result))
				}
				if result["tool1"] != "tool1" {
					t.Errorf("expected tool1 -> tool1, got %s", result["tool1"])
				}
				if result["tool2"] != "tool2" {
					t.Errorf("expected tool2 -> tool2, got %s", result["tool2"])
				}
			},
		},
		{
			name: "shortened mappings",
			shortMap: map[string]string{
				"very_long_tool_name": "very_long_tool_name_1",
				"another_long_name":   "another_long_name_2",
			},
			checkFunc: func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Fatalf("expected 2 entries, got %d", len(result))
				}
				if result["very_long_tool_name_1"] != "very_long_tool_name" {
					t.Errorf("reverse mapping incorrect for very_long_tool_name_1")
				}
				if result["another_long_name_2"] != "another_long_name" {
					t.Errorf("reverse mapping incorrect for another_long_name_2")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildReverseToolNameMap(tt.shortMap)
			if tt.checkFunc != nil {
				tt.checkFunc(t, result)
			}
		})
	}
}

// TestHandleCodexCCNormalResponse tests non-streaming Codex response conversion
func TestHandleCodexCCNormalResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		responseBody   string
		responseStatus int
		expectError    bool
		checkFunc      func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "successful text response",
			responseBody: `{
				"id": "resp_123",
				"object": "response",
				"created_at": 1234567890,
				"status": "completed",
				"model": "gpt-4o",
				"output": [
					{
						"type": "message",
						"role": "assistant",
						"content": [
							{"type": "output_text", "text": "Hello, world!"}
						]
					}
				],
				"usage": {
					"input_tokens": 10,
					"output_tokens": 5,
					"total_tokens": 15
				}
			}`,
			responseStatus: http.StatusOK,
			expectError:    false,
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
					t.Error("expected at least one content block")
				}
				if claudeResp.Content[0].Type != "text" {
					t.Errorf("expected text block, got %s", claudeResp.Content[0].Type)
				}
				if claudeResp.Content[0].Text != "Hello, world!" {
					t.Errorf("expected 'Hello, world!', got %s", claudeResp.Content[0].Text)
				}
			},
		},
		{
			name: "response with tool call",
			responseBody: `{
				"id": "resp_456",
				"object": "response",
				"status": "completed",
				"model": "gpt-4o",
				"output": [
					{
						"type": "function_call",
						"call_id": "call_abc123",
						"name": "web_search",
						"arguments": "{\"query\":\"test\"}"
					}
				]
			}`,
			responseStatus: http.StatusOK,
			expectError:    false,
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
				if claudeResp.Content[0].Name != "web_search" {
					t.Errorf("expected tool name web_search, got %s", claudeResp.Content[0].Name)
				}
			},
		},
		{
			name: "response with reasoning/thinking",
			responseBody: `{
				"id": "resp_789",
				"object": "response",
				"status": "completed",
				"model": "gpt-4o",
				"output": [
					{
						"type": "reasoning",
						"summary": [
							{"type": "summary_text", "text": "Let me think about this..."}
						]
					},
					{
						"type": "message",
						"content": [
							{"type": "output_text", "text": "The answer is 42."}
						]
					}
				]
			}`,
			responseStatus: http.StatusOK,
			expectError:    false,
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
			responseBody:   `{"error":{"type":"invalid_request_error","message":"Invalid model"}}`,
			responseStatus: http.StatusBadRequest,
			expectError:    false,
			checkFunc: func(t *testing.T, w *httptest.ResponseRecorder) {
				if w.Code != http.StatusBadRequest {
					t.Errorf("expected status 400, got %d", w.Code)
				}
				var claudeErr ClaudeErrorResponse
				if err := json.Unmarshal(w.Body.Bytes(), &claudeErr); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if claudeErr.Type != "error" {
					t.Errorf("expected type error, got %s", claudeErr.Type)
				}
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
			ps.handleCodexCCNormalResponse(c, upstreamResp)

			// Check results
			if tt.checkFunc != nil {
				tt.checkFunc(t, w)
			}
		})
	}
}

// TestHandleCodexCCStreamingResponse tests streaming Codex response conversion
func TestHandleCodexCCStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		events    []string
		checkFunc func(*testing.T, string)
	}{
		{
			name: "basic text streaming",
			events: []string{
				`{"type":"response.created","response":{"id":"resp_123","model":"gpt-4o"}}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`,
				`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text"}}`,
				`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hello"}`,
				`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":" world"}`,
				`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Hello world"}`,
				`{"type":"response.content_part.done","output_index":0,"content_index":0}`,
				`{"type":"response.output_item.done","output_index":0}`,
				`{"type":"response.completed"}`,
			},
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
				if !strings.Contains(output, `"text":"Hello"`) && !strings.Contains(output, `"text":" world"`) {
					t.Error("expected text deltas in output")
				}
			},
		},
		{
			name: "streaming with tool call",
			events: []string{
				`{"type":"response.created","response":{"id":"resp_456","model":"gpt-4o"}}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_123","name":"web_search"}}`,
				`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"query\":"}`,
				`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"test\"}"}`,
				`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"query\":\"test\"}"}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_123","name":"web_search","arguments":"{\"query\":\"test\"}"}}`,
				`{"type":"response.completed"}`,
			},
			checkFunc: func(t *testing.T, output string) {
				if !strings.Contains(output, "event: content_block_start") {
					t.Error("expected content_block_start for tool_use")
				}
				if !strings.Contains(output, `"type":"tool_use"`) {
					t.Error("expected tool_use block")
				}
				if !strings.Contains(output, `"name":"web_search"`) {
					t.Error("expected tool name web_search")
				}
			},
		},
		{
			name: "streaming with reasoning",
			events: []string{
				`{"type":"response.created","response":{"id":"resp_789","model":"gpt-4o"}}`,
				`{"type":"response.reasoning_summary_part.added","output_index":0}`,
				`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Thinking..."}`,
				`{"type":"response.reasoning_summary_text.done","output_index":0,"text":"Thinking..."}`,
				`{"type":"response.reasoning_summary_part.done","output_index":0}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"message"}}`,
				`{"type":"response.content_part.added","output_index":1,"content_index":0,"part":{"type":"output_text"}}`,
				`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Answer"}`,
				`{"type":"response.completed"}`,
			},
			checkFunc: func(t *testing.T, output string) {
				// Should have thinking block
				if !strings.Contains(output, `"type":"thinking"`) {
					t.Error("expected thinking block")
				}
				if !strings.Contains(output, `"thinking":"Thinking..."`) {
					t.Error("expected thinking content")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create SSE stream
			var sseData strings.Builder
			for _, event := range tt.events {
				sseData.WriteString("data: ")
				sseData.WriteString(event)
				sseData.WriteString("\n\n")
			}
			sseData.WriteString("data: [DONE]\n\n")

			// Create mock upstream response
			upstreamResp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(sseData.String())),
				Header:     make(http.Header),
			}
			upstreamResp.Header.Set("Content-Type", "text/event-stream")

			// Create test context
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/test", nil)

			// Call the handler
			ps := &ProxyServer{}
			ps.handleCodexCCStreamingResponse(c, upstreamResp)

			// Check results
			output := w.Body.String()
			if tt.checkFunc != nil {
				tt.checkFunc(t, output)
			}
		})
	}
}

// TestCodexHelperFunctions tests various helper functions in codex_cc_support.go
func TestCodexHelperFunctions(t *testing.T) {
	t.Run("buildReverseToolNameMap", func(t *testing.T) {
		shortMap := map[string]string{
			"original_long_name": "short",
			"another_name":       "another",
		}

		reverseMap := buildReverseToolNameMap(shortMap)

		if len(reverseMap) != 2 {
			t.Errorf("expected 2 entries, got %d", len(reverseMap))
		}
		if reverseMap["short"] != "original_long_name" {
			t.Errorf("expected 'original_long_name', got %q", reverseMap["short"])
		}
		if reverseMap["another"] != "another_name" {
			t.Errorf("expected 'another_name', got %q", reverseMap["another"])
		}
	})

	t.Run("isCodexCCMode", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		// Test when not set
		if isCodexCCMode(c) {
			t.Error("expected false when ctxKeyCodexCC not set")
		}

		// Test when set to true
		c.Set(ctxKeyCodexCC, true)
		if !isCodexCCMode(c) {
			t.Error("expected true when ctxKeyCodexCC is true")
		}

		// Test when set to false
		c.Set(ctxKeyCodexCC, false)
		if isCodexCCMode(c) {
			t.Error("expected false when ctxKeyCodexCC is false")
		}
	})

	t.Run("getCodexToolNameReverseMap", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		// Test with no map set
		result := getCodexToolNameReverseMap(c)
		if result != nil {
			t.Error("expected nil when no map is set")
		}

		// Test with map set
		testMap := map[string]string{"short": "original"}
		c.Set(ctxKeyCodexToolNameReverseMap, testMap)
		result = getCodexToolNameReverseMap(c)
		if result == nil {
			t.Fatal("expected non-nil map")
		}
		if result["short"] != "original" {
			t.Errorf("expected 'original', got %q", result["short"])
		}
	})

	t.Run("extractToolResultContent", func(t *testing.T) {
		tests := []struct {
			name     string
			block    ClaudeContentBlock
			expected string
		}{
			{
				name: "string_content",
				block: ClaudeContentBlock{
					Type:    "tool_result",
					Content: json.RawMessage(`"result text"`),
				},
				expected: "result text",
			},
			{
				name: "array_content",
				block: ClaudeContentBlock{
					Type: "tool_result",
					Content: json.RawMessage(`[
						{"type": "text", "text": "first"},
						{"type": "text", "text": "second"}
					]`),
				},
				expected: "firstsecond",
			},
			{
				name: "windows_path",
				block: ClaudeContentBlock{
					Type:    "tool_result",
					Content: json.RawMessage(`"C:\\Users\\test\\file.txt"`),
				},
				expected: "C:/Users/test/file.txt",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := extractToolResultContent(tt.block)
				if result != tt.expected {
					t.Errorf("expected %q, got %q", tt.expected, result)
				}
			})
		}
	})
}

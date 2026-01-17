package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
	corruptedPath := "F:MyProjects\testlanguagepythonxxhello.py"
	expectedPath := "F:/MyProjectsestlanguagepythonxxhello.py"

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

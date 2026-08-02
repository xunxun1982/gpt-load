package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
)

func syncCodexCompatibilityHeaders(header http.Header, body []byte) {
	var envelope struct {
		ClientMetadata map[string]json.RawMessage `json:"client_metadata"`
	}
	if header == nil || json.Unmarshal(body, &envelope) != nil {
		return
	}

	turnMetadataJSON := rawJSONString(envelope.ClientMetadata, "x-codex-turn-metadata")
	var turnMetadata map[string]json.RawMessage
	if !json.Valid([]byte(turnMetadataJSON)) || json.Unmarshal([]byte(turnMetadataJSON), &turnMetadata) != nil {
		turnMetadataJSON = ""
		turnMetadata = nil
	}
	turnMetadataHeader := boundedCodexTurnMetadataHeader(turnMetadata, turnMetadataJSON)
	first := func(values ...string) string {
		for _, value := range values {
			if value != "" {
				return value
			}
		}
		return ""
	}

	syncCodexHeader(header, "X-Codex-Installation-Id", first(
		rawJSONString(turnMetadata, "installation_id"),
		rawJSONString(envelope.ClientMetadata, "x-codex-installation-id"),
	))
	syncCodexHeader(header, "Session-Id", first(
		rawJSONString(turnMetadata, "session_id"),
		rawJSONString(envelope.ClientMetadata, "session_id"),
	))
	syncCodexHeader(header, "Thread-Id", first(
		rawJSONString(turnMetadata, "thread_id"),
		rawJSONString(envelope.ClientMetadata, "thread_id"),
	))
	syncCodexHeader(header, "X-Codex-Window-Id", first(
		rawJSONString(turnMetadata, "window_id"),
		rawJSONString(envelope.ClientMetadata, "x-codex-window-id"),
	))
	syncCodexHeader(header, "X-Codex-Parent-Thread-Id", first(
		rawJSONString(turnMetadata, "parent_thread_id"),
		rawJSONString(envelope.ClientMetadata, "x-codex-parent-thread-id"),
	))
	syncCodexHeader(header, "X-OpenAI-Subagent", rawJSONString(envelope.ClientMetadata, "x-openai-subagent"))
	syncCodexHeader(header, "X-Codex-Turn-Metadata", turnMetadataHeader)
	header.Del("Session_ID")
	header.Del("Thread_ID")
}

func boundedCodexTurnMetadataHeader(metadata map[string]json.RawMessage, original string) string {
	if metadata == nil {
		return ""
	}
	if _, ok := metadata["code_mode_tool_names"]; !ok {
		return original
	}
	delete(metadata, "code_mode_tool_names")
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func rawJSONString(values map[string]json.RawMessage, key string) string {
	if values == nil {
		return ""
	}
	var value string
	if json.Unmarshal(values[key], &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func syncCodexHeader(header http.Header, name, value string) {
	if value == "" {
		header.Del(name)
		return
	}
	header.Set(name, value)
}

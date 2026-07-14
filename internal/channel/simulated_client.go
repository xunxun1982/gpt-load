package channel

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/google/uuid"
)

const (
	simulatedClientOff        = "off"
	simulatedClientCodex      = "codex"
	simulatedClientClaudeCode = "claude_code"
)

var simulatedClaudeCodeBetaTokens = []string{
	"claude-code-20250219",
	"interleaved-thinking-2025-05-14",
	"redact-thinking-2026-02-12",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"mid-conversation-system-2026-04-07",
	"effort-2025-11-24",
}

var simulatedOpenAIBetaTokens = []string{
	"responses=experimental",
}

func simulatedClientMode(group *models.Group) string {
	if group == nil || group.Config == nil {
		return simulatedClientOff
	}
	raw, ok := group.Config["simulated_client"]
	if !ok {
		return simulatedClientOff
	}
	mode, ok := raw.(string)
	if !ok {
		return simulatedClientOff
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", simulatedClientOff:
		return simulatedClientOff
	case simulatedClientCodex, simulatedClientClaudeCode:
		return mode
	default:
		return simulatedClientOff
	}
}

// IsSimulatedClientEnabled reports whether the group enables a simulated client preset.
func IsSimulatedClientEnabled(group *models.Group) bool {
	return simulatedClientMode(group) != simulatedClientOff
}

// ApplySimulatedClientHeaders applies the configured client fingerprint preset.
// Some official-client checks also require request-body metadata.
// It returns the rewritten body when metadata was added, or nil when the body was unchanged.
func ApplySimulatedClientHeaders(req *http.Request, group *models.Group, isStream bool) []byte {
	if req == nil {
		return nil
	}

	switch simulatedClientMode(group) {
	case simulatedClientCodex:
		return ApplyCodexCompatibleHeaders(req, group, isStream)
	case simulatedClientClaudeCode:
		// This preset targets the Anthropic Claude Code CLI signature.
		// The Claude Code Codex plugin uses a different Originator/User-Agent pair.
		version := simulatedClientVersion(group, "simulated_claude_code_version", DefaultClaudeCodeVersion)
		req.Header.Set("User-Agent", BuildClaudeCodeUserAgent(version))
		setHeaderIfMissing(req, "Accept", "application/json")
		setHeaderIfMissing(req, "Content-Type", "application/json")
		req.Header.Set("X-App", "cli")
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", mergeCommaHeaderTokens(req.Header.Get("anthropic-beta"), simulatedClaudeCodeBetaTokens))
		req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
		req.Header.Set("X-Stainless-Lang", "js")
		req.Header.Set("X-Stainless-Package-Version", "0.94.0")
		req.Header.Set("X-Stainless-OS", "Linux")
		req.Header.Set("X-Stainless-Arch", "arm64")
		req.Header.Set("X-Stainless-Runtime", "node")
		req.Header.Set("X-Stainless-Runtime-Version", "v24.3.0")
		req.Header.Set("X-Stainless-Retry-Count", "0")
		req.Header.Set("X-Stainless-Timeout", "600")
		return ensureClaudeCodeRequestIdentity(req, version)
	}
	return nil
}

// ApplyCodexCompatibleHeaders applies Codex-compatible headers for Responses compatibility paths.
// It returns the rewritten body when metadata was added, or nil when the body was unchanged.
func ApplyCodexCompatibleHeaders(req *http.Request, group *models.Group, isStream bool) []byte {
	if req == nil {
		return nil
	}

	version := simulatedClientVersion(group, "simulated_codex_version", DefaultCodexVersion)
	req.Header.Set("User-Agent", BuildCodexUserAgent(version))
	req.Header.Set("Version", version)
	req.Header.Set("originator", "codex-tui")
	req.Header.Set("OpenAI-Beta", mergeCommaHeaderTokens(req.Header.Get("OpenAI-Beta"), simulatedOpenAIBetaTokens))
	rewrittenBody := ensureCodexRequestIdentity(req)
	setHeaderIfMissing(req, "Content-Type", "application/json")
	if isStream {
		setHeaderIfMissing(req, "Accept", "text/event-stream")
	} else {
		setHeaderIfMissing(req, "Accept", "application/json")
	}
	return rewrittenBody
}

func ensureCodexRequestIdentity(req *http.Request) []byte {
	var payload map[string]any
	var clientMetadata map[string]any
	if isSimulatedCodexResponsesRequest(req) {
		payload, _ = readRequestJSONBody(req)
		clientMetadata, _ = payload["client_metadata"].(map[string]any)
	}
	if clientMetadata == nil {
		clientMetadata = make(map[string]any)
	}

	turnMetadata := make(map[string]any)
	turnMetadataJSON := firstNonEmptyString(
		strings.TrimSpace(req.Header.Get("X-Codex-Turn-Metadata")),
		jsonStringValue(clientMetadata["x-codex-turn-metadata"]),
	)
	if turnMetadataJSON != "" {
		metadataBytes := []byte(turnMetadataJSON)
		if json.Valid(metadataBytes) {
			decoder := json.NewDecoder(bytes.NewReader(metadataBytes))
			decoder.UseNumber()
			var decodedMetadata map[string]any
			if err := decoder.Decode(&decodedMetadata); err == nil && decodedMetadata != nil {
				turnMetadata = decodedMetadata
			}
		}
	}

	installationID := firstNonEmptyString(
		strings.TrimSpace(req.Header.Get("X-Codex-Installation-Id")),
		jsonStringValue(clientMetadata["x-codex-installation-id"]),
		jsonStringValue(turnMetadata["installation_id"]),
	)
	if installationID == "" {
		installationID = uuid.NewString()
	}
	sessionID := firstNonEmptyString(
		strings.TrimSpace(req.Header.Get("Session-Id")),
		strings.TrimSpace(req.Header.Get("Session_ID")),
		jsonStringValue(clientMetadata["session_id"]),
		jsonStringValue(turnMetadata["session_id"]),
	)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	threadID := firstNonEmptyString(
		strings.TrimSpace(req.Header.Get("Thread-Id")),
		strings.TrimSpace(req.Header.Get("Thread_ID")),
		jsonStringValue(clientMetadata["thread_id"]),
		jsonStringValue(turnMetadata["thread_id"]),
		strings.TrimSpace(req.Header.Get("x-client-request-id")),
	)
	if threadID == "" {
		threadID = uuid.NewString()
	}
	windowID := firstNonEmptyString(
		strings.TrimSpace(req.Header.Get("X-Codex-Window-Id")),
		jsonStringValue(clientMetadata["x-codex-window-id"]),
		jsonStringValue(turnMetadata["window_id"]),
	)
	if windowID == "" {
		windowID = uuid.NewString()
	}
	turnID := firstNonEmptyString(
		jsonStringValue(clientMetadata["turn_id"]),
		jsonStringValue(turnMetadata["turn_id"]),
	)
	if turnID == "" {
		turnID = uuid.NewString()
	}

	req.Header.Set("X-Codex-Installation-Id", installationID)
	setHeaderIfMissing(req, "Session-Id", sessionID)
	setHeaderIfMissing(req, "Thread-Id", threadID)
	req.Header.Set("x-client-request-id", threadID)
	req.Header.Set("X-Codex-Window-Id", windowID)

	turnMetadata["installation_id"] = installationID
	turnMetadata["session_id"] = sessionID
	turnMetadata["thread_id"] = threadID
	turnMetadata["turn_id"] = turnID
	turnMetadata["window_id"] = windowID
	if jsonStringValue(turnMetadata["request_kind"]) == "" {
		turnMetadata["request_kind"] = "turn"
	}
	encodedTurnMetadata, err := json.Marshal(turnMetadata)
	if err != nil {
		return nil
	}
	turnMetadataJSON = string(encodedTurnMetadata)
	req.Header.Set("X-Codex-Turn-Metadata", turnMetadataJSON)

	if payload == nil {
		return nil
	}
	clientMetadata["x-codex-installation-id"] = installationID
	clientMetadata["session_id"] = sessionID
	clientMetadata["thread_id"] = threadID
	clientMetadata["turn_id"] = turnID
	clientMetadata["x-codex-window-id"] = windowID
	clientMetadata["x-codex-turn-metadata"] = turnMetadataJSON
	payload["client_metadata"] = clientMetadata
	return replaceRequestJSONBody(req, payload)
}

func ensureClaudeCodeRequestIdentity(req *http.Request, version string) []byte {
	sessionID := strings.TrimSpace(req.Header.Get("X-Claude-Code-Session-Id"))
	var payload map[string]any
	if isSimulatedClaudeMessagesRequest(req) {
		payload, _ = readRequestJSONBody(req)
	}

	modified := false
	if payload != nil {
		metadata, ok := payload["metadata"].(map[string]any)
		if !ok {
			metadata = make(map[string]any)
			payload["metadata"] = metadata
			modified = true
		}
		userID, _ := metadata["user_id"].(string)
		userIDSessionID, validUserID := claudeCodeSessionIDFromUserID(userID)
		if validUserID {
			if sessionID == "" {
				sessionID = userIDSessionID
			}
		} else {
			if sessionID == "" {
				sessionID = uuid.NewString()
			}
			generatedUserID, err := buildClaudeCodeUserID(version, sessionID)
			if err == nil {
				metadata["user_id"] = generatedUserID
				modified = true
			}
		}
		if ensureClaudeCodeSystemPrompt(payload) {
			modified = true
		}
	}
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	setHeaderIfMissing(req, "X-Claude-Code-Session-Id", sessionID)
	if modified {
		return replaceRequestJSONBody(req, payload)
	}
	return nil
}

func ensureClaudeCodeSystemPrompt(payload map[string]any) bool {
	systemPromptBlock := map[string]any{
		"type": "text",
		"text": claudeCodeValidationSystemPrompt,
		"cache_control": map[string]any{
			"type": "ephemeral",
		},
	}
	switch system := payload["system"].(type) {
	case []any:
		for _, item := range system {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, _ := block["text"].(string)
			if strings.Contains(text, "Claude Code") && strings.Contains(text, "official CLI for Claude") {
				return false
			}
		}
		payload["system"] = append([]any{systemPromptBlock}, system...)
	case string:
		block := map[string]any{"type": "text", "text": system}
		if strings.Contains(system, "Claude Code") && strings.Contains(system, "official CLI for Claude") {
			payload["system"] = []any{block}
		} else {
			payload["system"] = []any{systemPromptBlock, block}
		}
	default:
		payload["system"] = []any{systemPromptBlock}
	}
	return true
}

func claudeCodeSessionIDFromUserID(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "{") {
		var metadata struct {
			DeviceID  string `json:"device_id"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return "", false
		}
		metadata.DeviceID = strings.TrimSpace(metadata.DeviceID)
		metadata.SessionID = strings.TrimSpace(metadata.SessionID)
		return metadata.SessionID, metadata.DeviceID != "" && metadata.SessionID != ""
	}
	if !strings.HasPrefix(raw, "user_") {
		return "", false
	}
	accountIndex := strings.Index(raw, "_account_")
	sessionIndex := strings.LastIndex(raw, "_session_")
	if accountIndex < len("user_") || sessionIndex < accountIndex+len("_account_") {
		return "", false
	}
	deviceID := raw[len("user_"):accountIndex]
	accountID := raw[accountIndex+len("_account_") : sessionIndex]
	sessionID := raw[sessionIndex+len("_session_"):]
	if len(deviceID) != 64 || len(sessionID) != 36 || !isHexOrHyphen(deviceID, false) || !isHexOrHyphen(accountID, true) || !isHexOrHyphen(sessionID, false) {
		return "", false
	}
	return sessionID, true
}

func isHexOrHyphen(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') && ch != '-' {
			return false
		}
	}
	return true
}

func isSimulatedCodexResponsesRequest(req *http.Request) bool {
	if req == nil || req.Method != http.MethodPost {
		return false
	}
	path := strings.TrimRight(req.URL.Path, "/")
	return strings.HasSuffix(path, "/responses")
}

func isSimulatedClaudeMessagesRequest(req *http.Request) bool {
	if req == nil || req.Method != http.MethodPost {
		return false
	}
	path := strings.TrimRight(req.URL.Path, "/")
	return strings.HasSuffix(path, "/messages")
}

func readRequestJSONBody(req *http.Request) (map[string]any, bool) {
	if req == nil || req.Body == nil {
		return nil, false
	}
	var (
		body       []byte
		err        error
		bodyReader io.ReadCloser
	)
	if req.GetBody != nil {
		bodyReader, err = req.GetBody()
		if err != nil {
			return nil, false
		}
		body, err = io.ReadAll(bodyReader)
		_ = bodyReader.Close()
	} else {
		body, err = io.ReadAll(req.Body)
	}
	if err != nil {
		return nil, false
	}
	replaceRequestBody(req, body)
	if len(body) == 0 || !json.Valid(body) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func replaceRequestJSONBody(req *http.Request, payload map[string]any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	replaceRequestBody(req, body)
	return body
}

func replaceRequestBody(req *http.Request, body []byte) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func jsonStringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// Media negotiation headers describe request semantics, so keep explicit passthrough values.
func setHeaderIfMissing(req *http.Request, key, value string) {
	if strings.TrimSpace(req.Header.Get(key)) == "" {
		req.Header.Set(key, value)
	}
}

func mergeCommaHeaderTokens(existing string, required []string) string {
	tokens := make([]string, 0, len(required)+4)
	seen := make(map[string]struct{}, len(required)+4)

	for _, token := range strings.Split(existing, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		key := strings.ToLower(token)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tokens = append(tokens, token)
	}

	for _, token := range required {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		key := strings.ToLower(token)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tokens = append(tokens, token)
	}

	return strings.Join(tokens, ",")
}

func simulatedClientVersion(group *models.Group, key, fallback string) string {
	if group == nil || group.Config == nil {
		return fallback
	}
	version, ok := group.Config[key].(string)
	if !ok {
		return fallback
	}
	version = strings.TrimSpace(version)
	if !utils.IsDottedNumericVersion(version) {
		return fallback
	}
	return version
}

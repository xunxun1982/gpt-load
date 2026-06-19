package channel

import (
	"net/http"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"
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

// ApplySimulatedClientHeaders applies the configured client fingerprint preset.
func ApplySimulatedClientHeaders(req *http.Request, group *models.Group, isStream bool) {
	if req == nil {
		return
	}

	switch simulatedClientMode(group) {
	case simulatedClientCodex:
		ApplyCodexCompatibleHeaders(req, group, isStream)
	case simulatedClientClaudeCode:
		req.Header.Set("User-Agent", BuildClaudeCodeUserAgent(simulatedClientVersion(group, "simulated_claude_code_version", DefaultClaudeCodeVersion)))
		setHeaderIfMissing(req, "Accept", "application/json")
		setHeaderIfMissing(req, "Content-Type", "application/json")
		req.Header.Set("X-App", "cli")
		req.Header.Set("anthropic-version", "2023-06-01")
		// Runtime session and transport negotiation headers are preserved from clients, not synthesized here.
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
	}
}

// ApplyCodexCompatibleHeaders applies Codex-compatible headers for Responses compatibility paths.
func ApplyCodexCompatibleHeaders(req *http.Request, group *models.Group, isStream bool) {
	if req == nil {
		return
	}

	version := simulatedClientVersion(group, "simulated_codex_version", DefaultCodexVersion)
	req.Header.Set("User-Agent", BuildCodexUserAgent(version))
	req.Header.Set("Version", version)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("OpenAI-Beta", mergeCommaHeaderTokens(req.Header.Get("OpenAI-Beta"), simulatedOpenAIBetaTokens))
	setHeaderIfMissing(req, "Content-Type", "application/json")
	if isStream {
		setHeaderIfMissing(req, "Accept", "text/event-stream")
	} else {
		setHeaderIfMissing(req, "Accept", "application/json")
	}
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

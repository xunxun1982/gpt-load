package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	requestmiddleware "gpt-load/internal/middleware"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
)

var errInvalidCodexStateDomain = errors.New("invalid Codex state domain")

func codexAffinityEnabled(c *gin.Context, group *models.Group) bool {
	return c != nil && c.Request != nil && group != nil &&
		c.Request.Method == http.MethodPost &&
		isOpenAIResponsesEndpoint(c.Request.URL.Path) &&
		(group.GroupType == "standard" || group.GroupType == "aggregate") &&
		group.ChannelType == "openai-response" &&
		getGroupConfigBool(group, "codex_affinity_enabled")
}

func codexAffinityKey(c *gin.Context, group *models.Group, body []byte) string {
	if !codexAffinityEnabled(c, group) {
		return ""
	}
	var payload map[string]any
	payloadOK := json.Unmarshal(body, &payload) == nil
	return codexAffinityKeyFromPayload(c, payload, payloadOK)
}

func codexAffinityKeyFromPayload(c *gin.Context, payload map[string]any, payloadOK bool) string {
	metadata, hasMetadata := payload["client_metadata"].(map[string]any)
	if payloadOK && hasMetadata {
		if value := codexTurnMetadataAffinityKey(stringFromJSONMap(metadata, "x-codex-turn-metadata")); value != "" {
			return value
		}
		if value := stringFromJSONMap(metadata, "thread_id"); value != "" {
			return value
		}
	}
	if value := codexTurnMetadataAffinityKey(firstNonEmptyHeader(c, "X-Codex-Turn-Metadata")); value != "" {
		return value
	}
	if value := firstNonEmptyHeader(c, "Thread-Id"); value != "" {
		return value
	}
	if payloadOK && hasMetadata {
		for _, key := range []string{"session_id", "x-codex-window-id"} {
			if value := stringFromJSONMap(metadata, key); value != "" {
				return value
			}
		}
	}
	if value := firstNonEmptyHeader(c, "Session-Id", "X-Client-Request-Id", "Session_ID", "session_id", "X-Session-ID", "x-session-id", "Conversation_ID", "conversation_id"); value != "" {
		return value
	}
	if value := firstNonEmptyHeader(c, "X-Codex-Window-Id", "x-codex-window-id"); value != "" {
		return value
	}
	if !payloadOK {
		return ""
	}
	return stringFromJSONMap(payload, "prompt_cache_key")
}

func codexAffinityRequestState(c *gin.Context, group *models.Group, body []byte) (string, string) {
	if !codexAffinityEnabled(c, group) {
		return "", ""
	}
	var payload map[string]any
	payloadOK := json.Unmarshal(body, &payload) == nil
	threadKey := codexAffinityKeyFromPayload(c, payload, payloadOK)
	digest := requestmiddleware.AuthenticatedProxyIdentityDigest(c)
	return codexAffinityScopedCacheKey(group.ID, digest, threadKey), codexStateDomainKeyFromPayload(c, payload, payloadOK)
}

func codexEncryptedReasoningAllowed(retryCtx *retryContext) bool {
	if retryCtx == nil || !retryCtx.codexAffinityEnabled {
		return true
	}
	return retryCtx.codexAffinityUsingCached && !retryCtx.codexAffinityDegraded && !retryCtx.codexStateResetRequired
}

func sanitizeCodexIdentityChange(c *gin.Context, body []byte, targetGroup *models.Group, allowEncryptedReasoning bool) ([]byte, error) {
	if c != nil && c.Request != nil {
		c.Request.Header.Del("X-Codex-Turn-State")
	}
	supportsEncryptedReasoning := allowEncryptedReasoning && targetGroup != nil && strings.EqualFold(targetGroup.ChannelType, "openai-response")
	sanitized, _, err := sanitizeCodexStateDomain(body, supportsEncryptedReasoning)
	if err != nil {
		return body, errors.Join(errInvalidCodexStateDomain, err)
	}
	if c != nil && c.Request != nil {
		syncCodexCompatibilityHeaders(c.Request.Header, sanitized)
	}
	return sanitized, nil
}

func removeCodexTurnStateBeforeSend(req *http.Request, resetState bool) {
	if req != nil && resetState {
		req.Header.Del("X-Codex-Turn-State")
	}
}

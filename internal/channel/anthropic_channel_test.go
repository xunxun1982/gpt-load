package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

func TestClaudeCodeUserAgent(t *testing.T) {
	t.Parallel()

	// Test that the constant is defined and has the expected format
	assert.NotEmpty(t, ClaudeCodeUserAgent)
	assert.Equal(t, BuildClaudeCodeUserAgent(DefaultClaudeCodeVersion), ClaudeCodeUserAgent)
	assert.Contains(t, ClaudeCodeUserAgent, "claude-cli")
	assert.Contains(t, ClaudeCodeUserAgent, DefaultClaudeCodeVersion)
	assert.Regexp(t, `claude-cli/\d+\.\d+\.\d+`, ClaudeCodeUserAgent)
	assert.Contains(t, ClaudeCodeUserAgent, "external")
	assert.Contains(t, ClaudeCodeUserAgent, "cli")
	assert.Equal(t, 1, strings.Count(ClaudeCodeUserAgent, "(external, cli)"))
}

func TestAnthropicChannel_ModifyRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		existingHeaders       map[string]string
		expectedAuthHeader    string
		expectedXApiKeyHeader string
		expectedVersionHeader string
		shouldPreserveVersion bool
		shouldPreserveBeta    bool
	}{
		{
			name:                  "Sets default headers",
			existingHeaders:       map[string]string{},
			expectedAuthHeader:    "Bearer test-key",
			expectedXApiKeyHeader: "test-key",
			expectedVersionHeader: "2023-06-01",
			shouldPreserveVersion: false,
		},
		{
			name: "Preserves client anthropic-version",
			existingHeaders: map[string]string{
				"anthropic-version": "2024-01-01",
			},
			expectedAuthHeader:    "Bearer test-key",
			expectedXApiKeyHeader: "test-key",
			expectedVersionHeader: "2024-01-01",
			shouldPreserveVersion: true,
		},
		{
			name: "Preserves anthropic-beta header",
			existingHeaders: map[string]string{
				"anthropic-beta": "extended-thinking-2024-12-12",
			},
			expectedAuthHeader:    "Bearer test-key",
			expectedXApiKeyHeader: "test-key",
			expectedVersionHeader: "2023-06-01",
			shouldPreserveBeta:    true,
		},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable for parallel subtests
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock channel
			ch := &AnthropicChannel{
				BaseChannel: &BaseChannel{},
			}

			// Create a test request
			req := httptest.NewRequest("POST", "/v1/messages", nil)
			for k, v := range tt.existingHeaders {
				req.Header.Set(k, v)
			}

			// Create a test API key
			apiKey := &models.APIKey{
				KeyValue: "test-key",
			}

			// Call ModifyRequest
			ch.ModifyRequest(req, apiKey, &models.Group{})

			// Verify headers
			assert.Equal(t, tt.expectedAuthHeader, req.Header.Get("Authorization"))
			assert.Equal(t, tt.expectedXApiKeyHeader, req.Header.Get("x-api-key"))

			if tt.shouldPreserveVersion {
				assert.Equal(t, tt.expectedVersionHeader, req.Header.Get("anthropic-version"))
			} else {
				assert.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
			}

			if tt.shouldPreserveBeta {
				// Assert exact header value instead of just NotEmpty
				assert.Equal(t, tt.existingHeaders["anthropic-beta"], req.Header.Get("anthropic-beta"))
			}
		})
	}
}

func TestAnthropicChannel_ModifyRequest_DualAuth(t *testing.T) {
	t.Parallel()

	// Test that both Authorization and x-api-key headers are set
	ch := &AnthropicChannel{
		BaseChannel: &BaseChannel{},
	}

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	apiKey := &models.APIKey{
		KeyValue: "test-key-123",
	}

	ch.ModifyRequest(req, apiKey, &models.Group{})

	// Both headers should be set with the same key
	assert.Equal(t, "Bearer test-key-123", req.Header.Get("Authorization"))
	assert.Equal(t, "test-key-123", req.Header.Get("x-api-key"))
}

func TestAnthropicChannel_ValidateKey_StreamAndPromptQueue(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))

		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, true, body["stream"])
		messages, ok := body["messages"].([]any)
		if assert.True(t, ok) && assert.NotEmpty(t, messages) {
			message, ok := messages[0].(map[string]any)
			if assert.True(t, ok) {
				content, ok := message["content"].(string)
				assert.True(t, ok)
				assert.NotEqual(t, validationDefaultPrompt, content)
				assert.True(t, validationPromptInQueue(content))
				assert.LessOrEqual(t, utf8.RuneCountInString(content), 8)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ch := &AnthropicChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/messages",
			TestModel:          "claude-3-haiku-20240307",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100},
			},
		},
	}

	valid, err := ch.ValidateKey(context.Background(), &models.APIKey{KeyValue: "test-key"}, &models.Group{
		Config: datatypes.JSONMap{
			"validation_stream":      true,
			"validation_prompt_mode": "random_queue",
		},
	})
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestAnthropicChannel_ValidateKey_AppliesSimulatedClaudeCodeClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, BuildClaudeCodeUserAgent("2.1.183"), r.Header.Get("User-Agent"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "cli", r.Header.Get("X-App"))
		assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		assert.Contains(t, r.Header.Get("anthropic-beta"), "claude-code-20250219")
		assert.Contains(t, r.Header.Get("anthropic-beta"), "redact-thinking-2026-02-12")
		assert.Equal(t, "true", r.Header.Get("Anthropic-Dangerous-Direct-Browser-Access"))
		assert.Equal(t, "js", r.Header.Get("X-Stainless-Lang"))
		assert.Equal(t, "node", r.Header.Get("X-Stainless-Runtime"))
		assert.Equal(t, "Linux", r.Header.Get("X-Stainless-OS"))
		assert.Equal(t, "arm64", r.Header.Get("X-Stainless-Arch"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))

		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, true, body["stream"])
		assert.Equal(t, float64(1), body["temperature"])
		metadata, ok := body["metadata"].(map[string]any)
		if assert.True(t, ok) {
			userID, ok := metadata["user_id"].(string)
			assert.True(t, ok)
			assert.NotEmpty(t, userID)
		}
		system, ok := body["system"].([]any)
		if assert.True(t, ok) && assert.NotEmpty(t, system) {
			item, ok := system[0].(map[string]any)
			if assert.True(t, ok) {
				assert.Equal(t, "text", item["type"])
				assert.NotEmpty(t, item["text"])
			}
		}
		messages, ok := body["messages"].([]any)
		if assert.True(t, ok) && assert.NotEmpty(t, messages) {
			message, ok := messages[0].(map[string]any)
			if assert.True(t, ok) {
				content, ok := message["content"].([]any)
				if assert.True(t, ok) && assert.NotEmpty(t, content) {
					part, ok := content[0].(map[string]any)
					if assert.True(t, ok) {
						assert.Equal(t, "text", part["type"])
						cacheControl, ok := part["cache_control"].(map[string]any)
						if assert.True(t, ok) {
							assert.Equal(t, "ephemeral", cacheControl["type"])
						}
					}
				}
			}
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ch := &AnthropicChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/messages",
			TestModel:          "claude-3-haiku-20240307",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(context.Background(), &models.APIKey{KeyValue: "test-key"}, &models.Group{
		Config: datatypes.JSONMap{
			"simulated_client":              "claude_code",
			"simulated_claude_code_version": "2.1.183",
			"validation_stream":             true,
		},
	})
	assert.NoError(t, err)
	assert.True(t, valid)
}

// Benchmark tests
func BenchmarkAnthropicChannel_ModifyRequest(b *testing.B) {
	b.ReportAllocs()

	ch := &AnthropicChannel{
		BaseChannel: &BaseChannel{},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-key",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", nil)
		ch.ModifyRequest(req, apiKey, &models.Group{})
	}
}

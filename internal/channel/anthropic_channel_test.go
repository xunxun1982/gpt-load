package channel

import (
	"net/http/httptest"
	"testing"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/assert"
)

func TestClaudeCodeUserAgent(t *testing.T) {
	t.Parallel()

	// Test that the constant is defined and has the expected format
	assert.NotEmpty(t, ClaudeCodeUserAgent)
	assert.Contains(t, ClaudeCodeUserAgent, "claude-cli")
	assert.Contains(t, ClaudeCodeUserAgent, "external")
	assert.Contains(t, ClaudeCodeUserAgent, "cli")
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

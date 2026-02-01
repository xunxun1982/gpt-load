package channel

import (
	"net/http/httptest"
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestCodexUserAgent(t *testing.T) {
	t.Parallel()

	// Test that the constant is defined and has the expected format
	assert.NotEmpty(t, CodexUserAgent)
	assert.Contains(t, CodexUserAgent, "codex-cli")
	assert.Contains(t, CodexUserAgent, "0.77.0")
}

func TestCodexChannel_ModifyRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		existingHeaders    map[string]string
		expectedAuthHeader string
	}{
		{
			name:               "Sets Authorization header",
			existingHeaders:    map[string]string{},
			expectedAuthHeader: "Bearer test-key",
		},
		{
			name: "Overwrites existing Authorization header",
			existingHeaders: map[string]string{
				"Authorization": "Bearer old-key",
			},
			expectedAuthHeader: "Bearer test-key",
		},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable for parallel subtests
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a mock channel
			ch := &CodexChannel{
				BaseChannel: &BaseChannel{},
			}

			// Create a test request
			req := httptest.NewRequest("POST", "/v1/responses", nil)
			for k, v := range tt.existingHeaders {
				req.Header.Set(k, v)
			}

			// Create a test API key
			apiKey := &models.APIKey{
				KeyValue: "test-key",
			}

			// Call ModifyRequest
			ch.ModifyRequest(req, apiKey, &models.Group{})

			// Verify Authorization header
			assert.Equal(t, tt.expectedAuthHeader, req.Header.Get("Authorization"))
		})
	}
}

func TestCodexChannel_IsStreamRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		acceptHeader   string
		queryParam     string
		bodyJSON       string
		expectedResult bool
	}{
		{
			name:           "Stream via Accept header",
			acceptHeader:   "text/event-stream",
			queryParam:     "",
			bodyJSON:       `{}`,
			expectedResult: true,
		},
		{
			name:           "Stream via query parameter",
			acceptHeader:   "",
			queryParam:     "true",
			bodyJSON:       `{}`,
			expectedResult: true,
		},
		{
			name:           "Stream via JSON body",
			acceptHeader:   "",
			queryParam:     "",
			bodyJSON:       `{"stream": true}`,
			expectedResult: true,
		},
		{
			name:           "Non-streaming request",
			acceptHeader:   "",
			queryParam:     "",
			bodyJSON:       `{"stream": false}`,
			expectedResult: false,
		},
		{
			name:           "No stream indicator",
			acceptHeader:   "",
			queryParam:     "",
			bodyJSON:       `{}`,
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &CodexChannel{
				BaseChannel: &BaseChannel{},
			}

			// Create mock Gin context
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

			// Set headers and query params
			if tt.acceptHeader != "" {
				c.Request.Header.Set("Accept", tt.acceptHeader)
			}
			if tt.queryParam != "" {
				c.Request.URL.RawQuery = "stream=" + tt.queryParam
			}

			result := ch.IsStreamRequest(c, []byte(tt.bodyJSON))
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestCodexChannel_ExtractModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		bodyJSON       string
		expectedResult string
	}{
		{
			name:           "Valid model in body",
			bodyJSON:       `{"model": "gpt-5.2-codex"}`,
			expectedResult: "gpt-5.2-codex",
		},
		{
			name:           "No model in body",
			bodyJSON:       `{}`,
			expectedResult: "",
		},
		{
			name:           "Invalid JSON",
			bodyJSON:       `{invalid}`,
			expectedResult: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &CodexChannel{
				BaseChannel: &BaseChannel{},
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

			result := ch.ExtractModel(c, []byte(tt.bodyJSON))
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestForceStreamRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		inputBody           string
		expectedModified    bool
		expectedStreamValue bool
		shouldContainStream bool
	}{
		{
			name:                "Non-streaming request becomes streaming",
			inputBody:           `{"model": "gpt-5.2-codex", "input": "test"}`,
			expectedModified:    true,
			expectedStreamValue: true,
			shouldContainStream: true,
		},
		{
			name:                "Already streaming request unchanged",
			inputBody:           `{"model": "gpt-5.2-codex", "stream": true}`,
			expectedModified:    false,
			expectedStreamValue: true,
			shouldContainStream: true,
		},
		{
			name:                "Empty body unchanged",
			inputBody:           ``,
			expectedModified:    false,
			expectedStreamValue: false,
			shouldContainStream: false,
		},
		{
			name:                "Invalid JSON unchanged",
			inputBody:           `{invalid}`,
			expectedModified:    false,
			expectedStreamValue: false,
			shouldContainStream: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			modifiedBody, wasModified := ForceStreamRequest([]byte(tt.inputBody))
			assert.Equal(t, tt.expectedModified, wasModified)

			if tt.shouldContainStream {
				// Check for stream field with flexible whitespace matching
				assert.Contains(t, string(modifiedBody), `"stream"`)
				assert.Contains(t, string(modifiedBody), `true`)
			}
		})
	}
}

// Benchmark tests
func BenchmarkCodexChannel_ModifyRequest(b *testing.B) {
	b.ReportAllocs()

	ch := &CodexChannel{
		BaseChannel: &BaseChannel{},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-key",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1/responses", nil)
		ch.ModifyRequest(req, apiKey, &models.Group{})
	}
}

func BenchmarkForceStreamRequest(b *testing.B) {
	b.ReportAllocs()

	body := []byte(`{"model": "gpt-5.2-codex", "input": "test message"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ForceStreamRequest(body)
	}
}

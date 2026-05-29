package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/assert"
)

func TestOpenAIChannel_ModifyRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		existingHeaders    map[string]string
		apiKeyValue        string
		expectedAuthHeader string
	}{
		{
			name:               "Sets Authorization header",
			existingHeaders:    map[string]string{},
			apiKeyValue:        "test-key-123",
			expectedAuthHeader: "Bearer test-key-123",
		},
		{
			name: "Overwrites existing Authorization header",
			existingHeaders: map[string]string{
				"Authorization": "Bearer old-key",
			},
			apiKeyValue:        "new-key-456",
			expectedAuthHeader: "Bearer new-key-456",
		},
		{
			name: "Preserves other headers",
			existingHeaders: map[string]string{
				"Content-Type": "application/json",
				"User-Agent":   "test-agent",
			},
			apiKeyValue:        "test-key-789",
			expectedAuthHeader: "Bearer test-key-789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &OpenAIChannel{
				BaseChannel: &BaseChannel{},
			}

			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			for k, v := range tt.existingHeaders {
				req.Header.Set(k, v)
			}

			apiKey := &models.APIKey{
				KeyValue: tt.apiKeyValue,
			}

			ch.ModifyRequest(req, apiKey, &models.Group{})

			assert.Equal(t, tt.expectedAuthHeader, req.Header.Get("Authorization"))

			// Verify other headers are preserved
			for k, v := range tt.existingHeaders {
				if k != "Authorization" {
					assert.Equal(t, v, req.Header.Get(k))
				}
			}
		})
	}
}

func TestOpenAIChannel_ValidateKey_Success(t *testing.T) {
	t.Parallel()

	// Create mock server that returns success
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request format
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "/v1/chat/completions")
		assert.Equal(t, "Bearer test-valid-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Verify request body
		var body map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, "gpt-3.5-turbo", body["model"])
		assert.NotNil(t, body["messages"])

		// Return success response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hello"}}]}`))
	}))
	defer server.Close()

	ch := &OpenAIChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/chat/completions",
			TestModel:          "gpt-3.5-turbo",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100},
			},
		},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-valid-key",
	}

	group := &models.Group{}

	valid, err := ch.ValidateKey(context.Background(), apiKey, group)
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIChannel_ValidateKey_InvalidKey(t *testing.T) {
	t.Parallel()

	// Create mock server that returns 401 Unauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	ch := &OpenAIChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/chat/completions",
			TestModel:          "gpt-3.5-turbo",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100},
			},
		},
	}

	apiKey := &models.APIKey{
		KeyValue: "invalid-key",
	}

	group := &models.Group{}

	valid, err := ch.ValidateKey(context.Background(), apiKey, group)
	assert.Error(t, err)
	assert.False(t, valid)
	assert.Contains(t, err.Error(), "401")
}

func TestOpenAIChannel_ValidateKey_ServerError(t *testing.T) {
	t.Parallel()

	// Create mock server that returns 500 Internal Server Error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"Internal server error","type":"server_error"}}`))
	}))
	defer server.Close()

	ch := &OpenAIChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/chat/completions",
			TestModel:          "gpt-3.5-turbo",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100},
			},
		},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-key",
	}

	group := &models.Group{}

	valid, err := ch.ValidateKey(context.Background(), apiKey, group)
	assert.Error(t, err)
	assert.False(t, valid)
	assert.Contains(t, err.Error(), "500")
}

func TestOpenAIChannel_ValidateKey_NoUpstream(t *testing.T) {
	t.Parallel()

	ch := &OpenAIChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/chat/completions",
			TestModel:          "gpt-3.5-turbo",
			Upstreams:          []UpstreamInfo{}, // No upstreams
		},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-key",
	}

	group := &models.Group{}

	valid, err := ch.ValidateKey(context.Background(), apiKey, group)
	assert.Error(t, err)
	assert.False(t, valid)
	assert.Contains(t, err.Error(), "failed to select validation upstream")
}

// Benchmark tests
func BenchmarkOpenAIChannel_ModifyRequest(b *testing.B) {
	b.ReportAllocs()

	ch := &OpenAIChannel{
		BaseChannel: &BaseChannel{},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-key",
	}

	group := &models.Group{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		ch.ModifyRequest(req, apiKey, group)
	}
}

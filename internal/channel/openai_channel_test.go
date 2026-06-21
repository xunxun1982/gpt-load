package channel

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"

	"gpt-load/internal/models"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
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

func TestOpenAIChannel_ValidateKey_RejectsUnreadableSuccessBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("��x�(.4�N_`�л��=%��8�L#����?�'�W"))
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

	valid, err := ch.ValidateKey(context.Background(), &models.APIKey{KeyValue: "test-key"}, &models.Group{})

	assert.False(t, valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation response")
	assert.NotContains(t, err.Error(), "�")
}

func TestOpenAIChannel_ValidateKey_StreamAndPromptQueue(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, true, body["stream"])
		messages, ok := body["messages"].([]interface{})
		if assert.True(t, ok) && assert.NotEmpty(t, messages) {
			message, ok := messages[0].(map[string]interface{})
			if assert.True(t, ok) {
				content, ok := message["content"].(string)
				assert.True(t, ok)
				assert.NotEqual(t, validationDefaultPrompt, content)
				assert.True(t, validationPromptInQueue(content))
				assert.LessOrEqual(t, utf8.RuneCountInString(content), 8)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk"}` + "\n\n"))
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

	valid, err := ch.ValidateKey(context.Background(), &models.APIKey{KeyValue: "test-key"}, &models.Group{
		Config: datatypes.JSONMap{
			"validation_stream":      true,
			"validation_prompt_mode": "random_queue",
		},
	})
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIChannel_ValidateKey_ForceStreamControlsValidationStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, true, body["stream"])
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk"}` + "\n\n"))
	}))
	defer server.Close()

	ch := &OpenAIChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/chat/completions",
			TestModel:          "gpt-3.5-turbo",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(context.Background(), &models.APIKey{KeyValue: "test-key"}, &models.Group{
		Config: datatypes.JSONMap{"force_stream": true},
	})
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIChannel_ValidateKey_AppliesSimulatedCodexClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, BuildCodexUserAgent("0.150.1"), r.Header.Get("User-Agent"))
		assert.Equal(t, "0.150.1", r.Header.Get("Version"))
		assert.Equal(t, "codex_cli_rs", r.Header.Get("originator"))
		assert.Equal(t, "responses=experimental", r.Header.Get("OpenAI-Beta"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"test","object":"chat.completion"}`))
	}))
	defer server.Close()

	ch := &OpenAIChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/chat/completions",
			TestModel:          "gpt-3.5-turbo",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(context.Background(), &models.APIKey{KeyValue: "test-key"}, &models.Group{
		Config: datatypes.JSONMap{
			"simulated_client":        "codex",
			"simulated_codex_version": "0.150.1",
		},
	})
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

func TestOpenAIChannel_ValidateKey_DecodesCompressedErrorBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		encoding string
		compress func(t *testing.T, body []byte) []byte
	}{
		{
			name:     "gzip",
			encoding: "gzip",
			compress: compressGzipForValidationTest,
		},
		{
			name:     "brotli",
			encoding: "br",
			compress: compressBrotliForValidationTest,
		},
		{
			name:     "zstd",
			encoding: "zstd",
			compress: compressZstdForValidationTest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rawBody := []byte(`{"error":{"message":"upstream rejected simulated codex client","type":"forbidden"}}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", tt.encoding)
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write(tt.compress(t, rawBody))
			}))
			defer server.Close()

			ch := &OpenAIChannel{
				BaseChannel: &BaseChannel{
					ValidationEndpoint: "/v1/chat/completions",
					TestModel:          "gpt-3.5-turbo",
					HTTPClient: &http.Client{
						Transport: &http.Transport{DisableCompression: true},
					},
					Upstreams: []UpstreamInfo{
						{
							URL:    mustParseURL(server.URL),
							Weight: 100,
							HTTPClient: &http.Client{
								Transport: &http.Transport{DisableCompression: true},
							},
						},
					},
				},
			}

			valid, err := ch.ValidateKey(context.Background(), &models.APIKey{KeyValue: "test-key"}, &models.Group{})
			assert.False(t, valid)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "[status 403]")
			assert.Contains(t, err.Error(), "upstream rejected simulated codex client")
		})
	}
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

func compressGzipForValidationTest(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, err := writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

func compressBrotliForValidationTest(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := brotli.NewWriter(&buf)
	_, err := writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

func compressZstdForValidationTest(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

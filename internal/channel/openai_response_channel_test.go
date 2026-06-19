package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

func TestCodexUserAgent(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, CodexUserAgent)
	assert.Equal(t, BuildCodexUserAgent(DefaultCodexVersion), CodexUserAgent)
	assert.Contains(t, CodexUserAgent, "codex_cli_rs/"+DefaultCodexVersion)
	assert.Contains(t, CodexUserAgent, "xterm-256color")
}

func TestOpenAIResponseChannel_ModifyRequest(t *testing.T) {
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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &OpenAIResponseChannel{
				BaseChannel: &BaseChannel{},
			}

			req := httptest.NewRequest("POST", "/v1/responses", nil)
			for k, v := range tt.existingHeaders {
				req.Header.Set(k, v)
			}

			apiKey := &models.APIKey{
				KeyValue: "test-key",
			}

			ch.ModifyRequest(req, apiKey, &models.Group{})
			assert.Equal(t, tt.expectedAuthHeader, req.Header.Get("Authorization"))
		})
	}
}

func TestOpenAIResponseChannel_ValidateKey_ValidKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses", r.URL.Path)
		assert.Equal(t, "Bearer test-valid-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "gpt-5.2-codex", body["model"])
		assert.Equal(t, "hi", body["input"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response"}`))
	}))
	defer server.Close()

	ch := &OpenAIResponseChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/responses",
			TestModel:          "gpt-5.2-codex",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(
		context.Background(),
		&models.APIKey{KeyValue: "test-valid-key"},
		&models.Group{Name: "responses-group"},
	)
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIResponseChannel_ValidateKey_StreamPromptQueueAndInclude(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, true, body["stream"])
		input, ok := body["input"].(string)
		if assert.True(t, ok) {
			assert.NotEqual(t, validationDefaultPrompt, input)
			assert.True(t, validationPromptInQueue(input))
			assert.LessOrEqual(t, utf8.RuneCountInString(input), 8)
		}
		include, ok := body["include"].([]any)
		if assert.True(t, ok) {
			assert.Contains(t, include, "reasoning.encrypted_content")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response"}`))
	}))
	defer server.Close()

	ch := &OpenAIResponseChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/responses",
			TestModel:          "gpt-5.2-codex",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(
		context.Background(),
		&models.APIKey{KeyValue: "test-key"},
		&models.Group{
			Name: "responses-group",
			Config: datatypes.JSONMap{
				"validation_stream":                     true,
				"validation_prompt_mode":                "random_queue",
				"responses_include_encrypted_reasoning": true,
			},
		},
	)
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIResponseChannel_ValidateKey_AppliesSimulatedCodexClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, BuildCodexUserAgent("0.150.1"), r.Header.Get("User-Agent"))
		assert.Equal(t, "0.150.1", r.Header.Get("Version"))
		assert.Equal(t, "codex_cli_rs", r.Header.Get("Originator"))
		assert.Equal(t, "terminal_resize_reflow", r.Header.Get("X-Codex-Beta-Features"))
		assert.NotEmpty(t, r.Header.Get("X-Codex-Turn-Metadata"))
		assert.NotEmpty(t, r.Header.Get("X-Codex-Window-Id"))
		assert.Equal(t, "responses=experimental", r.Header.Get("OpenAI-Beta"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response"}`))
	}))
	defer server.Close()

	ch := &OpenAIResponseChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/responses",
			TestModel:          "gpt-5.2-codex",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(
		context.Background(),
		&models.APIKey{KeyValue: "test-key"},
		&models.Group{
			Name: "responses-group",
			Config: datatypes.JSONMap{
				"simulated_client":        "codex",
				"simulated_codex_version": "0.150.1",
			},
		},
	)
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIResponseChannel_ValidateKey_UsesCompactProbeForSimulatedCodex(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses/compact", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "codex_cli_rs", r.Header.Get("Originator"))
		assert.NotEmpty(t, r.Header.Get("Session_ID"))
		assert.Equal(t, r.Header.Get("Session_ID"), r.Header.Get("Conversation_ID"))

		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "gpt-5.2-codex", body["model"])
		assert.Equal(t, "You are a helpful coding assistant.", body["instructions"])
		input, ok := body["input"].([]any)
		if assert.True(t, ok) && assert.Len(t, input, 1) {
			item, ok := input[0].(map[string]any)
			if assert.True(t, ok) {
				assert.Equal(t, "message", item["type"])
				assert.Equal(t, "user", item["role"])
				content, ok := item["content"].([]any)
				if assert.True(t, ok) && assert.Len(t, content, 1) {
					part, ok := content[0].(map[string]any)
					if assert.True(t, ok) {
						assert.Equal(t, "input_text", part["type"])
						assert.Equal(t, "Respond with OK.", part["text"])
					}
				}
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response"}`))
	}))
	defer server.Close()

	ch := &OpenAIResponseChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/responses",
			TestModel:          "gpt-5.2-codex",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(
		context.Background(),
		&models.APIKey{KeyValue: "test-key"},
		&models.Group{
			Name: "responses-group",
			Config: datatypes.JSONMap{
				"simulated_client":        "codex",
				"simulated_codex_version": "0.150.1",
			},
		},
	)
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIResponseChannel_ValidateKey_KeepsExistingCompactProbePath(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses/compact", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response"}`))
	}))
	defer server.Close()

	ch := &OpenAIResponseChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/responses/compact/",
			TestModel:          "gpt-5.2-codex",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(
		context.Background(),
		&models.APIKey{KeyValue: "test-key"},
		&models.Group{
			Name: "responses-group",
			Config: datatypes.JSONMap{
				"simulated_client": "codex",
			},
		},
	)
	assert.NoError(t, err)
	assert.True(t, valid)
}

func TestOpenAIResponseChannel_ValidateKey_InvalidKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	ch := &OpenAIResponseChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/responses",
			TestModel:          "gpt-5.2-codex",
			HTTPClient:         server.Client(),
			Upstreams: []UpstreamInfo{
				{URL: mustParseURL(server.URL), Weight: 100, HTTPClient: server.Client()},
			},
		},
	}

	valid, err := ch.ValidateKey(
		context.Background(),
		&models.APIKey{KeyValue: "invalid-key"},
		&models.Group{Name: "responses-group"},
	)
	assert.Error(t, err)
	assert.False(t, valid)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "invalid key")
}

func TestOpenAIResponseChannel_ValidateKey_NoUpstream(t *testing.T) {
	t.Parallel()

	ch := &OpenAIResponseChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1/responses",
			TestModel:          "gpt-5.2-codex",
			Upstreams:          []UpstreamInfo{},
		},
	}

	valid, err := ch.ValidateKey(
		context.Background(),
		&models.APIKey{KeyValue: "test-key"},
		&models.Group{Name: "responses-group"},
	)
	assert.Error(t, err)
	assert.False(t, valid)
	assert.Contains(t, err.Error(), "failed to select validation upstream")
}

func TestOpenAIResponseChannel_IsStreamRequest(t *testing.T) {
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

			ch := &OpenAIResponseChannel{
				BaseChannel: &BaseChannel{},
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

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

func TestOpenAIResponseChannel_ExtractModel(t *testing.T) {
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

			ch := &OpenAIResponseChannel{
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
				assert.Contains(t, string(modifiedBody), `"stream"`)
				assert.Contains(t, string(modifiedBody), `true`)
			}
		})
	}
}

func BenchmarkOpenAIResponseChannel_ModifyRequest(b *testing.B) {
	b.ReportAllocs()

	ch := &OpenAIResponseChannel{
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

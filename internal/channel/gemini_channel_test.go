package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiChannel_ModifyRequest_NativeFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		path                 string
		apiKeyValue          string
		expectedQueryKey     string
		expectedHeaderKey    string
		shouldHaveAuthHeader bool
	}{
		{
			name:                 "Native format sets key parameter and header",
			path:                 "/v1beta/models/gemini-pro:generateContent",
			apiKeyValue:          "test-key-123",
			expectedQueryKey:     "test-key-123",
			expectedHeaderKey:    "test-key-123",
			shouldHaveAuthHeader: false,
		},
		{
			name:                 "Native format with different key",
			path:                 "/v1/models/gemini-1.5-pro:streamGenerateContent",
			apiKeyValue:          "another-key-456",
			expectedQueryKey:     "another-key-456",
			expectedHeaderKey:    "another-key-456",
			shouldHaveAuthHeader: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &GeminiChannel{
				BaseChannel: &BaseChannel{},
			}

			req := httptest.NewRequest("POST", tt.path, nil)
			apiKey := &models.APIKey{
				KeyValue: tt.apiKeyValue,
			}

			ch.ModifyRequest(req, apiKey, &models.Group{})

			// Verify query parameter
			assert.Equal(t, tt.expectedQueryKey, req.URL.Query().Get("key"))

			// Verify x-goog-api-key header
			assert.Equal(t, tt.expectedHeaderKey, req.Header.Get("x-goog-api-key"))

			// Verify no Bearer token for native format
			if !tt.shouldHaveAuthHeader {
				assert.Empty(t, req.Header.Get("Authorization"))
			}
		})
	}
}

func TestGeminiChannel_ModifyRequest_OpenAICompatible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		path               string
		apiKeyValue        string
		expectedAuthHeader string
		shouldHaveQueryKey bool
	}{
		{
			name:               "OpenAI-compatible format uses Bearer token",
			path:               "/v1beta/openai/chat/completions",
			apiKeyValue:        "test-key-789",
			expectedAuthHeader: "Bearer test-key-789",
			shouldHaveQueryKey: false,
		},
		{
			name:               "OpenAI-compatible with different path",
			path:               "/v1beta/openai/models",
			apiKeyValue:        "openai-key-123",
			expectedAuthHeader: "Bearer openai-key-123",
			shouldHaveQueryKey: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &GeminiChannel{
				BaseChannel: &BaseChannel{},
			}

			req := httptest.NewRequest("POST", tt.path, nil)
			apiKey := &models.APIKey{
				KeyValue: tt.apiKeyValue,
			}

			ch.ModifyRequest(req, apiKey, &models.Group{})

			// Verify Authorization header
			assert.Equal(t, tt.expectedAuthHeader, req.Header.Get("Authorization"))

			// Verify no query parameter for OpenAI-compatible format
			if !tt.shouldHaveQueryKey {
				assert.Empty(t, req.URL.Query().Get("key"))
			}
		})
	}
}

func TestGeminiChannel_IsStreamRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		path           string
		bodyJSON       string
		expectedResult bool
	}{
		{
			name:           "Stream via path suffix",
			path:           "/v1beta/models/gemini-pro:streamGenerateContent",
			bodyJSON:       `{}`,
			expectedResult: true,
		},
		{
			name:           "Stream via JSON body",
			path:           "/v1beta/models/gemini-pro:generateContent",
			bodyJSON:       `{"stream": true}`,
			expectedResult: true,
		},
		{
			name:           "Non-streaming request",
			path:           "/v1beta/models/gemini-pro:generateContent",
			bodyJSON:       `{"stream": false}`,
			expectedResult: false,
		},
		{
			name:           "No stream indicator",
			path:           "/v1beta/models/gemini-pro:generateContent",
			bodyJSON:       `{}`,
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &GeminiChannel{
				BaseChannel: &BaseChannel{},
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", tt.path, nil)

			result := ch.IsStreamRequest(c, []byte(tt.bodyJSON))
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestGeminiChannel_ExtractModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		path           string
		bodyJSON       string
		expectedResult string
	}{
		{
			name:           "Extract from native format path",
			path:           "/v1beta/models/gemini-pro:generateContent",
			bodyJSON:       `{}`,
			expectedResult: "gemini-pro",
		},
		{
			name:           "Extract from native format with version",
			path:           "/v1/models/gemini-1.5-pro:streamGenerateContent",
			bodyJSON:       `{}`,
			expectedResult: "gemini-1.5-pro",
		},
		{
			name:           "Extract from OpenAI format body",
			path:           "/v1beta/openai/chat/completions",
			bodyJSON:       `{"model": "gemini-pro"}`,
			expectedResult: "gemini-pro",
		},
		{
			name:           "No model in path or body",
			path:           "/v1beta/other/endpoint",
			bodyJSON:       `{}`,
			expectedResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &GeminiChannel{
				BaseChannel: &BaseChannel{},
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", tt.path, nil)

			result := ch.ExtractModel(c, []byte(tt.bodyJSON))
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestGeminiChannel_ValidateKey_Success(t *testing.T) {
	t.Parallel()

	// Create mock server that returns success
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request format
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "gemini-pro")
		assert.Contains(t, r.URL.Path, "generateContent")
		assert.Equal(t, "test-valid-key", r.URL.Query().Get("key"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Verify request body
		var body map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&body)
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.NotNil(t, body["contents"])

		// Return success response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}`))
	}))
	defer server.Close()

	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1beta/models/gemini-pro:generateContent",
			TestModel:          "gemini-pro",
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

func TestGeminiChannel_ValidateKey_InvalidKey(t *testing.T) {
	t.Parallel()

	// Create mock server that returns 400 Bad Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"message":"API key not valid","status":"INVALID_ARGUMENT"}}`))
	}))
	defer server.Close()

	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{
			ValidationEndpoint: "/v1beta/models/gemini-pro:generateContent",
			TestModel:          "gemini-pro",
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
	assert.Contains(t, err.Error(), "400")
}

func TestGeminiChannel_ApplyModelRedirect_NativeFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		path             string
		redirectMap      map[string]string
		expectedPath     string
		expectedOriginal string
		shouldModify     bool
	}{
		{
			name: "Redirect gemini-pro to gemini-1.5-pro",
			path: "/v1beta/models/gemini-pro:generateContent",
			redirectMap: map[string]string{
				"gemini-pro": "gemini-1.5-pro",
			},
			expectedPath:     "/v1beta/models/gemini-1.5-pro:generateContent",
			expectedOriginal: "gemini-pro",
			shouldModify:     true,
		},
		{
			name: "No redirect rule",
			path: "/v1beta/models/gemini-ultra:generateContent",
			redirectMap: map[string]string{
				"gemini-pro": "gemini-1.5-pro",
			},
			expectedPath:     "/v1beta/models/gemini-ultra:generateContent",
			expectedOriginal: "",
			shouldModify:     false,
		},
		{
			name: "Redirect with streamGenerateContent",
			path: "/v1/models/gemini-pro:streamGenerateContent",
			redirectMap: map[string]string{
				"gemini-pro": "gemini-1.5-flash",
			},
			expectedPath:     "/v1/models/gemini-1.5-flash:streamGenerateContent",
			expectedOriginal: "gemini-pro",
			shouldModify:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &GeminiChannel{
				BaseChannel: &BaseChannel{},
			}

			req := httptest.NewRequest("POST", tt.path, nil)
			group := &models.Group{
				ModelRedirectMap: tt.redirectMap,
			}

			bodyBytes := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
			modifiedBody, originalModel, err := ch.ApplyModelRedirect(req, bodyBytes, group)

			assert.NoError(t, err)
			assert.Equal(t, bodyBytes, modifiedBody) // Body should not change for native format
			assert.Equal(t, tt.expectedOriginal, originalModel)

			if tt.shouldModify {
				assert.Equal(t, tt.expectedPath, req.URL.Path)
			} else {
				assert.Equal(t, tt.path, req.URL.Path)
			}
		})
	}
}

func TestGeminiChannel_ApplyModelRedirect_OpenAIFormat(t *testing.T) {
	t.Parallel()

	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{},
	}

	req := httptest.NewRequest("POST", "/v1beta/openai/chat/completions", nil)
	group := &models.Group{
		ModelRedirectMap: map[string]string{
			"gemini-pro": "gemini-1.5-pro",
		},
	}

	bodyBytes := []byte(`{"model":"gemini-pro","messages":[{"role":"user","content":"hi"}]}`)
	modifiedBody, originalModel, err := ch.ApplyModelRedirect(req, bodyBytes, group)

	assert.NoError(t, err)
	assert.Equal(t, "gemini-pro", originalModel)

	// Verify body was modified
	var body map[string]interface{}
	err = json.Unmarshal(modifiedBody, &body)
	require.NoError(t, err)
	assert.Equal(t, "gemini-1.5-pro", body["model"])
}

func TestGeminiChannel_TransformModelList_NativeFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		responseJSON  string
		redirectMap   map[string]string
		strictMode    bool
		expectedLen   int
		shouldContain []string
	}{
		{
			name: "Non-strict mode merges models",
			responseJSON: `{
				"models": [
					{"name": "models/gemini-pro", "displayName": "Gemini Pro"},
					{"name": "models/gemini-ultra", "displayName": "Gemini Ultra"}
				]
			}`,
			redirectMap: map[string]string{
				"custom-model": "gemini-pro",
			},
			strictMode:    false,
			expectedLen:   3, // 2 upstream + 1 configured
			shouldContain: []string{"models/gemini-pro", "models/gemini-ultra", "models/custom-model"},
		},
		{
			name: "Strict mode returns only configured models",
			responseJSON: `{
				"models": [
					{"name": "models/gemini-pro", "displayName": "Gemini Pro"},
					{"name": "models/gemini-ultra", "displayName": "Gemini Ultra"}
				]
			}`,
			redirectMap: map[string]string{
				"custom-model-1": "gemini-pro",
				"custom-model-2": "gemini-1.5-pro",
			},
			strictMode:    true,
			expectedLen:   2, // Only configured models
			shouldContain: []string{"models/custom-model-1", "models/custom-model-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := &GeminiChannel{
				BaseChannel: &BaseChannel{},
			}

			req := httptest.NewRequest("GET", "/v1beta/models", nil)
			group := &models.Group{
				ModelRedirectMap:    tt.redirectMap,
				ModelRedirectStrict: tt.strictMode,
			}

			result, err := ch.TransformModelList(req, []byte(tt.responseJSON), group)
			assert.NoError(t, err)
			assert.NotNil(t, result)

			modelList, ok := result["models"].([]any)
			require.True(t, ok)
			assert.Len(t, modelList, tt.expectedLen)

			// Check that expected models are present
			modelNames := make([]string, 0, len(modelList))
			for _, m := range modelList {
				if modelObj, ok := m.(map[string]any); ok {
					if name, ok := modelObj["name"].(string); ok {
						modelNames = append(modelNames, name)
					}
				}
			}

			for _, expected := range tt.shouldContain {
				assert.Contains(t, modelNames, expected)
			}
		})
	}
}

func TestGeminiChannel_TransformModelList_OpenAIFormat(t *testing.T) {
	t.Parallel()

	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{},
	}

	req := httptest.NewRequest("GET", "/v1beta/openai/models", nil)
	responseJSON := `{
		"data": [
			{"id": "gemini-pro", "object": "model"},
			{"id": "gemini-ultra", "object": "model"}
		]
	}`

	group := &models.Group{
		ModelRedirectMap: map[string]string{
			"custom-model": "gemini-pro",
		},
	}

	result, err := ch.TransformModelList(req, []byte(responseJSON), group)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Should use BaseChannel's transform for OpenAI format
	data, ok := result["data"]
	assert.True(t, ok)
	assert.NotNil(t, data)
}

func TestBuildConfiguredGeminiModelsFromRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		v1Map         map[string]string
		v2Map         map[string]*models.ModelRedirectRuleV2
		expectedLen   int
		shouldContain []string
	}{
		{
			name: "V1 rules only",
			v1Map: map[string]string{
				"model-a": "target-a",
				"model-b": "target-b",
			},
			v2Map:         nil,
			expectedLen:   2,
			shouldContain: []string{"models/model-a", "models/model-b"},
		},
		{
			name:  "V2 rules only",
			v1Map: nil,
			v2Map: map[string]*models.ModelRedirectRuleV2{
				"model-x": {Targets: []models.ModelRedirectTarget{{Model: "target-x", Weight: 100}}},
				"model-y": {Targets: []models.ModelRedirectTarget{{Model: "target-y", Weight: 100}}},
			},
			expectedLen:   2,
			shouldContain: []string{"models/model-x", "models/model-y"},
		},
		{
			name: "V2 takes priority over V1",
			v1Map: map[string]string{
				"model-a": "target-a",
			},
			v2Map: map[string]*models.ModelRedirectRuleV2{
				"model-x": {Targets: []models.ModelRedirectTarget{{Model: "target-x", Weight: 100}}},
			},
			expectedLen:   1, // Only V2 models
			shouldContain: []string{"models/model-x"},
		},
		{
			name:          "Empty rules",
			v1Map:         map[string]string{},
			v2Map:         map[string]*models.ModelRedirectRuleV2{},
			expectedLen:   0,
			shouldContain: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := buildConfiguredGeminiModelsFromRules(tt.v1Map, tt.v2Map)
			assert.Len(t, result, tt.expectedLen)

			// Extract model names
			modelNames := make([]string, 0, len(result))
			for _, m := range result {
				if modelObj, ok := m.(map[string]any); ok {
					if name, ok := modelObj["name"].(string); ok {
						modelNames = append(modelNames, name)
					}
				}
			}

			for _, expected := range tt.shouldContain {
				assert.Contains(t, modelNames, expected)
			}
		})
	}
}

func TestMergeGeminiModelLists(t *testing.T) {
	t.Parallel()

	upstream := []any{
		map[string]any{"name": "models/gemini-pro", "displayName": "Gemini Pro"},
		map[string]any{"name": "models/gemini-ultra", "displayName": "Gemini Ultra"},
	}

	configured := []any{
		map[string]any{"name": "models/custom-model", "displayName": "Custom Model"},
		map[string]any{"name": "models/gemini-pro", "displayName": "Gemini Pro Duplicate"}, // Duplicate
	}

	result := mergeGeminiModelLists(upstream, configured)

	// Should have 3 models: 2 upstream + 1 unique configured
	assert.Len(t, result, 3)

	// Extract names
	names := make([]string, 0, len(result))
	for _, m := range result {
		if modelObj, ok := m.(map[string]any); ok {
			if name, ok := modelObj["name"].(string); ok {
				names = append(names, name)
			}
		}
	}

	assert.Contains(t, names, "models/gemini-pro")
	assert.Contains(t, names, "models/gemini-ultra")
	assert.Contains(t, names, "models/custom-model")

	// Count occurrences of gemini-pro (should be 1, not 2) - use exact match
	count := 0
	for _, name := range names {
		if name == "models/gemini-pro" {
			count++
		}
	}
	assert.Equal(t, 1, count, "gemini-pro should appear only once")
}

func TestIsFirstPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		queryParams    string
		expectedResult bool
	}{
		{
			name:           "First page - no pageToken",
			queryParams:    "",
			expectedResult: true,
		},
		{
			name:           "First page - empty pageToken",
			queryParams:    "pageToken=",
			expectedResult: true,
		},
		{
			name:           "Subsequent page - has pageToken",
			queryParams:    "pageToken=abc123",
			expectedResult: false,
		},
		{
			name:           "First page - other params only",
			queryParams:    "pageSize=10&filter=test",
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "/v1beta/models?"+tt.queryParams, nil)
			result := isFirstPage(req)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// Benchmark tests
func BenchmarkGeminiChannel_ModifyRequest_Native(b *testing.B) {
	b.ReportAllocs()

	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-key",
	}

	group := &models.Group{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1beta/models/gemini-pro:generateContent", nil)
		ch.ModifyRequest(req, apiKey, group)
	}
}

func BenchmarkGeminiChannel_ModifyRequest_OpenAI(b *testing.B) {
	b.ReportAllocs()

	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{},
	}

	apiKey := &models.APIKey{
		KeyValue: "test-key",
	}

	group := &models.Group{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1beta/openai/chat/completions", nil)
		ch.ModifyRequest(req, apiKey, group)
	}
}

func BenchmarkGeminiChannel_ExtractModel(b *testing.B) {
	b.ReportAllocs()

	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{},
	}

	bodyBytes := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Create fresh context per iteration to avoid state accumulation
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1beta/models/gemini-pro:generateContent", nil)
		ch.ExtractModel(c, bodyBytes)
	}
}

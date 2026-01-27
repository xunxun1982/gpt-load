package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
)

func TestDetectRelayFormat(t *testing.T) {
	h := &HubHandler{}

	tests := []struct {
		name   string
		path   string
		method string
		want   types.RelayFormat
	}{
		// Chat endpoints
		{"Chat completions", "/hub/v1/chat/completions", "POST", types.RelayFormatOpenAIChat},
		{"Legacy completions", "/hub/v1/completions", "POST", types.RelayFormatOpenAICompletion},

		// Claude endpoints
		{"Claude messages", "/hub/v1/messages", "POST", types.RelayFormatClaude},

		// Codex endpoints
		{"Codex responses", "/hub/v1/responses", "POST", types.RelayFormatCodex},

		// Image endpoints
		{"Image generations", "/hub/v1/images/generations", "POST", types.RelayFormatOpenAIImage},
		{"Image edits", "/hub/v1/images/edits", "POST", types.RelayFormatOpenAIImageEdit},
		{"Image variations", "/hub/v1/images/variations", "POST", types.RelayFormatOpenAIImage},

		// Audio endpoints
		{"Audio transcriptions", "/hub/v1/audio/transcriptions", "POST", types.RelayFormatOpenAIAudioTranscription},
		{"Audio translations", "/hub/v1/audio/translations", "POST", types.RelayFormatOpenAIAudioTranslation},
		{"Audio speech", "/hub/v1/audio/speech", "POST", types.RelayFormatOpenAIAudioSpeech},

		// Embedding endpoints
		{"Embeddings", "/hub/v1/embeddings", "POST", types.RelayFormatOpenAIEmbedding},
		{"Engine embeddings", "/hub/v1/engines/text-embedding-ada-002/embeddings", "POST", types.RelayFormatOpenAIEmbedding},

		// Moderation endpoints
		{"Moderations", "/hub/v1/moderations", "POST", types.RelayFormatOpenAIModeration},

		// Gemini endpoints
		{"Gemini v1beta", "/hub/v1beta/models/gemini-pro:generateContent", "POST", types.RelayFormatGemini},
		{"Gemini v1", "/hub/v1/models/gemini-pro:streamGenerateContent", "POST", types.RelayFormatGemini},

		// Unknown endpoints
		{"Unknown path", "/hub/v1/unknown", "POST", types.RelayFormatUnknown},
		{"Root path", "/hub/v1", "POST", types.RelayFormatUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.detectRelayFormat(tt.path, tt.method)
			if got != tt.want {
				t.Errorf("detectRelayFormat(%q, %q) = %v, want %v", tt.path, tt.method, got, tt.want)
			}
		})
	}
}

func TestGetDefaultModelForFormat(t *testing.T) {
	h := &HubHandler{}

	tests := []struct {
		name   string
		format types.RelayFormat
		want   string
	}{
		{"Image generation default", types.RelayFormatOpenAIImage, "dall-e-3"},
		{"Image edit default", types.RelayFormatOpenAIImageEdit, "dall-e-3"},
		{"Audio transcription default", types.RelayFormatOpenAIAudioTranscription, "whisper-1"},
		{"Audio translation default", types.RelayFormatOpenAIAudioTranslation, "whisper-1"},
		{"Audio speech default", types.RelayFormatOpenAIAudioSpeech, "tts-1"},
		{"Embedding default", types.RelayFormatOpenAIEmbedding, "text-embedding-ada-002"},
		{"Moderation default", types.RelayFormatOpenAIModeration, "text-moderation-stable"},
		{"Gemini default", types.RelayFormatGemini, "gemini-2.0-flash-exp"},
		{"Chat no default", types.RelayFormatOpenAIChat, ""},
		{"Claude no default", types.RelayFormatClaude, ""},
		{"Unknown no default", types.RelayFormatUnknown, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.getDefaultModelForFormat(tt.format)
			if got != tt.want {
				t.Errorf("getDefaultModelForFormat(%v) = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

func TestExtractModelFromGeminiPath(t *testing.T) {
	h := &HubHandler{}

	tests := []struct {
		name string
		path string
		want string
	}{
		{"Gemini with action", "/v1beta/models/gemini-2.0-flash:generateContent", "gemini-2.0-flash"},
		{"Gemini stream action", "/v1beta/models/gemini-pro:streamGenerateContent", "gemini-pro"},
		{"Gemini no action", "/v1beta/models/gemini-pro", "gemini-pro"},
		{"No models prefix", "/v1beta/gemini-pro:generateContent", ""},
		{"Empty after models", "/v1beta/models/", ""},
		{"No path", "", ""},
		{"Invalid path", "/invalid", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.extractModelFromGeminiPath(tt.path)
			if got != tt.want {
				t.Errorf("extractModelFromGeminiPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestRewriteHubPath(t *testing.T) {
	h := &HubHandler{}
	groupName := "test-group"

	tests := []struct {
		name string
		path string
		want string
	}{
		{"Chat completions", "/hub/v1/chat/completions", "/proxy/test-group/v1/chat/completions"},
		{"Claude messages", "/hub/v1/messages", "/proxy/test-group/v1/messages"},
		{"Image generations", "/hub/v1/images/generations", "/proxy/test-group/v1/images/generations"},
		{"Audio transcriptions", "/hub/v1/audio/transcriptions", "/proxy/test-group/v1/audio/transcriptions"},
		{"Embeddings", "/hub/v1/embeddings", "/proxy/test-group/v1/embeddings"},
		{"Gemini v1beta", "/hub/v1beta/models/gemini-pro:generateContent", "/proxy/test-group/v1beta/models/gemini-pro:generateContent"},
		{"Models list", "/hub/v1/models", "/proxy/test-group/v1/models"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.rewriteHubPath(tt.path, groupName)
			if got != tt.want {
				t.Errorf("rewriteHubPath(%q, %q) = %q, want %q", tt.path, groupName, got, tt.want)
			}
		})
	}
}

func TestExtractModelFromRequest_JSON(t *testing.T) {
	h := &HubHandler{}

	tests := []struct {
		name        string
		format      types.RelayFormat
		body        map[string]interface{}
		wantModel   string
		wantDefault bool
	}{
		{
			name:      "Chat with model",
			format:    types.RelayFormatOpenAIChat,
			body:      map[string]interface{}{"model": "gpt-4"},
			wantModel: "gpt-4",
		},
		{
			name:        "Chat without model",
			format:      types.RelayFormatOpenAIChat,
			body:        map[string]interface{}{"messages": []interface{}{}},
			wantModel:   "",
			wantDefault: true,
		},
		{
			name:      "Image with model",
			format:    types.RelayFormatOpenAIImage,
			body:      map[string]interface{}{"model": "dall-e-3", "prompt": "test"},
			wantModel: "dall-e-3",
		},
		{
			name:        "Image without model",
			format:      types.RelayFormatOpenAIImage,
			body:        map[string]interface{}{"prompt": "test"},
			wantModel:   "dall-e-3",
			wantDefault: true,
		},
		{
			name:        "Audio transcription without model",
			format:      types.RelayFormatOpenAIAudioTranscription,
			body:        map[string]interface{}{"file": "audio.mp3"},
			wantModel:   "whisper-1",
			wantDefault: true,
		},
		{
			name:        "Embedding without model",
			format:      types.RelayFormatOpenAIEmbedding,
			body:        map[string]interface{}{"input": "test"},
			wantModel:   "text-embedding-ada-002",
			wantDefault: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test request
			bodyBytes, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/test", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			// Create gin context
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			got, returnedBodyBytes, err := h.extractModelFromRequest(c, tt.format)
			if err != nil {
				t.Fatalf("extractModelFromRequest() error = %v", err)
			}

			if got != tt.wantModel {
				t.Errorf("extractModelFromRequest() = %q, want %q", got, tt.wantModel)
			}

			// Verify body bytes are returned for non-GET requests
			if tt.body != nil && len(returnedBodyBytes) == 0 {
				t.Error("Expected body bytes to be returned, got empty")
			}

			// Verify default model is used when expected
			if tt.wantDefault {
				defaultModel := h.getDefaultModelForFormat(tt.format)
				if got != defaultModel {
					t.Errorf("Expected default model %q, got %q", defaultModel, got)
				}
			}
		})
	}
}

func TestExtractModelFromRequest_Gemini(t *testing.T) {
	h := &HubHandler{}

	tests := []struct {
		name      string
		path      string
		wantModel string
	}{
		{
			name:      "Gemini from path",
			path:      "/hub/v1beta/models/gemini-2.0-flash:generateContent",
			wantModel: "gemini-2.0-flash",
		},
		{
			name:      "Gemini pro from path",
			path:      "/hub/v1/models/gemini-pro:streamGenerateContent",
			wantModel: "gemini-pro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.path, bytes.NewReader([]byte("{}")))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			got, _, err := h.extractModelFromRequest(c, types.RelayFormatGemini)
			if err != nil {
				t.Fatalf("extractModelFromRequest() error = %v", err)
			}

			if got != tt.wantModel {
				t.Errorf("extractModelFromRequest() = %q, want %q", got, tt.wantModel)
			}
		})
	}
}

func TestExtractModelFromRequest_EmptyBody(t *testing.T) {
	h := &HubHandler{}

	tests := []struct {
		name      string
		format    types.RelayFormat
		wantModel string
	}{
		{"Image with empty body", types.RelayFormatOpenAIImage, "dall-e-3"},
		{"Audio with empty body", types.RelayFormatOpenAIAudioSpeech, "tts-1"},
		{"Embedding with empty body", types.RelayFormatOpenAIEmbedding, "text-embedding-ada-002"},
		{"Chat with empty body", types.RelayFormatOpenAIChat, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/test", bytes.NewReader([]byte{}))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = req

			got, bodyBytes, err := h.extractModelFromRequest(c, tt.format)
			if err != nil {
				t.Fatalf("extractModelFromRequest() error = %v", err)
			}

			if got != tt.wantModel {
				t.Errorf("extractModelFromRequest() = %q, want %q", got, tt.wantModel)
			}

			// Verify empty body returns empty bytes
			if len(bodyBytes) != 0 {
				t.Errorf("Expected empty body bytes, got %d bytes", len(bodyBytes))
			}
		})
	}
}

func TestExtractModelFromRequest_InvalidJSON(t *testing.T) {
	h := &HubHandler{}

	req := httptest.NewRequest("POST", "/test", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	// Should return default model for image format even with invalid JSON
	got, bodyBytes, err := h.extractModelFromRequest(c, types.RelayFormatOpenAIImage)
	if err != nil {
		t.Fatalf("extractModelFromRequest() error = %v", err)
	}

	if got != "dall-e-3" {
		t.Errorf("extractModelFromRequest() with invalid JSON = %q, want %q", got, "dall-e-3")
	}

	// Verify body bytes are returned even for invalid JSON
	if len(bodyBytes) == 0 {
		t.Error("Expected body bytes to be returned for invalid JSON")
	}
}

// TestEndpointCoverage verifies all major endpoints are covered
func TestEndpointCoverage(t *testing.T) {
	h := &HubHandler{}

	endpoints := []struct {
		path   string
		format types.RelayFormat
	}{
		{"/hub/v1/chat/completions", types.RelayFormatOpenAIChat},
		{"/hub/v1/completions", types.RelayFormatOpenAICompletion},
		{"/hub/v1/messages", types.RelayFormatClaude},
		{"/hub/v1/responses", types.RelayFormatCodex},
		{"/hub/v1/images/generations", types.RelayFormatOpenAIImage},
		{"/hub/v1/images/edits", types.RelayFormatOpenAIImageEdit},
		{"/hub/v1/audio/transcriptions", types.RelayFormatOpenAIAudioTranscription},
		{"/hub/v1/audio/translations", types.RelayFormatOpenAIAudioTranslation},
		{"/hub/v1/audio/speech", types.RelayFormatOpenAIAudioSpeech},
		{"/hub/v1/embeddings", types.RelayFormatOpenAIEmbedding},
		{"/hub/v1/moderations", types.RelayFormatOpenAIModeration},
		{"/hub/v1beta/models/gemini-pro:generateContent", types.RelayFormatGemini},
	}

	for _, ep := range endpoints {
		t.Run(ep.path, func(t *testing.T) {
			detected := h.detectRelayFormat(ep.path, "POST")
			if detected != ep.format {
				t.Errorf("Endpoint %s: detected format %v, want %v", ep.path, detected, ep.format)
			}

			// Verify format is valid
			if !detected.IsValid() {
				t.Errorf("Endpoint %s: format %v is not valid", ep.path, detected)
			}
		})
	}
}

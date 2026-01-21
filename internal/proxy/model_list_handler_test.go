package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShouldInterceptModelList(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		method   string
		expected bool
	}{
		{"v1 models GET", "/v1/models", "GET", true},
		{"v1beta models GET", "/v1beta/models", "GET", true},
		{"openai v1 models GET", "/v1beta/openai/v1/models", "GET", true},
		{"v1 models POST", "/v1/models", "POST", false},
		{"chat completions GET", "/v1/chat/completions", "GET", false},
		{"embeddings GET", "/v1/embeddings", "GET", false},
		{"empty path", "", "GET", false},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable for parallel subtests
		t.Run(tt.name, func(t *testing.T) {
			result := shouldInterceptModelList(tt.path, tt.method)
			assert.Equal(t, tt.expected, result)
		})
	}
}

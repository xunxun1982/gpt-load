package utils

import (
	"gpt-load/internal/models"
	"testing"
)

func TestGetValidationEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		group          *models.Group
		expectedResult string
	}{
		{
			name: "OpenAI channel with custom endpoint",
			group: &models.Group{
				ChannelType:        "openai",
				ValidationEndpoint: "/custom/endpoint",
			},
			expectedResult: "/custom/endpoint",
		},
		{
			name: "OpenAI channel with default endpoint",
			group: &models.Group{
				ChannelType:        "openai",
				ValidationEndpoint: "",
			},
			expectedResult: "/v1/chat/completions",
		},
		{
			name: "Anthropic channel with default endpoint",
			group: &models.Group{
				ChannelType:        "anthropic",
				ValidationEndpoint: "",
			},
			expectedResult: "/v1/messages",
		},
		{
			name: "Codex channel with default endpoint",
			group: &models.Group{
				ChannelType:        "codex",
				ValidationEndpoint: "",
			},
			expectedResult: "/v1/responses",
		},
		{
			name: "Codex channel with custom endpoint",
			group: &models.Group{
				ChannelType:        "codex",
				ValidationEndpoint: "/v1/chat/completions",
			},
			expectedResult: "/v1/chat/completions",
		},
		{
			name: "Unknown channel type",
			group: &models.Group{
				ChannelType:        "unknown",
				ValidationEndpoint: "",
			},
			expectedResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetValidationEndpoint(tt.group)
			if result != tt.expectedResult {
				t.Errorf("GetValidationEndpoint() = %v, want %v", result, tt.expectedResult)
			}
		})
	}
}

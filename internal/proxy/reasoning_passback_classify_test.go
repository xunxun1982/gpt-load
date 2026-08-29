package proxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// reasoningPassbackMessage is the exact DeepSeek thinking-mode rejection
// observed in the wild, including the backticks around `reasoning_content`.
const reasoningPassbackMessage = "The `reasoning_content` in the thinking mode must be passed back to the API."

// TestReasoningContentPassbackFailureDetector locks the isReasoningContentPassbackFailure
// matching rules: the message must contain both "reasoning_content" and "passed back"
// (case-insensitively) to be classified as the DeepSeek thinking-mode passback
// rejection.
func TestReasoningContentPassbackFailureDetector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{
			name:    "real deepseek message with backticks",
			message: reasoningPassbackMessage,
			want:    true,
		},
		{
			name:    "variant without backticks",
			message: "The reasoning_content in the thinking mode must be passed back to the API.",
			want:    true,
		},
		{
			name:    "wording variant sent back",
			message: "The reasoning_content in the thinking mode must be sent back to the API.",
			want:    true,
		},
		{
			name:    "wording variant returned back",
			message: "reasoning_content thinking content must be returned back on the next turn",
			want:    true,
		},
		{
			name:    "keyword order shuffled",
			message: "thinking mode requires reasoning_content to be passed back",
			want:    true,
		},
		{
			name:    "mixed case",
			message: "The `REASONING_CONTENT` in the Thinking Mode must be PASSED BACK to the API.",
			want:    true,
		},
		{
			name:    "missing back keyword",
			message: "The reasoning_content in the thinking mode must be provided again on retry.",
			want:    false,
		},
		{
			name:    "missing thinking keyword",
			message: "The reasoning_content must be passed back to the API.",
			want:    false,
		},
		{
			name:    "missing reasoning_content keyword",
			message: "The previous thinking message must be passed back to the API before continuing.",
			want:    false,
		},
		{
			name:    "empty message",
			message: "",
			want:    false,
		},
		{
			name:    "unrelated rate limit message",
			message: "rate limit exceeded",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isReasoningContentPassbackFailure(tt.message))
		})
	}
}

// TestReasoningPassbackClassification locks the isPermanentLogicalFailure contract
// after the fix: invalid_request_error carrying the DeepSeek thinking-mode passback
// rejection is recoverable (retryable), while every other case keeps its prior
// permanent/retryable classification.
func TestReasoningPassbackClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		errorCode    string
		errorMessage string
		want         bool
	}{
		{
			name:         "invalid_request_error with reasoning passback is retryable",
			errorCode:    "invalid_request_error",
			errorMessage: reasoningPassbackMessage,
			want:         false,
		},
		{
			name:         "invalid_request_error with ordinary message stays permanent",
			errorCode:    "invalid_request_error",
			errorMessage: "bad request",
			want:         true,
		},
		{
			name:         "invalid_request_error with reasoning passback is case-insensitive",
			errorCode:    "INVALID_REQUEST_ERROR",
			errorMessage: reasoningPassbackMessage,
			want:         false,
		},
		{
			name:         "model_not_found ignores message and stays permanent",
			errorCode:    "model_not_found",
			errorMessage: reasoningPassbackMessage,
			want:         true,
		},
		{
			name:         "empty error code is retryable by default",
			errorCode:    "",
			errorMessage: reasoningPassbackMessage,
			want:         false,
		},
		{
			name:         "rate_limit_exceeded is retryable",
			errorCode:    "rate_limit_exceeded",
			errorMessage: reasoningPassbackMessage,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isPermanentLogicalFailure(tt.errorCode, tt.errorMessage))
		})
	}
}

// TestReasoningPassbackLogicalStatusCodeLock confirms the reasoning passback
// rejection still maps to HTTP 400 like any other invalid_request_error: the fix
// changes retryability, not the client-visible status contract.
func TestReasoningPassbackLogicalStatusCodeLock(t *testing.T) {
	t.Parallel()

	require.Equal(t, http.StatusBadRequest, logicalFailureStatusCode("invalid_request_error", reasoningPassbackMessage))
	require.Equal(t, http.StatusBadRequest, logicalFailureStatusCode("invalid_request_error", "bad request"))
}

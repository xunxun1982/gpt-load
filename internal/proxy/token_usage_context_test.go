package proxy

import (
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/tokenusage"

	"github.com/gin-gonic/gin"
)

func TestTokenUsageContextCanBeClearedBetweenAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}

	setTokenUsage(c, tokenusage.Usage{InputTokens: 2, OutputTokens: 3})
	usage, source, ok := getTokenUsage(c)
	if !ok {
		t.Fatal("expected token usage")
	}
	if usage.TotalTokens != 5 {
		t.Fatalf("unexpected total tokens: %d", usage.TotalTokens)
	}
	if source != models.TokenUsageSourceUpstream {
		t.Fatalf("unexpected token usage source: %q", source)
	}

	setEstimatedOutputTokens(c, 4)
	if got := getEstimatedOutputTokens(c); got != 4 {
		t.Fatalf("unexpected estimated output tokens: %d", got)
	}

	clearTokenUsage(c)
	if usage, source, ok := getTokenUsage(c); ok || !usage.IsZero() || source != "" {
		t.Fatalf("unexpected token usage after clear: %+v source=%q ok=%v", usage, source, ok)
	}
	if got := getEstimatedOutputTokens(c); got != 0 {
		t.Fatalf("unexpected estimated output tokens after clear: %d", got)
	}
}

func TestEstimatedTokenCaptureHandlesSplitUTF8(t *testing.T) {
	var capture estimatedTokenCapture
	text := []byte("你好hello")
	if _, err := capture.Write(text[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.Write(text[1:]); err != nil {
		t.Fatal(err)
	}

	if got, want := capture.Tokens(), int64(2); got != want {
		t.Fatalf("unexpected token estimate: got %d want %d", got, want)
	}
}

func TestUpstreamTokenUsageClearsStaleEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}

	setEstimatedOutputTokens(c, 9)
	setTokenUsage(c, tokenusage.Usage{InputTokens: 4, OutputTokens: 2})

	if got := getEstimatedOutputTokens(c); got != 0 {
		t.Fatalf("unexpected stale estimated output tokens: %d", got)
	}
	usage, source, ok := getTokenUsage(c)
	if !ok {
		t.Fatal("expected token usage")
	}
	if source != models.TokenUsageSourceUpstream {
		t.Fatalf("unexpected source: %q", source)
	}
	if usage.TotalTokens != 6 {
		t.Fatalf("unexpected total tokens: %d", usage.TotalTokens)
	}
}

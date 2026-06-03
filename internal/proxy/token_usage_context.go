package proxy

import (
	"unicode/utf8"

	"gpt-load/internal/models"
	"gpt-load/internal/tokenusage"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
)

const (
	ctxKeyTokenUsage            = "token_usage"
	ctxKeyEstimatedOutputTokens = "estimated_output_tokens"
)

type tokenUsageContextValue struct {
	Usage  tokenusage.Usage
	Source string
}

func setTokenUsage(c *gin.Context, usage tokenusage.Usage) {
	setTokenUsageWithSource(c, usage, models.TokenUsageSourceUpstream)
}

func setTokenUsageWithSource(c *gin.Context, usage tokenusage.Usage, source string) {
	if c == nil || usage.IsZero() {
		return
	}
	if source == models.TokenUsageSourceUnknown {
		source = models.TokenUsageSourceUpstream
	}
	if source == models.TokenUsageSourceUpstream && c.Keys != nil {
		delete(c.Keys, ctxKeyEstimatedOutputTokens)
	}
	c.Set(ctxKeyTokenUsage, tokenUsageContextValue{
		Usage:  usage.Normalize(),
		Source: source,
	})
}

func setTokenUsageCounts(c *gin.Context, inputTokens, outputTokens, totalTokens int64) {
	setTokenUsage(c, tokenusage.Usage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
	})
}

func setTokenUsageFromBody(c *gin.Context, body []byte) bool {
	if usage, ok := tokenusage.FromResponseBody(body); ok {
		setTokenUsage(c, usage)
		return true
	}
	return false
}

func setTokenUsageOrEstimateFromFullBody(c *gin.Context, body []byte) {
	setTokenUsageOrEstimateFromFullBodyIf(c, body, true)
}

func setTokenUsageOrEstimateFromFullBodyIf(c *gin.Context, body []byte, allowEstimate bool) {
	if setTokenUsageFromBody(c, body) {
		return
	}
	if allowEstimate {
		setEstimatedOutputTokensFromBody(c, body)
	}
}

func setEstimatedOutputTokensFromBody(c *gin.Context, body []byte) {
	setEstimatedOutputTokens(c, int64(utils.EstimateTokensFromBytes(body)))
}

func setEstimatedOutputTokensFromText(c *gin.Context, text string) int64 {
	tokens := int64(utils.EstimateTokensFromString(text))
	setEstimatedOutputTokens(c, tokens)
	return tokens
}

func setEstimatedOutputTokens(c *gin.Context, tokens int64) {
	if c == nil || tokens <= 0 {
		return
	}
	c.Set(ctxKeyEstimatedOutputTokens, tokens)
}

func getEstimatedOutputTokens(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	value, exists := c.Get(ctxKeyEstimatedOutputTokens)
	if !exists {
		return 0
	}
	tokens, ok := value.(int64)
	if !ok || tokens <= 0 {
		return 0
	}
	return tokens
}

func getTokenUsage(c *gin.Context) (tokenusage.Usage, string, bool) {
	if c == nil {
		return tokenusage.Usage{}, "", false
	}
	value, exists := c.Get(ctxKeyTokenUsage)
	if !exists {
		return tokenusage.Usage{}, "", false
	}
	stored, ok := value.(tokenUsageContextValue)
	if !ok || stored.Usage.IsZero() {
		return tokenusage.Usage{}, "", false
	}
	return stored.Usage.Normalize(), stored.Source, true
}

func clearTokenUsage(c *gin.Context) {
	if c == nil || c.Keys == nil {
		return
	}
	delete(c.Keys, ctxKeyTokenUsage)
	delete(c.Keys, ctxKeyEstimatedOutputTokens)
}

type estimatedTokenCapture struct {
	runeCount int64
	pending   []byte
}

func (w *estimatedTokenCapture) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.add(p)
	return len(p), nil
}

func (w *estimatedTokenCapture) Tokens() int64 {
	runes := w.runeCount
	if len(w.pending) > 0 {
		runes += int64(utf8.RuneCount(w.pending))
	}
	if runes <= 0 {
		return 0
	}
	return (runes + 3) / 4
}

func (w *estimatedTokenCapture) add(p []byte) {
	data := p
	if len(w.pending) > 0 {
		combined := make([]byte, 0, len(w.pending)+len(p))
		combined = append(combined, w.pending...)
		combined = append(combined, p...)
		data = combined
		w.pending = w.pending[:0]
	}
	if len(data) == 0 {
		return
	}

	start := len(data)
	for start > 0 && !utf8.RuneStart(data[start-1]) {
		start--
	}
	if start > 0 {
		lastStart := start - 1
		if !utf8.FullRune(data[lastStart:]) {
			w.pending = append(w.pending, data[lastStart:]...)
			data = data[:lastStart]
		}
	}
	w.runeCount += int64(utf8.RuneCount(data))
}

func (w *estimatedTokenCapture) addString(s string) {
	if s == "" {
		return
	}
	if len(w.pending) > 0 {
		w.add([]byte(s))
		return
	}
	w.runeCount += int64(utf8.RuneCountInString(s))
}

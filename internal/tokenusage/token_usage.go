package tokenusage

import (
	"bytes"
	"encoding/json"
	"strings"
)

const maxSSELineBytes = 256 * 1024

// Usage stores upstream-reported token counts.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ThinkingTokens   int64 `json:"thinking_tokens"`
}

// IsZero reports whether no token usage was found.
func (u Usage) IsZero() bool {
	return u.InputTokens == 0 &&
		u.OutputTokens == 0 &&
		u.TotalTokens == 0 &&
		u.CacheReadTokens == 0 &&
		u.CacheWriteTokens == 0 &&
		u.ThinkingTokens == 0
}

// Normalize fills TotalTokens when upstream omits a total count.
func (u Usage) Normalize() Usage {
	if u.TotalTokens <= 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	return u
}

// FromJSON extracts usage from common upstream response bodies.
// Providers tokenize differently, so this package maps reported usage fields instead of
// applying a local tokenizer for exact accounting.
func FromJSON(body []byte) (Usage, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return Usage{}, false
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return Usage{}, false
	}
	if usage, ok := usageFromRaw(root["usage"]); ok {
		return usage, true
	}
	if usage, ok := usageFromRaw(root["usageMetadata"]); ok {
		return usage, true
	}
	if usage, ok := usageFromRaw(root["meta"]); ok {
		return usage, true
	}
	if metadata, ok := objectField(root, "metadata"); ok {
		if usage, ok := usageFromRaw(metadata["usage"]); ok {
			return usage, true
		}
	}
	if usage, ok := usageFromRaw(body); ok {
		return usage, true
	}
	return Usage{}, false
}

// FromSSE extracts the last usage object from an SSE stream.
func FromSSE(body []byte) (Usage, bool) {
	var last Usage
	found := false

	for _, line := range bytes.Split(body, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if usage, ok := FromJSON(data); ok {
			last = usage
			found = true
		}
	}

	return last, found
}

// SSEParser incrementally extracts usage from streaming SSE chunks.
type SSEParser struct {
	pending []byte
	usage   Usage
	found   bool
}

// Write parses any complete SSE lines from chunk.
func (p *SSEParser) Write(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	p.pending = append(p.pending, chunk...)
	for {
		idx := bytes.IndexByte(p.pending, '\n')
		if idx < 0 {
			if len(p.pending) > maxSSELineBytes {
				p.pending = p.pending[:0]
			}
			return
		}
		line := p.pending[:idx]
		p.pending = p.pending[idx+1:]
		p.parseLine(line)
	}
}

// Finish parses the final unterminated line and returns the last usage found.
func (p *SSEParser) Finish() (Usage, bool) {
	if len(p.pending) > 0 {
		p.parseLine(p.pending)
		p.pending = nil
	}
	return p.usage, p.found
}

func (p *SSEParser) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if usage, ok := FromJSON(data); ok {
		p.usage = usage
		p.found = true
	}
}

// FromResponseBody extracts usage from either JSON or text/event-stream payloads.
func FromResponseBody(body []byte) (Usage, bool) {
	if usage, ok := FromJSON(body); ok {
		return usage, true
	}
	if usage, ok := FromSSE(body); ok {
		return usage, true
	}
	if usage, ok := FromJSONLines(body); ok {
		return usage, true
	}
	return fromJSONFragment(body)
}

// FromJSONLines extracts the last usage object from newline-delimited JSON streams.
func FromJSONLines(body []byte) (Usage, bool) {
	var last Usage
	found := false

	for _, line := range bytes.Split(body, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if usage, ok := FromJSON(line); ok {
			last = usage
			found = true
		}
	}

	return last, found
}

func usageFromRaw(raw json.RawMessage) (Usage, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return Usage{}, false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Usage{}, false
	}

	usage := Usage{
		InputTokens: firstInt(
			fields,
			"prompt_tokens",
			"input_tokens",
			"promptTokenCount",
			"inputTokens",
			"prompt_eval_count",
		),
		OutputTokens: firstInt(
			fields,
			"completion_tokens",
			"output_tokens",
			"candidatesTokenCount",
			"outputTokens",
			"eval_count",
		),
		TotalTokens: firstInt(fields, "total_tokens", "totalTokenCount", "totalTokens"),
		CacheReadTokens: firstInt(
			fields,
			"cache_read_input_tokens",
			"cache_read_tokens",
			"cached_input_tokens",
			"cached_tokens",
			"cachedContentTokenCount",
			"cacheReadInputTokens",
			"prompt_cache_hit_tokens",
		),
		CacheWriteTokens: firstInt(fields, "cache_creation_input_tokens", "cache_write_tokens", "cacheWriteInputTokens"),
		ThinkingTokens:   firstInt(fields, "thinking_tokens", "reasoning_tokens", "reasoning_output_tokens", "thoughtsTokenCount"),
	}

	for _, nestedKey := range []string{"tokens", "billed_units", "billedUnits"} {
		if nested, ok := objectField(fields, nestedKey); ok {
			usage.InputTokens = firstPositive(usage.InputTokens, firstInt(nested, "input_tokens", "inputTokens"))
			usage.OutputTokens = firstPositive(usage.OutputTokens, firstInt(nested, "output_tokens", "outputTokens"))
			usage.TotalTokens = firstPositive(usage.TotalTokens, firstInt(nested, "total_tokens", "totalTokens"))
		}
	}

	if details, ok := objectField(fields, "completion_tokens_details", "output_tokens_details"); ok {
		usage.ThinkingTokens = firstPositive(usage.ThinkingTokens, firstInt(details, "reasoning_tokens"))
	}
	if details, ok := objectField(fields, "prompt_tokens_details", "input_tokens_details"); ok {
		usage.CacheReadTokens = firstPositive(usage.CacheReadTokens, firstInt(details, "cached_tokens", "cache_read_tokens", "cache_read_input_tokens"))
		usage.CacheWriteTokens = firstPositive(usage.CacheWriteTokens, firstInt(details, "cache_creation_input_tokens", "cache_write_tokens"))
		if cacheCreation, ok := objectField(details, "cache_creation"); ok {
			usage.CacheWriteTokens = firstPositive(usage.CacheWriteTokens, firstInt(cacheCreation, "cache_creation_input_tokens", "ephemeral_5m_input_tokens"))
		}
	}
	if usage.TotalTokens <= 0 {
		usage.TotalTokens = fallbackTotalTokens(fields, usage)
	}

	usage = usage.Normalize()
	return usage, !usage.IsZero()
}

func fallbackTotalTokens(fields map[string]json.RawMessage, usage Usage) int64 {
	total := usage.InputTokens + usage.OutputTokens

	if hasAnyField(fields, "cache_read_input_tokens", "cache_creation_input_tokens", "cached_input_tokens", "cache_read_tokens", "cache_write_tokens") {
		total += usage.CacheReadTokens + usage.CacheWriteTokens
	}
	if hasAnyField(fields, "thinking_tokens", "reasoning_output_tokens", "thoughtsTokenCount") {
		total += usage.ThinkingTokens
	}

	return total
}

func fromJSONFragment(body []byte) (Usage, bool) {
	for _, key := range [][]byte{[]byte(`"usageMetadata"`), []byte(`"usage"`), []byte(`"meta"`)} {
		if usage, ok := usageFromFragmentKey(body, key); ok {
			return usage, true
		}
	}
	return Usage{}, false
}

func usageFromFragmentKey(body, key []byte) (Usage, bool) {
	idx := bytes.LastIndex(body, key)
	if idx < 0 {
		return Usage{}, false
	}

	i := idx + len(key)
	for i < len(body) && isJSONSpace(body[i]) {
		i++
	}
	if i >= len(body) || body[i] != ':' {
		return Usage{}, false
	}
	i++
	for i < len(body) && isJSONSpace(body[i]) {
		i++
	}
	if i >= len(body) || body[i] != '{' {
		return Usage{}, false
	}

	raw, ok := extractJSONObject(body[i:])
	if !ok {
		return Usage{}, false
	}
	return usageFromRaw(raw)
}

func extractJSONObject(body []byte) (json.RawMessage, bool) {
	if len(body) == 0 || body[0] != '{' {
		return nil, false
	}

	depth := 0
	inString := false
	escaped := false
	for i, ch := range body {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return json.RawMessage(body[:i+1]), true
			}
		}
	}
	return nil, false
}

func isJSONSpace(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}

func objectField(fields map[string]json.RawMessage, keys ...string) (map[string]json.RawMessage, bool) {
	for _, key := range keys {
		if raw, ok := rawField(fields, key); ok {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(raw, &nested); err == nil {
				return nested, true
			}
		}
	}
	return nil, false
}

func firstInt(fields map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		if raw, ok := rawField(fields, key); ok {
			if value, ok := parseInt(raw); ok && value > 0 {
				return value
			}
		}
	}
	return 0
}

func rawField(fields map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	if value, ok := fields[key]; ok {
		return value, true
	}
	for field, value := range fields {
		if strings.EqualFold(field, key) {
			return value, true
		}
	}
	return nil, false
}

func hasAnyField(fields map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, ok := rawField(fields, key); ok {
			return true
		}
	}
	return false
}

func parseInt(raw json.RawMessage) (int64, bool) {
	var value int64
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true
	}
	var floatValue float64
	if err := json.Unmarshal(raw, &floatValue); err == nil {
		return int64(floatValue), true
	}
	return 0, false
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

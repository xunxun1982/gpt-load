package tokenusage

import (
	"bytes"
	"testing"
)

var benchmarkUsageSink Usage

func TestFromJSONOpenAIChatUsage(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"prompt_tokens": 11,
			"completion_tokens": 7,
			"total_tokens": 18,
			"completion_tokens_details": {"reasoning_tokens": 3}
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 || usage.TotalTokens != 18 || usage.ThinkingTokens != 3 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONOpenAIDetailsDoNotInflateFallbackTotal(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"prompt_tokens": 11,
			"completion_tokens": 7,
			"prompt_tokens_details": {"cached_tokens": 4},
			"completion_tokens_details": {"reasoning_tokens": 3}
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 || usage.TotalTokens != 18 || usage.CacheReadTokens != 4 || usage.ThinkingTokens != 3 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONResponsesUsage(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"input_tokens": 20,
			"output_tokens": 8,
			"total_tokens": 28
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 20 || usage.OutputTokens != 8 || usage.TotalTokens != 28 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONAnthropicUsage(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"input_tokens": 30,
			"output_tokens": 12,
			"cache_read_input_tokens": 5,
			"cache_creation_input_tokens": 6
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 30 || usage.OutputTokens != 12 || usage.TotalTokens != 53 || usage.CacheReadTokens != 5 || usage.CacheWriteTokens != 6 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONCodexUsageAliases(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cached_input_tokens": 20,
			"reasoning_output_tokens": 7
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 50 || usage.TotalTokens != 177 || usage.CacheReadTokens != 20 || usage.ThinkingTokens != 7 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONGeminiUsageMetadata(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usageMetadata": {
			"promptTokenCount": 13,
			"candidatesTokenCount": 9,
			"totalTokenCount": 24,
			"thoughtsTokenCount": 2,
			"cachedContentTokenCount": 4
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 13 || usage.OutputTokens != 9 || usage.TotalTokens != 24 || usage.ThinkingTokens != 2 || usage.CacheReadTokens != 4 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONDeepSeekUsageDetails(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 20,
			"total_tokens": 120,
			"prompt_cache_hit_tokens": 32,
			"prompt_cache_miss_tokens": 68,
			"completion_tokens_details": {"reasoning_tokens": 7}
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.TotalTokens != 120 || usage.CacheReadTokens != 32 || usage.ThinkingTokens != 7 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONQwenPromptTokenDetails(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"prompt_tokens": 21,
			"completion_tokens": 8,
			"total_tokens": 29,
			"prompt_tokens_details": {
				"cached_tokens": 5,
				"cache_creation_input_tokens": 3
			},
			"completion_tokens_details": {"reasoning_tokens": 2}
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 21 || usage.OutputTokens != 8 || usage.TotalTokens != 29 || usage.CacheReadTokens != 5 || usage.CacheWriteTokens != 3 || usage.ThinkingTokens != 2 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONQwenNestedCacheCreationDetails(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"usage": {
			"input_tokens": 40,
			"output_tokens": 10,
			"total_tokens": 50,
			"cached_tokens": 12,
			"prompt_tokens_details": {
				"cache_creation": {
					"cache_creation_input_tokens": 6
				}
			}
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 40 || usage.OutputTokens != 10 || usage.TotalTokens != 50 || usage.CacheReadTokens != 12 || usage.CacheWriteTokens != 6 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONBedrockConverseUsage(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"output": {"message": {"role": "assistant", "content": [{"text": "ok"}]}},
		"usage": {
			"inputTokens": 31,
			"outputTokens": 11,
			"totalTokens": 42,
			"cacheReadInputTokens": 4,
			"cacheWriteInputTokens": 2
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 31 || usage.OutputTokens != 11 || usage.TotalTokens != 42 || usage.CacheReadTokens != 4 || usage.CacheWriteTokens != 2 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONCohereMetaTokens(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"id": "chat-test",
		"message": {"role": "assistant", "content": [{"type": "text", "text": "ok"}]},
		"meta": {
			"tokens": {"input_tokens": 14, "output_tokens": 6},
			"billed_units": {"input_tokens": 15, "output_tokens": 7}
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 14 || usage.OutputTokens != 6 || usage.TotalTokens != 20 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONOllamaTopLevelCounts(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"model": "custom-local-model",
		"response": "ok",
		"done": true,
		"prompt_eval_count": 18,
		"eval_count": 9
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 18 || usage.OutputTokens != 9 || usage.TotalTokens != 27 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromJSONResponseWrappedUsage(t *testing.T) {
	usage, ok := FromJSON([]byte(`{
		"type": "response.completed",
		"response": {
			"id": "resp-test",
			"usage": {
				"input_tokens": 12,
				"output_tokens": 8,
				"total_tokens": 20
			}
		}
	}`))
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 8 || usage.TotalTokens != 20 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromSSEUsesLastUsageChunk(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6,\"total_tokens\":10}}\n\n" +
		"data: [DONE]\n\n")
	usage, ok := FromSSE(body)
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 4 || usage.OutputTokens != 6 || usage.TotalTokens != 10 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestSSEParserAcrossChunks(t *testing.T) {
	var parser SSEParser
	parser.Write([]byte("data: {\"choices\":[]}\n\ndata: {\"usage\":{\"input_"))
	parser.Write([]byte("tokens\":5,\"output_tokens\":6,\"total_tokens\":11}}\n\n"))
	parser.Write([]byte("data: [DONE]\n\n"))

	usage, ok := parser.Finish()
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 6 || usage.TotalTokens != 11 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromResponseBodyNoUsage(t *testing.T) {
	if usage, ok := FromResponseBody([]byte(`{"id":"x"}`)); ok || !usage.IsZero() {
		t.Fatalf("unexpected usage: %+v ok=%v", usage, ok)
	}
}

func TestFromResponseBodyFragmentUsage(t *testing.T) {
	body := []byte(`...large response tail...,"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`)
	usage, ok := FromResponseBody(body)
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 9 || usage.OutputTokens != 4 || usage.TotalTokens != 13 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromResponseBodyFragmentMetaUsage(t *testing.T) {
	body := []byte(`...large response tail...,"meta":{"tokens":{"input_tokens":9,"output_tokens":4}}}`)
	usage, ok := FromResponseBody(body)
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 9 || usage.OutputTokens != 4 || usage.TotalTokens != 13 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestFromResponseBodyJSONLinesUsage(t *testing.T) {
	body := []byte("{\"model\":\"custom-local-model\",\"response\":\"hel\",\"done\":false}\n" +
		"{\"model\":\"custom-local-model\",\"response\":\"lo\",\"done\":true,\"prompt_eval_count\":18,\"eval_count\":9}\n")
	usage, ok := FromResponseBody(body)
	if !ok {
		t.Fatal("expected usage")
	}
	if usage.InputTokens != 18 || usage.OutputTokens != 9 || usage.TotalTokens != 27 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func BenchmarkFromResponseBodyOpenAIUsage(b *testing.B) {
	body := []byte(`{"id":"chatcmpl-test","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168,"prompt_tokens_details":{"cached_tokens":12},"completion_tokens_details":{"reasoning_tokens":3}}}`)
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		usage, ok := FromResponseBody(body)
		if !ok {
			b.Fatal("expected usage")
		}
		benchmarkUsageSink = usage
	}
}

func BenchmarkSSEParserUsageAtTail(b *testing.B) {
	delta := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello 世界\"}}]}\n\n")
	body := bytes.Repeat(delta, 128)
	body = append(body, []byte("data: {\"usage\":{\"prompt_tokens\":123,\"completion_tokens\":45,\"total_tokens\":168}}\n\n")...)
	body = append(body, []byte("data: [DONE]\n\n")...)

	b.SetBytes(int64(len(body)))
	for b.Loop() {
		var parser SSEParser
		parser.Write(body)
		usage, ok := parser.Finish()
		if !ok {
			b.Fatal("expected usage")
		}
		benchmarkUsageSink = usage
	}
}

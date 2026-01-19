package utils

import (
	"fmt"
	"testing"
)

// BenchmarkDetectBrandPrefix benchmarks brand prefix detection
func BenchmarkDetectBrandPrefix(b *testing.B) {
	testCases := []struct {
		name  string
		model string
	}{
		{"OpenAI_GPT4", "gpt-4"},
		{"OpenAI_GPT35", "gpt-3.5-turbo"},
		{"Anthropic_Claude", "claude-3-opus"},
		{"Google_Gemini", "gemini-pro"},
		{"Meta_Llama", "llama-3-70b"},
		{"Mistral", "mistral-large"},
		{"DeepSeek", "deepseek-chat"},
		{"Qwen", "qwen-turbo"},
		{"GLM", "glm-4"},
		{"WithPrefix", "openai/gpt-4"},
		{"NoMatch", "unknown-model-xyz"},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = DetectBrandPrefix(tc.model)
			}
		})
	}
}

// BenchmarkDetectBrandPrefixWithOfficial benchmarks detection with official name
func BenchmarkDetectBrandPrefixWithOfficial(b *testing.B) {
	models := []string{
		"gpt-4",
		"claude-3-opus",
		"gemini-pro",
		"llama-3-70b",
		"mistral-large",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		model := models[i%len(models)]
		_, _ = DetectBrandPrefixWithOfficial(model)
	}
}

// BenchmarkStripExistingPrefix benchmarks prefix stripping
func BenchmarkStripExistingPrefix(b *testing.B) {
	testCases := []struct {
		name  string
		model string
	}{
		{"NoPrefix", "gpt-4"},
		{"SinglePrefix", "openai/gpt-4"},
		{"DoublePrefix", "lora/openai/gpt-4"},
		{"TriplePrefix", "pro/lora/openai/gpt-4"},
		{"HostingPrefix", "openrouter/gpt-4"},
		{"MultipleHosting", "deepinfra/openrouter/gpt-4"},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = StripExistingPrefix(tc.model)
			}
		})
	}
}

// BenchmarkApplyBrandPrefix benchmarks brand prefix application
func BenchmarkApplyBrandPrefix(b *testing.B) {
	testCases := []struct {
		name         string
		model        string
		useLowercase bool
	}{
		{"Lowercase_GPT4", "gpt-4", true},
		{"Official_GPT4", "gpt-4", false},
		{"Lowercase_Claude", "claude-3-opus", true},
		{"Official_Claude", "claude-3-opus", false},
		{"Lowercase_WithPrefix", "openai/gpt-4", true},
		{"Official_WithPrefix", "openai/gpt-4", false},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = ApplyBrandPrefix(tc.model, tc.useLowercase)
			}
		})
	}
}

// BenchmarkApplyBrandPrefixBatch benchmarks batch prefix application
func BenchmarkApplyBrandPrefixBatch(b *testing.B) {
	models := []string{
		"gpt-4",
		"gpt-3.5-turbo",
		"claude-3-opus",
		"gemini-pro",
		"llama-3-70b",
		"mistral-large",
		"deepseek-chat",
		"qwen-turbo",
		"glm-4",
		"unknown-model",
	}

	b.Run("Lowercase", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = ApplyBrandPrefixBatch(models, true)
		}
	})

	b.Run("Official", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = ApplyBrandPrefixBatch(models, false)
		}
	})
}

// BenchmarkNormalizeGLMModelName benchmarks GLM normalization
func BenchmarkNormalizeGLMModelName(b *testing.B) {
	testCases := []string{
		"glm4",
		"glm4.7",
		"GLM4",
		"glm-4",
		"chatglm3",
	}

	for i := 0; i < b.N; i++ {
		model := testCases[i%len(testCases)]
		_ = normalizeGLMModelName(model)
	}
}

// BenchmarkConcurrentDetection benchmarks concurrent brand detection
func BenchmarkConcurrentDetection(b *testing.B) {
	models := []string{
		"gpt-4",
		"claude-3-opus",
		"gemini-pro",
		"llama-3-70b",
		"mistral-large",
		"deepseek-chat",
		"qwen-turbo",
		"glm-4",
	}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			model := models[i%len(models)]
			_ = DetectBrandPrefix(model)
			i++
		}
	})
}

// BenchmarkConcurrentApply benchmarks concurrent prefix application
func BenchmarkConcurrentApply(b *testing.B) {
	models := []string{
		"gpt-4",
		"claude-3-opus",
		"gemini-pro",
		"llama-3-70b",
		"mistral-large",
	}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			model := models[i%len(models)]
			_ = ApplyBrandPrefix(model, true)
			i++
		}
	})
}

// BenchmarkRealisticWorkloadPrefix simulates realistic model prefix workload
func BenchmarkRealisticWorkloadPrefix(b *testing.B) {
	// Simulate realistic model name patterns from API requests
	models := make([]string, 100)
	for i := 0; i < 100; i++ {
		switch i % 10 {
		case 0:
			models[i] = fmt.Sprintf("gpt-4-%d", i)
		case 1:
			models[i] = fmt.Sprintf("claude-3-%d", i)
		case 2:
			models[i] = fmt.Sprintf("gemini-pro-%d", i)
		case 3:
			models[i] = fmt.Sprintf("llama-3-%d", i)
		case 4:
			models[i] = fmt.Sprintf("openai/gpt-4-%d", i)
		case 5:
			models[i] = fmt.Sprintf("lora/openai/gpt-4-%d", i)
		case 6:
			models[i] = fmt.Sprintf("deepseek-chat-%d", i)
		case 7:
			models[i] = fmt.Sprintf("qwen-turbo-%d", i)
		case 8:
			models[i] = fmt.Sprintf("glm4.%d", i)
		case 9:
			models[i] = fmt.Sprintf("unknown-model-%d", i)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		model := models[i%len(models)]

		// Detect brand
		prefix := DetectBrandPrefix(model)

		// Strip existing prefix
		stripped := StripExistingPrefix(model)

		// Apply brand prefix
		if prefix != "" {
			_ = ApplyBrandPrefix(stripped, true)
		}
	}
}

// BenchmarkAllBrandRules benchmarks detection against all brand rules
func BenchmarkAllBrandRules(b *testing.B) {
	// Test one model from each major brand category
	models := []string{
		"gpt-4",                    // OpenAI
		"claude-3-opus",            // Anthropic
		"gemini-pro",               // Google
		"llama-3-70b",              // Meta
		"mistral-large",            // Mistral
		"qwen-turbo",               // Tongyi/Alibaba
		"glm-4",                    // GLM/Zhipu
		"deepseek-chat",            // DeepSeek
		"kimi-chat",                // Kimi/Moonshot
		"doubao-pro",               // Doubao/ByteDance
		"ernie-bot",                // ERNIE/Baidu
		"spark-v3",                 // Spark/iFlytek
		"hunyuan-pro",              // Hunyuan/Tencent
		"yi-large",                 // Yi/01.AI
		"baichuan2",                // Baichuan
		"minimax-abab",             // MiniMax
		"stable-diffusion-xl",      // Stability
		"flux-pro",                 // BFL
		"dall-e-3",                 // OpenAI
		"midjourney-v6",            // Midjourney
		"whisper-1",                // OpenAI
		"tts-1",                    // OpenAI
		"text-embedding-ada-002",   // OpenAI
		"text-embedding-3-large",   // OpenAI
		"bge-large-zh",             // BAAI
		"jina-embeddings-v2",       // Jina
		"voyage-large-2",           // Voyage
		"cohere-embed-v3",          // Cohere
		"grok-beta",                // xAI
		"nova-pro",                 // Amazon
		"phi-3-medium",             // Microsoft
		"falcon-180b",              // TII
		"internlm2-chat",           // InternLM
		"vicuna-13b",               // LMSYS
		"zephyr-7b",                // HuggingFace
		"starcoder2",               // BigCode
		"nemotron-4",               // NVIDIA
		"granite-13b",              // IBM
		"dbrx-instruct",            // Databricks
		"arctic-embed",             // Snowflake
		"command-r-plus",           // Cohere
		"sonar-medium",             // Perplexity
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		model := models[i%len(models)]
		_ = DetectBrandPrefix(model)
	}
}

// BenchmarkPatternMatching benchmarks regex pattern matching
func BenchmarkPatternMatching(b *testing.B) {
	// Test models that require complex pattern matching
	testCases := []struct {
		name  string
		model string
	}{
		{"ExactMatch", "gpt-4"},
		{"PrefixMatch", "gpt-4-turbo"},
		{"SuffixMatch", "gpt-4-vision-preview"},
		{"MiddleMatch", "gpt-4-0125-preview"},
		{"CaseInsensitive", "GPT-4"},
		{"WithNumbers", "llama-3.1-70b"},
		{"WithDots", "glm-4.7"},
		{"WithHyphens", "claude-3-opus-20240229"},
		{"Complex", "lora/pro/openai/gpt-4-turbo-preview"},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = DetectBrandPrefix(tc.model)
			}
		})
	}
}

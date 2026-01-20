package utils

import (
	"encoding/json"
	"fmt"
	"testing"
)

// BenchmarkApplyModelMapping benchmarks model mapping application
func BenchmarkApplyModelMapping(b *testing.B) {
	testCases := []struct {
		name        string
		mappingJSON string
		model       string
	}{
		{
			"NoMapping",
			`{}`,
			"gpt-4",
		},
		{
			"SimpleMapping",
			`{"gpt-4": "gpt-4-turbo"}`,
			"gpt-4",
		},
		{
			"ChainedMapping",
			`{"gpt-4": "gpt-4-turbo", "gpt-4-turbo": "gpt-4-turbo-preview"}`,
			"gpt-4",
		},
		{
			"LongChain",
			`{"a": "b", "b": "c", "c": "d", "d": "e", "e": "f"}`,
			"a",
		},
		{
			"NoMatch",
			`{"gpt-4": "gpt-4-turbo"}`,
			"claude-3",
		},
		{
			"LargeMapping",
			generateLargeMappingJSON(100),
			"model-50",
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _, _ = ApplyModelMapping(tc.model, tc.mappingJSON)
			}
		})
	}
}

// BenchmarkApplyModelMappingFromMap benchmarks mapping with pre-parsed map
func BenchmarkApplyModelMappingFromMap(b *testing.B) {
	testCases := []struct {
		name  string
		model string
		mmap  map[string]string
	}{
		{
			"NoMapping",
			"gpt-4",
			map[string]string{},
		},
		{
			"SimpleMapping",
			"gpt-4",
			map[string]string{"gpt-4": "gpt-4-turbo"},
		},
		{
			"ChainedMapping",
			"gpt-4",
			map[string]string{
				"gpt-4":       "gpt-4-turbo",
				"gpt-4-turbo": "gpt-4-turbo-preview",
			},
		},
		{
			"LongChain",
			"a",
			map[string]string{
				"a": "b",
				"b": "c",
				"c": "d",
				"d": "e",
				"e": "f",
			},
		},
		{
			"NoMatch",
			"claude-3",
			map[string]string{"gpt-4": "gpt-4-turbo"},
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _, _ = ApplyModelMappingFromMap(tc.model, tc.mmap)
			}
		})
	}
}

// BenchmarkParseModelMapping benchmarks JSON parsing
func BenchmarkParseModelMapping(b *testing.B) {
	testCases := []struct {
		name        string
		mappingJSON string
	}{
		{"Empty", `{}`},
		{"Small", `{"gpt-4": "gpt-4-turbo"}`},
		{"Medium", generateLargeMappingJSON(10)},
		{"Large", generateLargeMappingJSON(100)},
		{"XLarge", generateLargeMappingJSON(1000)},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = ParseModelMapping(tc.mappingJSON)
			}
		})
	}
}

// BenchmarkValidateModelMapping benchmarks mapping validation
func BenchmarkValidateModelMapping(b *testing.B) {
	testCases := []struct {
		name        string
		mappingJSON string
	}{
		{
			"Valid_Simple",
			`{"gpt-4": "gpt-4-turbo"}`,
		},
		{
			"Valid_Chained",
			`{"gpt-4": "gpt-4-turbo", "gpt-4-turbo": "gpt-4-turbo-preview"}`,
		},
		{
			"Valid_Large",
			generateLargeMappingJSON(100),
		},
		{
			"Invalid_Circular",
			`{"a": "b", "b": "a"}`,
		},
		{
			"Invalid_SelfReference",
			`{"gpt-4": "gpt-4"}`,
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = ValidateModelMapping(tc.mappingJSON)
			}
		})
	}
}

// BenchmarkConcurrentMapping benchmarks concurrent mapping operations
func BenchmarkConcurrentMapping(b *testing.B) {
	mappingJSON := `{
		"gpt-4": "gpt-4-turbo",
		"gpt-3.5": "gpt-3.5-turbo",
		"claude-2": "claude-3-opus",
		"gemini": "gemini-pro"
	}`

	models := []string{"gpt-4", "gpt-3.5", "claude-2", "gemini", "unknown"}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			model := models[i%len(models)]
			_, _, _ = ApplyModelMapping(model, mappingJSON)
			i++
		}
	})
}

// BenchmarkConcurrentMappingFromMap benchmarks concurrent mapping with pre-parsed map
func BenchmarkConcurrentMappingFromMap(b *testing.B) {
	modelMap := map[string]string{
		"gpt-4":    "gpt-4-turbo",
		"gpt-3.5":  "gpt-3.5-turbo",
		"claude-2": "claude-3-opus",
		"gemini":   "gemini-pro",
	}

	models := []string{"gpt-4", "gpt-3.5", "claude-2", "gemini", "unknown"}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			model := models[i%len(models)]
			_, _, _ = ApplyModelMappingFromMap(model, modelMap)
			i++
		}
	})
}

// BenchmarkRealisticWorkloadMapping simulates realistic model mapping workload
func BenchmarkRealisticWorkloadMapping(b *testing.B) {
	// Realistic mapping configuration
	mappingJSON := `{
		"gpt-4": "gpt-4-turbo",
		"gpt-4-turbo": "gpt-4-turbo-preview",
		"gpt-3.5": "gpt-3.5-turbo",
		"claude-2": "claude-3-opus",
		"claude-3": "claude-3-opus-20240229",
		"gemini": "gemini-pro",
		"gemini-pro": "gemini-1.5-pro",
		"llama-2": "llama-3-70b",
		"mistral": "mistral-large",
		"qwen": "qwen-turbo"
	}`

	// Parse once for cached scenario
	modelMap, _ := ParseModelMapping(mappingJSON)

	// Realistic model distribution
	models := []string{
		"gpt-4", "gpt-4", "gpt-4", "gpt-4", "gpt-4", // 25% gpt-4
		"gpt-3.5", "gpt-3.5", "gpt-3.5", // 15% gpt-3.5
		"claude-2", "claude-2", "claude-3", // 15% claude
		"gemini", "gemini-pro", // 10% gemini
		"llama-2", "mistral", // 10% others
		"unknown-1", "unknown-2", "unknown-3", "unknown-4", // 20% unknown
		"custom-model", // 5% custom
	}

	b.Run("WithJSONParsing", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			model := models[i%len(models)]
			_, _, _ = ApplyModelMapping(model, mappingJSON)
		}
	})

	b.Run("WithCachedMap", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			model := models[i%len(models)]
			_, _, _ = ApplyModelMappingFromMap(model, modelMap)
		}
	})
}

// BenchmarkChainDepth benchmarks different chain depths
func BenchmarkChainDepth(b *testing.B) {
	depths := []int{1, 2, 5, 10, 20}

	for _, depth := range depths {
		b.Run(fmt.Sprintf("Depth%d", depth), func(b *testing.B) {
			modelMap := generateChainedMapping(depth)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _ = ApplyModelMappingFromMap("model-0", modelMap)
			}
		})
	}
}

// BenchmarkMappingSizes benchmarks different mapping sizes
func BenchmarkMappingSizes(b *testing.B) {
	sizes := []int{10, 50, 100, 500, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Size%d", size), func(b *testing.B) {
			mappingJSON := generateLargeMappingJSON(size)
			modelMap, _ := ParseModelMapping(mappingJSON)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				model := fmt.Sprintf("model-%d", i%size)
				_, _, _ = ApplyModelMappingFromMap(model, modelMap)
			}
		})
	}
}

// Helper functions

func generateLargeMappingJSON(size int) string {
	mapping := make(map[string]string, size)
	for i := 0; i < size; i++ {
		mapping[fmt.Sprintf("model-%d", i)] = fmt.Sprintf("target-%d", i)
	}
	data, _ := json.Marshal(mapping)
	return string(data)
}

func generateChainedMapping(depth int) map[string]string {
	mapping := make(map[string]string, depth)
	for i := 0; i < depth; i++ {
		mapping[fmt.Sprintf("model-%d", i)] = fmt.Sprintf("model-%d", i+1)
	}
	return mapping
}

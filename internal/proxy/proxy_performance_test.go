package proxy

import (
	"bytes"
	"encoding/json"
	"testing"

	"gpt-load/internal/models"
)

// BenchmarkApplyModelMapping benchmarks model name mapping in proxy
func BenchmarkApplyModelMapping(b *testing.B) {
	ps := &ProxyServer{}

	testCases := []struct {
		name        string
		modelMap    map[string]string
		requestBody string
	}{
		{
			name:        "NoMapping",
			modelMap:    map[string]string{},
			requestBody: `{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`,
		},
		{
			name: "SimpleMapping",
			modelMap: map[string]string{
				"gpt-4": "gpt-4-turbo",
			},
			requestBody: `{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`,
		},
		{
			name: "ComplexMapping",
			modelMap: map[string]string{
				"gpt-4":         "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
				"claude-2":      "claude-2.1",
				"gemini-pro":    "gemini-pro-vision",
			},
			requestBody: `{"model":"gpt-4","messages":[{"role":"user","content":"test"}],"temperature":0.7}`,
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			group := &models.Group{
				ModelRedirectMap: tc.modelMap,
			}
			bodyBytes := []byte(tc.requestBody)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, _ = ps.applyModelMapping(bodyBytes, group)
			}
		})
	}
}

// BenchmarkApplyParamOverrides benchmarks parameter override application
func BenchmarkApplyParamOverrides(b *testing.B) {
	ps := &ProxyServer{}

	testCases := []struct {
		name      string
		overrides map[string]any
		body      string
	}{
		{
			name:      "NoOverrides",
			overrides: map[string]any{},
			body:      `{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`,
		},
		{
			name: "SingleOverride",
			overrides: map[string]any{
				"temperature": 0.7,
			},
			body: `{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`,
		},
		{
			name: "MultipleOverrides",
			overrides: map[string]any{
				"temperature":       0.7,
				"max_tokens":        1000,
				"top_p":             0.9,
				"frequency_penalty": 0.5,
			},
			body: `{"model":"gpt-4","messages":[{"role":"user","content":"test"}],"stream":true}`,
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			group := &models.Group{
				ParamOverrides: tc.overrides,
			}
			bodyBytes := []byte(tc.body)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, _ = ps.applyParamOverrides(bodyBytes, group)
			}
		})
	}
}

// BenchmarkJSONMarshalUnmarshal benchmarks JSON operations in hot path
func BenchmarkJSONMarshalUnmarshal(b *testing.B) {
	requestBody := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Hello, how are you?"},
			{"role": "assistant", "content": "I'm doing well, thank you!"},
			{"role": "user", "content": "Can you help me with something?"},
		},
		"temperature": 0.7,
		"max_tokens":  1000,
		"stream":      false,
	}

	b.Run("Marshal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = json.Marshal(requestBody)
		}
	})

	bodyBytes, _ := json.Marshal(requestBody)

	b.Run("Unmarshal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var data map[string]any
			_ = json.Unmarshal(bodyBytes, &data)
		}
	})

	b.Run("MarshalUnmarshalCycle", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			data, _ := json.Marshal(requestBody)
			var result map[string]any
			_ = json.Unmarshal(data, &result)
		}
	})
}

// BenchmarkBufferOperations benchmarks buffer operations in request processing
func BenchmarkBufferOperations(b *testing.B) {
	testData := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"test message"}],"temperature":0.7}`)

	b.Run("BytesBuffer", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buf := bytes.NewBuffer(testData)
			_ = buf.Bytes()
		}
	})

	b.Run("BytesCopy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			dst := make([]byte, len(testData))
			copy(dst, testData)
		}
	})

	b.Run("BytesClone", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = bytes.Clone(testData)
		}
	})
}

// BenchmarkModelExtraction benchmarks extracting model name from request
func BenchmarkModelExtraction(b *testing.B) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "SimpleRequest",
			body: `{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`,
		},
		{
			name: "ComplexRequest",
			body: `{"model":"gpt-4-turbo-preview","messages":[{"role":"system","content":"You are a helpful assistant"},{"role":"user","content":"Hello"}],"temperature":0.7,"max_tokens":1000,"stream":false}`,
		},
		{
			name: "LargeRequest",
			body: `{"model":"claude-2.1","messages":[{"role":"user","content":"` + string(make([]byte, 1000)) + `"}],"temperature":0.8}`,
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			bodyBytes := []byte(tc.body)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				var data map[string]any
				_ = json.Unmarshal(bodyBytes, &data)
				_ = data["model"]
			}
		})
	}
}

// BenchmarkRealisticProxyWorkload simulates realistic proxy request processing
func BenchmarkRealisticProxyWorkload(b *testing.B) {
	ps := &ProxyServer{}

	// Simulate realistic group configuration
	group := &models.Group{
		Name: "test-group",
		ModelRedirectMap: map[string]string{
			"gpt-4":         "gpt-4-turbo",
			"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
		},
		ParamOverrides: map[string]any{
			"temperature": 0.7,
			"max_tokens":  2000,
		},
	}

	// Realistic request bodies
	requests := []string{
		`{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}`,
		`{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"Test"}],"stream":true}`,
		`{"model":"gpt-4","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Help me"}],"temperature":0.8}`,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		bodyBytes := []byte(requests[i%len(requests)])

		// Simulate full request processing pipeline
		bodyBytes, _ = ps.applyModelMapping(bodyBytes, group)
		bodyBytes, _ = ps.applyParamOverrides(bodyBytes, group)
	}
}

// BenchmarkConcurrentProxyOperations benchmarks concurrent request processing
func BenchmarkConcurrentProxyOperations(b *testing.B) {
	ps := &ProxyServer{}

	group := &models.Group{
		ModelRedirectMap: map[string]string{
			"gpt-4": "gpt-4-turbo",
		},
		ParamOverrides: map[string]any{
			"temperature": 0.7,
		},
	}

	requestBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bodyBytes := make([]byte, len(requestBody))
			copy(bodyBytes, requestBody)

			bodyBytes, _ = ps.applyModelMapping(bodyBytes, group)
			_, _ = ps.applyParamOverrides(bodyBytes, group)
		}
	})
}

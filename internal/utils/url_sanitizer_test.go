package utils

import (
	"net/url"
	"strings"
	"testing"
)

// TestSanitizeURLForLog tests URL sanitization for logging
func TestSanitizeURLForLog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		contains    []string
		notContains []string
	}{
		{
			"NilURL",
			"",
			[]string{},
			[]string{},
		},
		{
			"SimpleURL",
			"https://api.openai.com/v1/chat/completions",
			[]string{"https://api.openai.com"},
			[]string{},
		},
		{
			"URLWithAPIKey",
			"https://api.example.com/endpoint?api_key=secret123",
			[]string{"REDACTED"},
			[]string{"secret123"},
		},
		{
			"URLWithToken",
			"https://api.example.com/endpoint?token=abc123",
			[]string{"REDACTED"},
			[]string{"abc123"},
		},
		{
			"URLWithUserInfo",
			"https://user:pass@api.example.com/endpoint",
			[]string{"https://api.example.com"},
			[]string{"user", "pass"},
		},
		{
			"URLWithMultipleSensitiveParams",
			"https://api.example.com/endpoint?key=k1&token=t1&normal=value",
			[]string{"REDACTED", "normal=value"},
			[]string{"k1", "t1"},
		},
		{
			"URLWithCredentialKeyVariants",
			"https://api.example.com/endpoint?x-api-key=secret-a&openai_api_key=secret-b&subscription-key=secret-c&normal=value",
			[]string{"REDACTED", "normal=value"},
			[]string{"secret-a", "secret-b", "secret-c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u *url.URL
			if tt.input != "" {
				var err error
				u, err = url.Parse(tt.input)
				if err != nil {
					t.Fatalf("Failed to parse URL: %v", err)
				}
			}

			result := SanitizeURLForLog(u)

			// Verify nil input returns empty string
			if tt.input == "" && result != "" {
				t.Errorf("SanitizeURLForLog() with nil URL should return empty string, got %q", result)
			}

			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("SanitizeURLForLog() result should contain %q, got %q", s, result)
				}
			}

			for _, s := range tt.notContains {
				if strings.Contains(result, s) {
					t.Errorf("SanitizeURLForLog() result should not contain %q, got %q", s, result)
				}
			}
		})
	}
}

// TestSanitizeRequestURLForLog tests request URL sanitization
func TestSanitizeRequestURLForLog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		contains    []string
		notContains []string
		expectSame  bool // Expect result to be same as input
	}{
		{
			"EmptyString",
			"",
			[]string{},
			[]string{},
			true,
		},
		{
			"InvalidURL",
			"not a valid url",
			[]string{},
			[]string{},
			false, // url.Parse may modify the string
		},
		{
			"URLWithAPIKey",
			"https://api.example.com?api_key=secret",
			[]string{"REDACTED"},
			[]string{"secret"},
			false,
		},
		{
			"URLWithAccessToken",
			"https://api.example.com?access_token=token123",
			[]string{"REDACTED"},
			[]string{"token123"},
			false,
		},
		{
			"URLWithMixedCaseCredentialParams",
			"https://api.example.com/list?Api-Key=secret-a&subscriptionToken=secret-b&safe=value",
			[]string{"safe=value", "REDACTED"},
			[]string{"secret-a", "secret-b"},
			false,
		},
		{
			"URLWithCredentialKeyVariants",
			"https://api.example.com/list?x-api-key=secret-a&openai_api_key=secret-b&subscription-key=secret-c&safe=value",
			[]string{"safe=value", "REDACTED"},
			[]string{"secret-a", "secret-b", "secret-c"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeRequestURLForLog(tt.input)

			// Verify empty input returns empty string
			if tt.input == "" && result != "" {
				t.Errorf("SanitizeRequestURLForLog(%q) should return empty string, got %q", tt.input, result)
			}

			// Verify exact match when expected
			if tt.expectSame && result != tt.input {
				t.Errorf("SanitizeRequestURLForLog(%q) should return original input, got %q", tt.input, result)
			}

			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("SanitizeRequestURLForLog(%q) should contain %q, got %q", tt.input, s, result)
				}
			}

			for _, s := range tt.notContains {
				if strings.Contains(result, s) {
					t.Errorf("SanitizeRequestURLForLog(%q) should not contain %q, got %q", tt.input, s, result)
				}
			}
		})
	}
}

// TestSanitizeProxyURLForLog tests proxy URL sanitization
func TestSanitizeProxyURLForLog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		notContains []string
	}{
		{
			"NilURL",
			"",
			[]string{},
		},
		{
			"ProxyWithUserInfo",
			"http://user:pass@proxy.example.com:8080",
			[]string{"user", "pass"},
		},
		{
			"ProxyWithoutUserInfo",
			"http://proxy.example.com:8080",
			[]string{},
		},
		{
			"ProxyWithSensitiveQuery",
			"http://proxy.example.com:8080?token=raw-value-a&safe=value",
			[]string{"raw-value-a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u *url.URL
			if tt.input != "" {
				var err error
				u, err = url.Parse(tt.input)
				if err != nil {
					t.Fatalf("Failed to parse URL: %v", err)
				}
			}

			result := SanitizeProxyURLForLog(u)

			for _, s := range tt.notContains {
				if strings.Contains(result, s) {
					t.Errorf("SanitizeProxyURLForLog() should not contain %q, got %q", s, result)
				}
			}
		})
	}
}

// TestSanitizeProxyString tests proxy string sanitization
func TestSanitizeProxyString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		notContains []string
	}{
		{
			"EmptyString",
			"",
			[]string{},
		},
		{
			"ProxyWithUserInfo",
			"http://user:pass@proxy.example.com:8080",
			[]string{"user", "pass"},
		},
		{
			"ProxyWithoutUserInfo",
			"http://proxy.example.com:8080",
			[]string{},
		},
		{
			"InvalidProxyString",
			"not://a@valid@proxy",
			[]string{},
		},
		{
			"ProxyWithSpaces",
			"  http://user:pass@proxy.example.com:8080  ",
			[]string{"user", "pass"},
		},
		{
			"ProxyStringWithSensitiveQuery",
			"http://user:pass@proxy.example.com:8080?api_key=raw-value-b&safe=value",
			[]string{"user", "pass", "raw-value-b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeProxyString(tt.input)

			for _, s := range tt.notContains {
				if strings.Contains(result, s) {
					t.Errorf("SanitizeProxyString(%q) should not contain %q, got %q", tt.input, s, result)
				}
			}
		})
	}
}

// BenchmarkSanitizeURLForLog benchmarks URL sanitization
func BenchmarkSanitizeURLForLog(b *testing.B) {
	u, _ := url.Parse("https://user:pass@api.example.com/endpoint?api_key=secret&token=abc123&normal=value")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SanitizeURLForLog(u)
	}
}

// BenchmarkSanitizeRequestURLForLog benchmarks request URL sanitization
func BenchmarkSanitizeRequestURLForLog(b *testing.B) {
	urlStr := "https://api.example.com/endpoint?api_key=secret&token=abc123"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SanitizeRequestURLForLog(urlStr)
	}
}

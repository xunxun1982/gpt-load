package utils

import (
	"testing"
)

// TestBuildToolNameShortMap tests tool name shortening
func TestBuildToolNameShortMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		names    []string
		limit    int
		validate func(t *testing.T, result map[string]string)
	}{
		{
			"EmptyNames",
			[]string{},
			64,
			func(t *testing.T, result map[string]string) {
				if result != nil {
					t.Errorf("Expected nil for empty names, got %v", result)
				}
			},
		},
		{
			"NoShorteningNeeded",
			[]string{"tool1", "tool2", "tool3"},
			64,
			func(t *testing.T, result map[string]string) {
				if len(result) != 3 {
					t.Errorf("Expected 3 entries, got %d", len(result))
				}
				for _, name := range []string{"tool1", "tool2", "tool3"} {
					if result[name] != name {
						t.Errorf("Expected %q to map to itself, got %q", name, result[name])
					}
				}
			},
		},
		{
			"ShorteningNeeded",
			[]string{
				"this_is_a_very_long_tool_name_that_exceeds_the_limit",
				"short_tool",
			},
			20,
			func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(result))
				}
				for orig, short := range result {
					if len(short) > 20 {
						t.Errorf("Shortened name %q exceeds limit 20", short)
					}
					if orig == "short_tool" && short != "short_tool" {
						t.Errorf("Short name should not be modified: %q -> %q", orig, short)
					}
				}
			},
		},
		{
			"DuplicateNames",
			[]string{"tool1", "tool1", "tool2"},
			64,
			func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Errorf("Expected 2 unique entries, got %d", len(result))
				}
			},
		},
		{
			"MCPToolNames",
			[]string{
				"mcp__server__very_long_tool_name_that_needs_shortening",
				"mcp__server__short",
			},
			20,
			func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(result))
				}
				for _, short := range result {
					if len(short) > 20 {
						t.Errorf("Shortened name %q exceeds limit 20", short)
					}
				}
			},
		},
		{
			"CollisionHandling",
			[]string{
				"this_is_a_very_long_tool_name_1",
				"this_is_a_very_long_tool_name_2",
				"this_is_a_very_long_tool_name_3",
			},
			20,
			func(t *testing.T, result map[string]string) {
				if len(result) != 3 {
					t.Errorf("Expected 3 entries, got %d", len(result))
				}
				// Check all shortened names are unique
				seen := make(map[string]bool)
				for _, short := range result {
					if seen[short] {
						t.Errorf("Duplicate shortened name: %q", short)
					}
					seen[short] = true
					if len(short) > 20 {
						t.Errorf("Shortened name %q exceeds limit 20", short)
					}
				}
			},
		},
		{
			"ZeroLimit",
			[]string{"tool1", "tool2"},
			0,
			func(t *testing.T, result map[string]string) {
				// Should handle gracefully with limit=1
				if len(result) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(result))
				}
			},
		},
		{
			"NegativeLimit",
			[]string{"tool1", "tool2"},
			-1,
			func(t *testing.T, result map[string]string) {
				// Should handle gracefully with limit=1
				if len(result) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(result))
				}
			},
		},
		{
			"TinyLimit",
			[]string{"abc", "def"},
			2,
			func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(result))
				}
				for _, short := range result {
					if len(short) > 2 {
						t.Errorf("Shortened name %q exceeds limit 2", short)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildToolNameShortMap(tt.names, tt.limit)
			tt.validate(t, result)
		})
	}
}

// TestBuildReverseToolNameMap tests reverse mapping
func TestBuildReverseToolNameMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		shortMap map[string]string
		validate func(t *testing.T, result map[string]string)
	}{
		{
			"EmptyMap",
			map[string]string{},
			func(t *testing.T, result map[string]string) {
				if len(result) != 0 {
					t.Errorf("Expected empty map, got %d entries", len(result))
				}
			},
		},
		{
			"SimpleMap",
			map[string]string{
				"original1": "short1",
				"original2": "short2",
			},
			func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(result))
				}
				if result["short1"] != "original1" {
					t.Errorf("Expected short1 -> original1, got %q", result["short1"])
				}
				if result["short2"] != "original2" {
					t.Errorf("Expected short2 -> original2, got %q", result["short2"])
				}
			},
		},
		{
			"IdentityMap",
			map[string]string{
				"tool1": "tool1",
				"tool2": "tool2",
			},
			func(t *testing.T, result map[string]string) {
				if len(result) != 2 {
					t.Errorf("Expected 2 entries, got %d", len(result))
				}
				for k, v := range result {
					if k != v {
						t.Errorf("Expected identity mapping, got %q -> %q", k, v)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildReverseToolNameMap(tt.shortMap)
			tt.validate(t, result)
		})
	}
}

// BenchmarkBuildToolNameShortMap benchmarks tool name shortening
func BenchmarkBuildToolNameShortMap(b *testing.B) {
	names := []string{
		"tool1",
		"tool2",
		"this_is_a_very_long_tool_name_that_exceeds_the_limit_significantly",
		"another_long_tool_name_that_needs_shortening",
		"short",
		"mcp__server__very_long_tool_name",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildToolNameShortMap(names, 64)
	}
}

// BenchmarkBuildToolNameShortMapNoShortening benchmarks when no shortening needed
func BenchmarkBuildToolNameShortMapNoShortening(b *testing.B) {
	names := []string{
		"tool1",
		"tool2",
		"tool3",
		"tool4",
		"tool5",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildToolNameShortMap(names, 64)
	}
}

// BenchmarkBuildReverseToolNameMap benchmarks reverse mapping
func BenchmarkBuildReverseToolNameMap(b *testing.B) {
	shortMap := map[string]string{
		"original1": "short1",
		"original2": "short2",
		"original3": "short3",
		"original4": "short4",
		"original5": "short5",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildReverseToolNameMap(shortMap)
	}
}

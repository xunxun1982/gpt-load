package utils

import (
	"testing"
)

// TestMigrateModelMappingToRedirectRules tests model mapping migration
func TestMigrateModelMappingToRedirectRules(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			"EmptyString",
			"",
			nil,
			false,
		},
		{
			"OldFormatSingle",
			"gpt-3.5-turbo:gpt-3.5-turbo-0301",
			map[string]string{"gpt-3.5-turbo": "gpt-3.5-turbo-0301"},
			false,
		},
		{
			"OldFormatMultiple",
			"gpt-3.5-turbo:gpt-3.5-turbo-0301 gpt-4:gpt-4-0314",
			map[string]string{
				"gpt-3.5-turbo": "gpt-3.5-turbo-0301",
				"gpt-4":         "gpt-4-0314",
			},
			false,
		},
		{
			"JSONFormat",
			`{"gpt-3.5-turbo":"gpt-3.5-turbo-0301","gpt-4":"gpt-4-0314"}`,
			map[string]string{
				"gpt-3.5-turbo": "gpt-3.5-turbo-0301",
				"gpt-4":         "gpt-4-0314",
			},
			false,
		},
		{
			"InvalidFormatNoColon",
			"gpt-3.5-turbo gpt-4",
			nil,
			true,
		},
		{
			"InvalidFormatEmptyKey",
			":gpt-3.5-turbo-0301",
			nil,
			true,
		},
		{
			"InvalidFormatEmptyValue",
			"gpt-3.5-turbo:",
			nil,
			true,
		},
		{
			"OldFormatWithSpaces",
			"  gpt-3.5-turbo:gpt-3.5-turbo-0301   gpt-4:gpt-4-0314  ",
			map[string]string{
				"gpt-3.5-turbo": "gpt-3.5-turbo-0301",
				"gpt-4":         "gpt-4-0314",
			},
			false,
		},
		{
			"ComplexModelNames",
			"claude-3-opus-20240229:claude-3-opus gemini-1.5-pro:gemini-pro",
			map[string]string{
				"claude-3-opus-20240229": "claude-3-opus",
				"gemini-1.5-pro":         "gemini-pro",
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MigrateModelMappingToRedirectRules(tt.input)

			if (err != nil) != tt.wantErr {
				t.Errorf("MigrateModelMappingToRedirectRules() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if tt.want == nil {
				if got != nil {
					t.Errorf("MigrateModelMappingToRedirectRules() = %v, want nil", got)
				}
				return
			}

			if len(got) != len(tt.want) {
				t.Errorf("MigrateModelMappingToRedirectRules() length = %d, want %d", len(got), len(tt.want))
				return
			}

			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("MigrateModelMappingToRedirectRules()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestMigrateModelMappingToRedirectRulesJSONPriority tests JSON format priority
func TestMigrateModelMappingToRedirectRulesJSONPriority(t *testing.T) {
	// If input is valid JSON, it should be parsed as JSON even if it looks like old format
	input := `{"gpt-4":"gpt-4-turbo"}`
	got, err := MigrateModelMappingToRedirectRules(input)

	if err != nil {
		t.Fatalf("MigrateModelMappingToRedirectRules() error = %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("MigrateModelMappingToRedirectRules() length = %d, want 1", len(got))
	}

	if got["gpt-4"] != "gpt-4-turbo" {
		t.Errorf("MigrateModelMappingToRedirectRules()[\"gpt-4\"] = %q, want \"gpt-4-turbo\"", got["gpt-4"])
	}
}

// BenchmarkMigrateModelMappingOldFormat benchmarks old format migration
func BenchmarkMigrateModelMappingOldFormat(b *testing.B) {
	input := "gpt-3.5-turbo:gpt-3.5-turbo-0301 gpt-4:gpt-4-0314 claude-3:claude-3-opus"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MigrateModelMappingToRedirectRules(input)
	}
}

// BenchmarkMigrateModelMappingJSONFormat benchmarks JSON format migration
func BenchmarkMigrateModelMappingJSONFormat(b *testing.B) {
	input := `{"gpt-3.5-turbo":"gpt-3.5-turbo-0301","gpt-4":"gpt-4-0314","claude-3":"claude-3-opus"}`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MigrateModelMappingToRedirectRules(input)
	}
}

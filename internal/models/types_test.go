package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGroup_GetMaxRequestSizeKB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		preconditions map[string]any
		want          int
	}{
		{
			name:          "nil preconditions",
			preconditions: nil,
			want:          0,
		},
		{
			name:          "empty preconditions",
			preconditions: map[string]any{},
			want:          0,
		},
		{
			name: "valid int value",
			preconditions: map[string]any{
				"max_request_size_kb": 1024,
			},
			want: 1024,
		},
		{
			name: "valid int64 value",
			preconditions: map[string]any{
				"max_request_size_kb": int64(2048),
			},
			want: 2048,
		},
		{
			name: "valid float64 value",
			preconditions: map[string]any{
				"max_request_size_kb": float64(512),
			},
			want: 512,
		},
		{
			name: "json.Number value",
			preconditions: map[string]any{
				"max_request_size_kb": json.Number("4096"),
			},
			want: 4096,
		},
		{
			name: "negative value normalized to 0",
			preconditions: map[string]any{
				"max_request_size_kb": -100,
			},
			want: 0,
		},
		{
			name: "zero value",
			preconditions: map[string]any{
				"max_request_size_kb": 0,
			},
			want: 0,
		},
		{
			name: "invalid string value",
			preconditions: map[string]any{
				"max_request_size_kb": "invalid",
			},
			want: 0,
		},
		{
			name: "invalid bool value",
			preconditions: map[string]any{
				"max_request_size_kb": true,
			},
			want: 0,
		},
		{
			name: "invalid json.Number",
			preconditions: map[string]any{
				"max_request_size_kb": json.Number("not-a-number"),
			},
			want: 0,
		},
		{
			name: "large value",
			preconditions: map[string]any{
				"max_request_size_kb": 1048576, // 1GB
			},
			want: 1048576,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Group{
				Preconditions: tt.preconditions,
			}
			if got := g.GetMaxRequestSizeKB(); got != tt.want {
				t.Errorf("Group.GetMaxRequestSizeKB() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDynamicWeightMetric_IsDeleted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		metric  *DynamicWeightMetric
		want    bool
	}{
		{
			name: "not deleted - nil DeletedAt",
			metric: &DynamicWeightMetric{
				DeletedAt: nil,
			},
			want: false,
		},
		{
			name: "deleted - has DeletedAt",
			metric: &DynamicWeightMetric{
				DeletedAt: func() *time.Time { now := time.Now(); return &now }(),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.metric.IsDeleted(); got != tt.want {
				t.Errorf("DynamicWeightMetric.IsDeleted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultTimeWindowConfigs(t *testing.T) {
	t.Parallel()

	configs := DefaultTimeWindowConfigs()

	// Verify we have 5 time windows
	if len(configs) != 5 {
		t.Fatalf("DefaultTimeWindowConfigs() returned %d windows, want 5", len(configs))
	}

	// Verify the expected windows
	expectedWindows := []struct {
		days   int
		weight float64
	}{
		{7, 1.0},
		{14, 0.8},
		{30, 0.6},
		{90, 0.3},
		{180, 0.1},
	}

	for i, expected := range expectedWindows {
		if configs[i].Days != expected.days {
			t.Errorf("Window %d: Days = %d, want %d", i, configs[i].Days, expected.days)
		}
		if configs[i].Weight != expected.weight {
			t.Errorf("Window %d: Weight = %f, want %f", i, configs[i].Weight, expected.weight)
		}
	}

	// Verify weights are in descending order (recent data has higher weight)
	for i := 1; i < len(configs); i++ {
		if configs[i].Weight > configs[i-1].Weight {
			t.Errorf("Window %d weight (%f) should not be greater than window %d weight (%f)",
				i, configs[i].Weight, i-1, configs[i-1].Weight)
		}
	}

	// Verify days are in ascending order
	for i := 1; i < len(configs); i++ {
		if configs[i].Days <= configs[i-1].Days {
			t.Errorf("Window %d days (%d) should be greater than window %d days (%d)",
				i, configs[i].Days, i-1, configs[i-1].Days)
		}
	}
}

func TestDynamicWeightMetric_TableName(t *testing.T) {
	t.Parallel()

	m := DynamicWeightMetric{}
	if got := m.TableName(); got != "dynamic_weight_metrics" {
		t.Errorf("DynamicWeightMetric.TableName() = %v, want %v", got, "dynamic_weight_metrics")
	}
}

// BenchmarkGetMaxRequestSizeKB benchmarks the GetMaxRequestSizeKB method
func BenchmarkGetMaxRequestSizeKB(b *testing.B) {
	g := &Group{
		Preconditions: map[string]any{
			"max_request_size_kb": 1024,
		},
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = g.GetMaxRequestSizeKB()
	}
}

// BenchmarkGetMaxRequestSizeKBJSONNumber benchmarks with json.Number type
func BenchmarkGetMaxRequestSizeKBJSONNumber(b *testing.B) {
	g := &Group{
		Preconditions: map[string]any{
			"max_request_size_kb": json.Number("2048"),
		},
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = g.GetMaxRequestSizeKB()
	}
}

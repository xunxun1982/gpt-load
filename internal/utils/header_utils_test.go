package utils

import (
	"gpt-load/internal/models"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestResolveHeaderVariables tests header variable resolution
func TestResolveHeaderVariables(t *testing.T) {
	t.Parallel()
	group := &models.Group{Name: "test-group"}
	apiKey := &models.APIKey{KeyValue: "test-key-123"}
	ctx := &HeaderVariableContext{
		ClientIP: "192.168.1.1",
		Group:    group,
		APIKey:   apiKey,
	}

	tests := []struct {
		name  string
		value string
		ctx   *HeaderVariableContext
		check func(t *testing.T, result string)
	}{
		{
			"NoVariables",
			"static-value",
			ctx,
			func(t *testing.T, result string) {
				if result != "static-value" {
					t.Errorf("Expected 'static-value', got %q", result)
				}
			},
		},
		{
			"ClientIP",
			"IP: ${CLIENT_IP}",
			ctx,
			func(t *testing.T, result string) {
				if result != "IP: 192.168.1.1" {
					t.Errorf("Expected 'IP: 192.168.1.1', got %q", result)
				}
			},
		},
		{
			"GroupName",
			"Group: ${GROUP_NAME}",
			ctx,
			func(t *testing.T, result string) {
				if result != "Group: test-group" {
					t.Errorf("Expected 'Group: test-group', got %q", result)
				}
			},
		},
		{
			"APIKey",
			"Key: ${API_KEY}",
			ctx,
			func(t *testing.T, result string) {
				if result != "Key: test-key-123" {
					t.Errorf("Expected 'Key: test-key-123', got %q", result)
				}
			},
		},
		{
			"TimestampMS",
			"Time: ${TIMESTAMP_MS}",
			ctx,
			func(t *testing.T, result string) {
				if strings.Contains(result, "${") {
					t.Errorf("Timestamp placeholder not replaced: %q", result)
				}
				ts := strings.TrimPrefix(result, "Time: ")
				if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
					t.Errorf("Timestamp not numeric: %q", result)
				}
			},
		},
		{
			"TimestampS",
			"Time: ${TIMESTAMP_S}",
			ctx,
			func(t *testing.T, result string) {
				if strings.Contains(result, "${") {
					t.Errorf("Timestamp placeholder not replaced: %q", result)
				}
				ts := strings.TrimPrefix(result, "Time: ")
				if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
					t.Errorf("Timestamp not numeric: %q", result)
				}
			},
		},
		{
			"MultipleVariables",
			"${CLIENT_IP}-${GROUP_NAME}",
			ctx,
			func(t *testing.T, result string) {
				if result != "192.168.1.1-test-group" {
					t.Errorf("Expected '192.168.1.1-test-group', got %q", result)
				}
			},
		},
		{
			"NilContext",
			"${CLIENT_IP}",
			nil,
			func(t *testing.T, result string) {
				if result != "${CLIENT_IP}" {
					t.Errorf("Expected '${CLIENT_IP}', got %q", result)
				}
			},
		},
		{
			"NilGroup",
			"${GROUP_NAME}",
			&HeaderVariableContext{ClientIP: "1.2.3.4"},
			func(t *testing.T, result string) {
				// When group is nil, variable is not replaced
				if result != "${GROUP_NAME}" {
					t.Errorf("Expected '${GROUP_NAME}', got %q", result)
				}
			},
		},
		{
			"NilAPIKey",
			"${API_KEY}",
			&HeaderVariableContext{ClientIP: "1.2.3.4", Group: &models.Group{Name: "test"}},
			func(t *testing.T, result string) {
				// When APIKey is nil, variable is not replaced
				if result != "${API_KEY}" {
					t.Errorf("Expected '${API_KEY}', got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveHeaderVariables(tt.value, tt.ctx)
			tt.check(t, result)
		})
	}
}

// TestApplyHeaderRules tests header rule application
func TestApplyHeaderRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rules []models.HeaderRule
		check func(t *testing.T, req *http.Request)
	}{
		{
			"RemoveHeader",
			[]models.HeaderRule{
				{Key: "X-Test-Header", Action: "remove"},
			},
			func(t *testing.T, req *http.Request) {
				if req.Header.Get("X-Test-Header") != "" {
					t.Error("Header should be removed")
				}
			},
		},
		{
			"SetHeader",
			[]models.HeaderRule{
				{Key: "X-Custom-Header", Action: "set", Value: "custom-value"},
			},
			func(t *testing.T, req *http.Request) {
				if req.Header.Get("X-Custom-Header") != "custom-value" {
					t.Errorf("Header value = %q, want 'custom-value'", req.Header.Get("X-Custom-Header"))
				}
			},
		},
		{
			"SetHeaderWithVariable",
			[]models.HeaderRule{
				{Key: "X-Client-IP", Action: "set", Value: "${CLIENT_IP}"},
			},
			func(t *testing.T, req *http.Request) {
				if req.Header.Get("X-Client-IP") != "192.168.1.1" {
					t.Errorf("Header value = %q, want '192.168.1.1'", req.Header.Get("X-Client-IP"))
				}
			},
		},
		{
			"MultipleRules",
			[]models.HeaderRule{
				{Key: "X-Remove-Me", Action: "remove"},
				{Key: "X-Set-Me", Action: "set", Value: "value"},
			},
			func(t *testing.T, req *http.Request) {
				if req.Header.Get("X-Remove-Me") != "" {
					t.Error("X-Remove-Me should be removed")
				}
				if req.Header.Get("X-Set-Me") != "value" {
					t.Error("X-Set-Me should be set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			req.Header.Set("X-Test-Header", "test")
			req.Header.Set("X-Remove-Me", "remove")

			ctx := &HeaderVariableContext{ClientIP: "192.168.1.1"}
			ApplyHeaderRules(req, tt.rules, ctx)
			tt.check(t, req)
		})
	}
}

// TestApplyHeaderRulesNilRequest tests nil request handling
func TestApplyHeaderRulesNilRequest(t *testing.T) {
	t.Parallel()
	rules := []models.HeaderRule{
		{Key: "X-Test", Action: "set", Value: "value"},
	}
	// Should not panic
	ApplyHeaderRules(nil, rules, nil)
}

// TestApplyHeaderRulesEmptyRules tests empty rules handling
func TestApplyHeaderRulesEmptyRules(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	req.Header.Set("X-Test", "value")

	ApplyHeaderRules(req, []models.HeaderRule{}, nil)

	// Header should remain unchanged
	if req.Header.Get("X-Test") != "value" {
		t.Error("Header should remain unchanged")
	}
}

// TestNewHeaderVariableContext tests context creation
func TestNewHeaderVariableContext(t *testing.T) {
	t.Parallel()
	group := &models.Group{Name: "test"}
	apiKey := &models.APIKey{KeyValue: "key"}

	ctx := NewHeaderVariableContext(group, apiKey)

	if ctx == nil {
		t.Fatal("Context should not be nil")
	}

	if ctx.ClientIP != "127.0.0.1" {
		t.Errorf("ClientIP = %q, want '127.0.0.1'", ctx.ClientIP)
	}

	if ctx.Group != group {
		t.Error("Group not set correctly")
	}

	if ctx.APIKey != apiKey {
		t.Error("APIKey not set correctly")
	}
}

// BenchmarkResolveHeaderVariables benchmarks variable resolution
func BenchmarkResolveHeaderVariables(b *testing.B) {
	b.ReportAllocs()
	ctx := &HeaderVariableContext{
		ClientIP: "192.168.1.1",
		Group:    &models.Group{Name: "test-group"},
		APIKey:   &models.APIKey{KeyValue: "test-key"},
	}
	value := "${CLIENT_IP}-${GROUP_NAME}-${TIMESTAMP_MS}"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ResolveHeaderVariables(value, ctx)
	}
}

// BenchmarkApplyHeaderRules benchmarks header rule application
func BenchmarkApplyHeaderRules(b *testing.B) {
	rules := []models.HeaderRule{
		{Key: "X-Remove-1", Action: "remove"},
		{Key: "X-Remove-2", Action: "remove"},
		{Key: "X-Set-1", Action: "set", Value: "value1"},
		{Key: "X-Set-2", Action: "set", Value: "${CLIENT_IP}"},
	}
	ctx := &HeaderVariableContext{ClientIP: "192.168.1.1"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", "http://example.com", nil)
		req.Header.Set("X-Remove-1", "test")
		req.Header.Set("X-Remove-2", "test")
		ApplyHeaderRules(req, rules, ctx)
	}
}

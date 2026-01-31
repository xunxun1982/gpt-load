package main

import (
	"testing"
)

// TestBuildAddress verifies that buildAddress constructs correct TCP addresses
func TestBuildAddress(t *testing.T) {
	tests := []struct {
		name     string
		port     string
		expected string
	}{
		{
			name:     "Default port",
			port:     "3001",
			expected: "127.0.0.1:3001",
		},
		{
			name:     "Custom port",
			port:     "8080",
			expected: "127.0.0.1:8080",
		},
		{
			name:     "High port number",
			port:     "65535",
			expected: "127.0.0.1:65535",
		},
		{
			name:     "Low port number",
			port:     "80",
			expected: "127.0.0.1:80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAddress(tt.port)
			if result != tt.expected {
				t.Errorf("buildAddress(%q) = %q, want %q", tt.port, result, tt.expected)
			}
		})
	}
}

// TestBuildAddressUsesIPv4 ensures buildAddress always uses 127.0.0.1 instead of localhost
// This is critical for scratch-based Docker images without /etc/hosts
func TestBuildAddressUsesIPv4(t *testing.T) {
	address := buildAddress("3001")

	// Verify it uses 127.0.0.1 (not localhost)
	if address != "127.0.0.1:3001" {
		t.Errorf("buildAddress must use 127.0.0.1, got %q", address)
	}

	// Verify it doesn't contain "localhost"
	if len(address) >= 9 && address[:9] == "localhost" {
		t.Error("buildAddress must not use 'localhost' for scratch image compatibility")
	}
}

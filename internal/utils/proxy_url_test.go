package utils

import (
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeProxyURLAcceptsValidSchemes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "http",
			in:   "http://proxy.example:8080",
			want: "http://proxy.example:8080",
		},
		{
			name: "https",
			in:   "https://proxy.example:8080",
			want: "https://proxy.example:8080",
		},
		{
			name: "socks5",
			in:   "socks5://proxy.example:1080",
			want: "socks5://proxy.example:1080",
		},
		{
			name: "trim whitespace",
			in:   "  http://proxy.example:8080  ",
			want: "http://proxy.example:8080",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "blank",
			in:   "   ",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProxyURL(tt.in)
			if err != nil {
				t.Fatalf("NormalizeProxyURL(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeProxyURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeProxyURLRejectsUnsupportedSchemeFromBareHostPort(t *testing.T) {
	t.Parallel()

	_, err := NormalizeProxyURL("proxy.example.com:8080")

	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported proxy scheme") {
		t.Fatalf("expected unsupported scheme error, got %q", err.Error())
	}
}

func TestNormalizeProxyURLRejectsMissingHost(t *testing.T) {
	t.Parallel()

	_, err := NormalizeProxyURL("http://")

	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing host") {
		t.Fatalf("expected missing host error, got %q", err.Error())
	}
}

func TestNormalizeProxyURLParseErrorDoesNotLeakCredentials(t *testing.T) {
	t.Parallel()

	userInfo := url.UserPassword("u", "p").String()
	_, err := NormalizeProxyURL("http://" + userInfo + "@[::1")

	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid proxy URL") {
		t.Fatalf("expected invalid proxy URL error, got %q", err.Error())
	}
	if strings.Contains(err.Error(), userInfo) {
		t.Fatalf("proxy credentials leaked in error: %q", err.Error())
	}
}

package utils

import (
	"net/url"
	"strings"
	"testing"
)

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

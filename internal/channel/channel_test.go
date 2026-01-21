package channel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUpstreamSelection_Structure(t *testing.T) {
	t.Parallel()

	// Test UpstreamSelection structure
	proxyURL := "http://proxy.example.com:8080"
	selection := &UpstreamSelection{
		URL:          "https://api.example.com",
		HTTPClient:   nil,
		StreamClient: nil,
		ProxyURL:     &proxyURL,
	}

	assert.NotNil(t, selection)
	assert.Equal(t, "https://api.example.com", selection.URL)
	assert.NotNil(t, selection.ProxyURL)
	assert.Equal(t, proxyURL, *selection.ProxyURL)
}

func TestUpstreamSelection_NilProxyURL(t *testing.T) {
	t.Parallel()

	// Test UpstreamSelection with nil proxy URL
	selection := &UpstreamSelection{
		URL:          "https://api.example.com",
		HTTPClient:   nil,
		StreamClient: nil,
		ProxyURL:     nil,
	}

	assert.NotNil(t, selection)
	assert.Nil(t, selection.ProxyURL)
}

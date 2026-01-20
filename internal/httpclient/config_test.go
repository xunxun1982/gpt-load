package httpclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestConfig_FingerprintEmpty tests fingerprint with empty config
func TestConfig_FingerprintEmpty(t *testing.T) {
	config := &Config{}
	fp := config.getFingerprint()
	assert.NotEmpty(t, fp)
}

// TestConfig_FingerprintWithTimeouts tests fingerprint with various timeouts
func TestConfig_FingerprintWithTimeouts(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
	}{
		{
			name: "all timeouts",
			config: &Config{
				ConnectTimeout:        5 * time.Second,
				RequestTimeout:        30 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ExpectContinueTimeout: 2 * time.Second,
			},
		},
		{
			name:   "no timeouts",
			config: &Config{},
		},
		{
			name: "partial timeouts",
			config: &Config{
				ConnectTimeout: 5 * time.Second,
				RequestTimeout: 30 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp := tt.config.getFingerprint()
			assert.NotEmpty(t, fp)
		})
	}
}

// TestConfig_FingerprintWithPoolSettings tests fingerprint with pool settings
func TestConfig_FingerprintWithPoolSettings(t *testing.T) {
	config1 := &Config{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	}
	config2 := &Config{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 20,
	}

	fp1 := config1.getFingerprint()
	fp2 := config2.getFingerprint()

	assert.NotEqual(t, fp1, fp2)
}

// TestConfig_FingerprintWithCompression tests fingerprint with compression settings
func TestConfig_FingerprintWithCompression(t *testing.T) {
	config1 := &Config{
		DisableCompression: true,
	}
	config2 := &Config{
		DisableCompression: false,
	}

	fp1 := config1.getFingerprint()
	fp2 := config2.getFingerprint()

	assert.NotEqual(t, fp1, fp2)
}

// TestConfig_FingerprintWithBufferSizes tests fingerprint with buffer sizes
func TestConfig_FingerprintWithBufferSizes(t *testing.T) {
	config1 := &Config{
		WriteBufferSize: 4096,
		ReadBufferSize:  4096,
	}
	config2 := &Config{
		WriteBufferSize: 8192,
		ReadBufferSize:  8192,
	}

	fp1 := config1.getFingerprint()
	fp2 := config2.getFingerprint()

	assert.NotEqual(t, fp1, fp2)
}

// TestConfig_FingerprintWithHTTP2 tests fingerprint with HTTP/2 setting
func TestConfig_FingerprintWithHTTP2(t *testing.T) {
	config1 := &Config{
		ForceAttemptHTTP2: true,
	}
	config2 := &Config{
		ForceAttemptHTTP2: false,
	}

	fp1 := config1.getFingerprint()
	fp2 := config2.getFingerprint()

	assert.NotEqual(t, fp1, fp2)
}

// TestConfig_FingerprintWithProxy tests fingerprint with proxy settings
func TestConfig_FingerprintWithProxy(t *testing.T) {
	config1 := &Config{
		ProxyURL: "http://proxy1.example.com:8080",
	}
	config2 := &Config{
		ProxyURL: "http://proxy2.example.com:8080",
	}
	config3 := &Config{
		ProxyURL: "  http://proxy1.example.com:8080  ", // With whitespace
	}

	fp1 := config1.getFingerprint()
	fp2 := config2.getFingerprint()
	fp3 := config3.getFingerprint()

	assert.NotEqual(t, fp1, fp2)
	assert.Equal(t, fp1, fp3) // Whitespace should be trimmed
}

// TestConfig_FingerprintWithAllSettings tests fingerprint with all settings
func TestConfig_FingerprintWithAllSettings(t *testing.T) {
	config := &Config{
		ConnectTimeout:        10 * time.Second,
		RequestTimeout:        30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   100,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true,
		WriteBufferSize:       4096,
		ReadBufferSize:        4096,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		ProxyURL:              "http://proxy.example.com:8080",
	}

	fp := config.getFingerprint()
	assert.NotEmpty(t, fp)

	// Verify fingerprint is consistent
	fp2 := config.getFingerprint()
	assert.Equal(t, fp, fp2)
}

// BenchmarkConfigFingerprint benchmarks fingerprint generation with various configs
func BenchmarkConfigFingerprint(b *testing.B) {
	configs := []*Config{
		{},
		{
			ConnectTimeout: 10 * time.Second,
			RequestTimeout: 30 * time.Second,
		},
		{
			ConnectTimeout:        10 * time.Second,
			RequestTimeout:        30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   100,
			ResponseHeaderTimeout: 60 * time.Second,
			DisableCompression:    true,
			WriteBufferSize:       4096,
			ReadBufferSize:        4096,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 2 * time.Second,
			ProxyURL:              "http://proxy.example.com:8080",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = configs[i%len(configs)].getFingerprint()
	}
}

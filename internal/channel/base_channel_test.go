package channel

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// mustParseURL is a test helper that parses a URL or panics
func mustParseURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u
}

func restoreGatewayProxyBaseURL(id, previous string) {
	if strings.TrimSpace(previous) == "" {
		DisableGatewayProxyBaseURL(id)
		return
	}
	SetGatewayProxyBaseURL(id, previous)
}

// TestSelectUpstream tests upstream selection logic
func TestSelectUpstream(t *testing.T) {
	tests := []struct {
		name      string
		upstreams []UpstreamInfo
		wantNil   bool
	}{
		{
			"NoUpstreams",
			[]UpstreamInfo{},
			true,
		},
		{
			"SingleUpstream",
			[]UpstreamInfo{
				{URL: mustParseURL("https://api.openai.com"), Weight: 100},
			},
			false,
		},
		{
			"MultipleUpstreams",
			[]UpstreamInfo{
				{URL: mustParseURL("https://api1.openai.com"), Weight: 100},
				{URL: mustParseURL("https://api2.openai.com"), Weight: 200},
			},
			false,
		},
		{
			"AllZeroWeights",
			[]UpstreamInfo{
				{URL: mustParseURL("https://api1.openai.com"), Weight: 0},
				{URL: mustParseURL("https://api2.openai.com"), Weight: 0},
			},
			true,
		},
		{
			"SomeZeroWeights",
			[]UpstreamInfo{
				{URL: mustParseURL("https://api1.openai.com"), Weight: 100},
				{URL: mustParseURL("https://api2.openai.com"), Weight: 0},
				{URL: mustParseURL("https://api3.openai.com"), Weight: 200},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BaseChannel{
				Upstreams: tt.upstreams,
			}

			result := bc.SelectUpstream()

			if tt.wantNil {
				if result != nil {
					t.Errorf("SelectUpstream() = %v, want nil", result)
				}
			} else {
				if result == nil {
					t.Error("SelectUpstream() = nil, want non-nil")
				} else if result.Weight == 0 {
					t.Error("SelectUpstream() selected upstream with zero weight")
				}
			}
		})
	}
}

// TestSelectUpstreamDistribution tests that selection respects weights
func TestSelectUpstreamDistribution(t *testing.T) {
	upstreams := []UpstreamInfo{
		{URL: mustParseURL("https://api1.openai.com"), Weight: 100},
		{URL: mustParseURL("https://api2.openai.com"), Weight: 200},
		{URL: mustParseURL("https://api3.openai.com"), Weight: 300},
	}

	bc := &BaseChannel{
		Upstreams: upstreams,
	}

	// Run selection many times and count results
	// Using 10000 iterations for better statistical confidence
	counts := make(map[string]int)
	iterations := 10000

	for i := 0; i < iterations; i++ {
		result := bc.SelectUpstream()
		if result != nil {
			counts[result.URL.String()]++
		}
	}

	// All upstreams should be selected at least once
	if len(counts) != 3 {
		t.Errorf("Expected all 3 upstreams to be selected, got %d", len(counts))
	}

	// Higher weight should generally get more selections
	// Note: This test is probabilistic by nature. With 10000 iterations and weights 100:200:300,
	// the ordering should hold with high confidence, but rare failures are theoretically possible.
	url1 := "https://api1.openai.com"
	url2 := "https://api2.openai.com"
	url3 := "https://api3.openai.com"

	// Add tolerance-based assertion for better bug detection
	// Expected ratios: ~16.7%, ~33.3%, ~50% for weights 100:200:300
	totalWeight := float64(100 + 200 + 300)
	for urlStr, weight := range map[string]float64{url1: 100, url2: 200, url3: 300} {
		expected := float64(iterations) * (weight / totalWeight)
		actual := float64(counts[urlStr])
		deviation := (actual - expected) / expected
		if deviation < -0.5 || deviation > 0.5 {
			t.Errorf("Extreme deviation for %s: got %d, expected ~%.0f (%.1f%% off)", urlStr, counts[urlStr], expected, deviation*100)
		}
	}

	if counts[url1] > counts[url2] {
		t.Logf("Warning: Lower weight upstream selected more often (url1: %d, url2: %d)", counts[url1], counts[url2])
	}
	if counts[url2] > counts[url3] {
		t.Logf("Warning: Lower weight upstream selected more often (url2: %d, url3: %d)", counts[url2], counts[url3])
	}
}

// TestGetUpstreamURL tests deprecated getUpstreamURL method
func TestGetUpstreamURL(t *testing.T) {
	tests := []struct {
		name      string
		upstreams []UpstreamInfo
		wantNil   bool
	}{
		{
			"NoUpstreams",
			[]UpstreamInfo{},
			true,
		},
		{
			"SingleUpstream",
			[]UpstreamInfo{
				{URL: mustParseURL("https://api.openai.com"), Weight: 100},
			},
			false,
		},
		{
			"AllZeroWeights",
			[]UpstreamInfo{
				{URL: mustParseURL("https://api.openai.com"), Weight: 0},
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BaseChannel{
				Upstreams: tt.upstreams,
			}

			result := bc.getUpstreamURL()

			if tt.wantNil {
				if result != nil {
					t.Errorf("getUpstreamURL() = %v, want nil", result)
				}
			} else {
				if result == nil {
					t.Error("getUpstreamURL() = nil, want non-nil")
				}
			}
		})
	}
}

func TestSelectUpstreamWithClientsAppliesGatewayProxy(t *testing.T) {
	tests := []struct {
		name        string
		channelName string
		groupName   string
		baseURL     string
		originalURL string
		wantURL     string
	}{
		{
			name:        "openai chat",
			channelName: "openai",
			groupName:   "openai-group",
			baseURL:     "https://api.openai.com",
			originalURL: "/proxy/openai-group/v1/chat/completions?stream=true",
			wantURL:     "https://betterclau.de/openai/api.openai.com/v1/chat/completions?stream=true",
		},
		{
			name:        "openai responses",
			channelName: "openai-response",
			groupName:   "responses-group",
			baseURL:     "https://api.openai.com",
			originalURL: "/proxy/responses-group/v1/responses",
			wantURL:     "https://betterclau.de/openai/api.openai.com/v1/responses",
		},
		{
			name:        "anthropic claude",
			channelName: "anthropic",
			groupName:   "claude-group",
			baseURL:     "https://api.anthropic.com",
			originalURL: "/proxy/claude-group/v1/messages",
			wantURL:     "https://betterclau.de/claude/api.anthropic.com/v1/messages",
		},
		{
			name:        "gemini native",
			channelName: "gemini",
			groupName:   "gemini-group",
			baseURL:     "https://generativelanguage.googleapis.com",
			originalURL: "/proxy/gemini-group/v1beta/models/gemini-2.5-flash:generateContent",
			wantURL:     "https://betterclau.de/gemini/generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent",
		},
		{
			name:        "gemini native streaming",
			channelName: "gemini",
			groupName:   "gemini-group",
			baseURL:     "https://generativelanguage.googleapis.com",
			originalURL: "/proxy/gemini-group/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
			wantURL:     "https://betterclau.de/gemini/generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		},
		{
			name:        "preserves upstream path",
			channelName: "openai",
			groupName:   "custom-group",
			baseURL:     "https://api.example.com/custom/base",
			originalURL: "/proxy/custom-group/v1/messages",
			wantURL:     "https://betterclau.de/openai/api.example.com/custom/base/v1/messages",
		},
	}

	previous := GatewayProxyBaseURL("betterclaude")
	t.Cleanup(func() {
		restoreGatewayProxyBaseURL("betterclaude", previous)
	})
	SetGatewayProxyBaseURL("betterclaude", "https://betterclau.de")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BaseChannel{
				Name: tt.channelName,
				Upstreams: []UpstreamInfo{
					{
						URL:          mustParseURL(tt.baseURL),
						Weight:       100,
						GatewayProxy: "betterclaude",
					},
				},
			}

			selection, err := bc.SelectUpstreamWithClients(mustParseURL(tt.originalURL), tt.groupName)
			require.NoError(t, err)
			require.Equal(t, tt.wantURL, selection.URL)
			require.Equal(t, "betterclaude", selection.GatewayProxy)
		})
	}
}

func TestSelectUpstreamWithClientsUsesRuntimeGatewayProxyBaseURL(t *testing.T) {
	previous := GatewayProxyBaseURL("betterclaude")
	t.Cleanup(func() {
		restoreGatewayProxyBaseURL("betterclaude", previous)
	})
	SetGatewayProxyBaseURL("betterclaude", "https://cf.betterclau.de")

	bc := &BaseChannel{
		Name: "openai",
		Upstreams: []UpstreamInfo{
			{
				URL:          mustParseURL("https://api.openai.com"),
				Weight:       100,
				GatewayProxy: "betterclaude",
			},
		},
	}

	selection, err := bc.SelectUpstreamWithClients(mustParseURL("/proxy/openai-group/v1/chat/completions"), "openai-group")

	require.NoError(t, err)
	require.Equal(t, "https://cf.betterclau.de/openai/api.openai.com/v1/chat/completions", selection.URL)
	require.Equal(t, "betterclaude", selection.GatewayProxy)
}

func TestSelectUpstreamWithClientsFallsBackWhenGatewayProxyRuntimeBaseDisabled(t *testing.T) {
	previous := GatewayProxyBaseURL("betterclaude")
	t.Cleanup(func() {
		restoreGatewayProxyBaseURL("betterclaude", previous)
	})
	DisableGatewayProxyBaseURL("betterclaude")

	bc := &BaseChannel{
		Name: "openai",
		Upstreams: []UpstreamInfo{
			{
				URL:          mustParseURL("https://api.openai.com"),
				Weight:       100,
				GatewayProxy: "betterclaude",
			},
		},
	}

	selection, err := bc.SelectUpstreamWithClients(mustParseURL("/proxy/openai-group/v1/models"), "openai-group")

	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1/models", selection.URL)
	require.Empty(t, selection.GatewayProxy)
}

func TestSelectUpstreamWithClientsKeepsURLWithoutGatewayProxy(t *testing.T) {
	t.Parallel()

	bc := &BaseChannel{
		Name: "openai",
		Upstreams: []UpstreamInfo{
			{URL: mustParseURL("https://api.openai.com"), Weight: 100},
		},
	}

	selection, err := bc.SelectUpstreamWithClients(mustParseURL("/proxy/openai-group/v1/models?limit=10"), "openai-group")
	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1/models?limit=10", selection.URL)
	require.Empty(t, selection.GatewayProxy)
}

// TestSelectUpstreamConcurrency tests concurrent upstream selection
func TestSelectUpstreamConcurrency(t *testing.T) {
	upstreams := []UpstreamInfo{
		{URL: mustParseURL("https://api1.openai.com"), Weight: 100},
		{URL: mustParseURL("https://api2.openai.com"), Weight: 200},
		{URL: mustParseURL("https://api3.openai.com"), Weight: 300},
	}

	bc := &BaseChannel{
		Upstreams: upstreams,
	}

	// Run concurrent selections
	var wg sync.WaitGroup
	var errCount int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				result := bc.SelectUpstream()
				if result == nil {
					atomic.AddInt64(&errCount, 1)
				}
			}
		}()
	}

	// Wait for all goroutines to complete
	wg.Wait()
	if errCount > 0 {
		t.Errorf("SelectUpstream() returned nil %d times in concurrent test", errCount)
	}
}

func TestBaseChannelIsStreamRequestDefaultsMissingStreamFieldToNonStream(t *testing.T) {
	t.Parallel()

	bc := &BaseChannel{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	result := bc.IsStreamRequest(c, []byte(`{"model":"gpt-test","messages":[]}`))
	if result {
		t.Fatal("request without stream indicators should be treated as non-stream")
	}
}

package channel

import (
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
)

// mustParseURL is a test helper that parses a URL or panics
func mustParseURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u
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

package channel

import (
	"testing"
)

// Note: mustParseURL helper is defined in base_channel_test.go

func BenchmarkSelectUpstream(b *testing.B) {
	bc := &BaseChannel{
		Upstreams: []UpstreamInfo{
			{URL: mustParseURL("https://api.openai.com"), Weight: 100},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bc.SelectUpstream()
	}
}

package channel

import (
"net/url"
"testing"
)

func mustParseURL(rawURL string) *url.URL {
u, err := url.Parse(rawURL)
if err != nil {
panic(err)
}
return u
}

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

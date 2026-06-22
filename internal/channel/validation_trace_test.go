package channel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidationUpstreamAddrSanitizesGatewayProxy(t *testing.T) {
	t.Parallel()

	addr := validationUpstreamAddr(&UpstreamSelection{
		URL:          "https://upstream.example.com/v1",
		GatewayProxy: "http://user:raw-value@gateway.example.com:8080",
	})

	require.Contains(t, addr, "https://upstream.example.com/v1")
	require.Contains(t, addr, "gateway.example.com:8080")
	require.False(t, strings.Contains(addr, "user:raw-value"), addr)
}

package proxy

import (
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexRequestOptionCompatRejectsUnmodeledTopLevelFields(t *testing.T) {
	for _, field := range []string{
		`"background":true`,
		`"max_tool_calls":4`,
		`"prompt_cache_retention":"24h"`,
		`"future_protocol_field":{"enabled":true}`,
	} {
		t.Run(field, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello",` + field + `}`)
			c, _ := gin.CreateTestContext(nil)
			_, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: "openai"}, body)
			require.Error(t, err)
			assert.False(t, converted)
			assert.Contains(t, err.Error(), "unsupported_request_option")
			assert.Contains(t, err.Error(), "Not Supported")
		})
	}
}

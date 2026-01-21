package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestShouldCaptureResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("capture enabled", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		group := &models.Group{
			EffectiveConfig: types.SystemSettings{
				EnableRequestBodyLogging: true,
			},
		}
		c.Set("group", group)

		result := shouldCaptureResponse(c)
		assert.True(t, result)
	})

	t.Run("capture disabled", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		group := &models.Group{
			EffectiveConfig: types.SystemSettings{
				EnableRequestBodyLogging: false,
			},
		}
		c.Set("group", group)

		result := shouldCaptureResponse(c)
		assert.False(t, result)
	})

	t.Run("no group in context", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)

		result := shouldCaptureResponse(c)
		assert.False(t, result)
	})
}

func TestCollectCodexStreamToResponse(t *testing.T) {
	t.Run("simple text response", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_123","model":"gpt-4","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":" World"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_123","model":"gpt-4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello World"}]}]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "resp_123", result.ID)
		assert.Equal(t, "gpt-4", result.Model)
		assert.Equal(t, "completed", result.Status)
	})

	t.Run("function call response", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_456","model":"gpt-4"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"get_weather"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","delta":"{\"location\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","delta":"\"Tokyo\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_123","name":"get_weather","arguments":"{\"location\":\"Tokyo\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_456","model":"gpt-4","status":"completed","output":[{"type":"function_call","call_id":"call_123","name":"get_weather","arguments":"{\"location\":\"Tokyo\"}"}]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "resp_456", result.ID)
		assert.Len(t, result.Output, 1)
		assert.Equal(t, "function_call", result.Output[0].Type)
	})

	t.Run("stream without completion event", func(t *testing.T) {
		streamData := `event: response.created
data: {"type":"response.created","response":{"id":"resp_789","model":"gpt-4"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Incomplete"}
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		// Should build response from collected data
		assert.Equal(t, "resp_789", result.ID)
		assert.Equal(t, "completed", result.Status)
	})

	t.Run("invalid JSON in stream", func(t *testing.T) {
		streamData := `event: response.created
data: {invalid json}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Text"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_999","status":"completed","output":[]}}

data: [DONE]
`

		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(streamData)),
		}

		result, err := collectCodexStreamToResponse(resp)

		// Should handle parse errors gracefully
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("empty stream", func(t *testing.T) {
		resp := &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte{})),
		}

		result, err := collectCodexStreamToResponse(resp)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		// Should return a minimal response
		assert.Equal(t, "completed", result.Status)
	})
}

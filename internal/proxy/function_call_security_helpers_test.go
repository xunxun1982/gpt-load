package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gpt-load/internal/models"
)

func newFunctionCallSecurityContext(
	t *testing.T,
	channel, path string,
	toolChoice any,
	toolNames ...string,
) (*ProxyServer, *gin.Context, *httptest.ResponseRecorder, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	group := &models.Group{Name: "security-test", ChannelType: channel}
	c.Set("group", group)

	tools := make([]any, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": name,
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"n": map[string]any{"type": "integer"},
						"q": map[string]any{"type": "string"},
					},
				},
			},
		})
	}
	req := map[string]any{"tools": tools, "tool_choice": toolChoice}
	if channel == "openai-response" {
		req["input"] = "test"
	} else {
		req["messages"] = []any{map[string]any{"role": "user", "content": "test"}}
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	ps := &ProxyServer{}
	_, trigger, err := ps.applyFunctionCallRequestRewrite(c, group, body)
	if err != nil || trigger == "" {
		t.Fatalf("rewrite failed: trigger=%q err=%v", trigger, err)
	}
	c.Set(ctxKeyTriggerSignal, trigger)
	return ps, c, w, trigger
}

func TestFunctionCallRequestRewritePreservesLargeIntegers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	group := &models.Group{Name: "security-test", ChannelType: "openai"}
	c.Set("group", group)
	body := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"A","parameters":{"type":"object"}}}],"request_id":9007199254740993}`)

	rewritten, trigger, err := (&ProxyServer{}).applyFunctionCallRequestRewrite(c, group, body)
	if err != nil || trigger == "" {
		t.Fatalf("rewrite failed: trigger=%q err=%v", trigger, err)
	}
	if !bytes.Contains(rewritten, []byte(`"request_id":9007199254740993`)) {
		t.Fatalf("rewrite changed the large integer: %s", rewritten)
	}
}

func runFunctionCallSecurityResponse(
	t *testing.T,
	ps *ProxyServer,
	c *gin.Context,
	w *httptest.ResponseRecorder,
	channel string,
	body string,
) string {
	t.Helper()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
	switch channel {
	case "openai-response":
		ps.handleFunctionCallResponsesNormalResponse(c, resp)
	case "anthropic":
		ps.handleFunctionCallAnthropicNormalResponse(c, resp)
	default:
		ps.handleFunctionCallNormalResponse(c, resp)
	}
	return w.Body.String()
}

func setTestFunctionCallSecuritySession(c *gin.Context, trigger string, names ...string) {
	defs := make([]functionToolDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, functionToolDefinition{
			Name: name,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"n":     map[string]any{"type": "integer"},
					"q":     map[string]any{"type": "string"},
					"query": map[string]any{"type": "string"},
				},
			},
		})
	}
	setFunctionCallSession(c, newFunctionCallSession(trigger, defs, "auto", true))
}

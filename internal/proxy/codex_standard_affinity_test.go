package proxy

import (
	"encoding/json"
	"net/http"
	"testing"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/require"
)

func TestHandleProxyStandardCodexAffinityReusesExactKeyAndUpstream(t *testing.T) {
	handler, group, observations := setupStandardCodexAffinityGroup(t, true, nil)
	body := []byte(`{"model":"gpt-5","input":"hello","stream":false,"prompt_cache_key":"thread-1"}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "", body).Code)
	first := <-observations
	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "", body).Code)
	second := <-observations

	require.Equal(t, first.auth, second.auth)
	require.Equal(t, first.upstream, second.upstream)
}

func TestHandleProxyStandardCodexUnboundAffinityDelaysEncryptedReasoningUntilBound(t *testing.T) {
	handler, group, observations := setupStandardCodexAffinityGroup(t, true, nil)
	body := []byte(`{"model":"gpt-5","input":"hello","stream":false,"include":["reasoning.encrypted_content"]}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "", body).Code)
	first := <-observations
	require.NotContains(t, string(first.body), "reasoning.encrypted_content")

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "", body).Code)
	second := <-observations
	require.Contains(t, string(second.body), "reasoning.encrypted_content")
}

func TestHandleProxyStandardCodexIdentityChangeSanitizesBeforeSend(t *testing.T) {
	rules := []models.HeaderRule{{Key: "X-Codex-Turn-State", Value: "rule-readded", Action: "set"}}
	handler, group, observations := setupStandardCodexAffinityGroup(t, true, rules)
	body := []byte(`{
  "model":"gpt-5","previous_response_id":"resp_old","conversation":"conv_old",
  "include":["reasoning.encrypted_content"],
  "input":[{"type":"message","role":"user","content":"hello"},{"type":"reasoning","encrypted_content":"cipher"}]
}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "old-turn", body).Code)
	observation := <-observations

	require.Empty(t, observation.turn)
	require.NotContains(t, string(observation.body), "previous_response_id")
	require.NotContains(t, string(observation.body), `"type":"reasoning"`)
	require.NotContains(t, string(observation.body), "reasoning.encrypted_content")
}

func TestHandleProxyStandardCodexNewThreadInSameClientStartsCleanIdentityDomain(t *testing.T) {
	handler, group, observations := setupStandardCodexAffinityGroup(t, true, nil)
	firstBody := []byte(`{"model":"gpt-5","input":"first","stream":false}`)
	require.Equal(t, http.StatusOK, runCodexAffinityRequest(t, handler, group.Name, "proxy-a", "thread-1", "", firstBody).Code)
	<-observations

	newThreadBody := []byte(`{
  "model":"gpt-5","previous_response_id":"resp_old","include":["reasoning.encrypted_content"],
  "input":[{"type":"message","role":"user","content":"new"},{"type":"reasoning","encrypted_content":"cipher"}]
}`)
	require.Equal(t, http.StatusOK, runCodexAffinityRequest(t, handler, group.Name, "proxy-a", "thread-2", "old-turn", newThreadBody).Code)
	observation := <-observations

	require.Empty(t, observation.turn)
	require.NotContains(t, string(observation.body), "previous_response_id")
	require.NotContains(t, string(observation.body), `"type":"reasoning"`)
	require.NotContains(t, string(observation.body), "reasoning.encrypted_content")
}

func TestHandleProxyStandardCodexAffinityDisabledPreservesStateAndRotation(t *testing.T) {
	handler, group, observations := setupStandardCodexAffinityGroup(t, false, nil)
	body := []byte(`{"model":"gpt-5","previous_response_id":"resp_old","input":"hello","stream":false}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "old-turn", body).Code)
	first := <-observations
	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "old-turn", body).Code)
	second := <-observations

	require.NotEqual(t, first.auth, second.auth)
	require.Equal(t, "old-turn", first.turn)
	require.Contains(t, string(first.body), "previous_response_id")
}

func TestHandleProxyStandardCodexAffinityFailureStripsEncryptedStateBeforeRetry(t *testing.T) {
	handler, group, observations := setupRetryingStandardCodexAffinityGroup(t)
	body := []byte(`{
  "model":"gpt-5","include":["reasoning.encrypted_content","web_search_call.results"],
  "input":[{"type":"message","role":"user","content":"hello"},{"type":"reasoning","encrypted_content":"cipher"}]
}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "old-turn", body).Code)
	first := <-observations
	second := <-observations

	require.Empty(t, first.turn)
	require.NotContains(t, string(first.body), "reasoning.encrypted_content")
	require.NotContains(t, string(first.body), `"type":"reasoning"`)
	require.Empty(t, second.turn)
	require.NotContains(t, string(second.body), "reasoning.encrypted_content")
	require.Contains(t, string(second.body), "web_search_call.results")
	require.NotEqual(t, first.auth, second.auth)
}

func TestHandleProxyStandardCodexAffinityPreservesRedirectedModelAcrossRetry(t *testing.T) {
	handler, group, observations := setupRetryingStandardCodexAffinityGroup(t)
	body := []byte(`{"model":"gpt-source","input":"hello","stream":false}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "", body).Code)
	first := <-observations
	second := <-observations

	for _, observation := range []codexAffinityObservation{first, second} {
		var payload map[string]any
		require.NoError(t, json.Unmarshal(observation.body, &payload))
		require.Equal(t, "gpt-target", payload["model"])
	}
}

func TestHandleProxyStandardCodexAffinityRetriesLeadingEncryptedContentFailure(t *testing.T) {
	handler, group, requestCount, observations := setupStreamingRetryingStandardCodexAffinityGroup(t)
	body := []byte(`{
  "model":"gpt-5","stream":true,"include":["reasoning.encrypted_content"],
  "input":[{"type":"message","role":"user","content":"hello"},{"type":"reasoning","encrypted_content":"cipher"}]
}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "old-turn", body).Code)
	warmup := <-observations
	require.NotContains(t, string(warmup.body), "reasoning.encrypted_content")

	response := runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "old-turn", body)
	require.Equal(t, int32(3), requestCount.Load())
	first := <-observations
	second := <-observations

	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "response.failed")
	require.Contains(t, response.Body.String(), "response.completed")
	require.NotEqual(t, first.auth, second.auth)
	require.Contains(t, string(first.body), "reasoning.encrypted_content")
	require.NotContains(t, string(second.body), "reasoning.encrypted_content")
	require.NotContains(t, string(second.body), `"type":"reasoning"`)
}

func TestHandleProxyStandardCodexAffinitySeparatesProxyKeys(t *testing.T) {
	handler, group, observations := setupStandardCodexAffinityGroup(t, true, nil)
	body := []byte(`{"model":"gpt-5","input":"hello","stream":false}`)

	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "", body).Code)
	first := <-observations
	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-b", "", body).Code)
	second := <-observations
	require.Equal(t, http.StatusOK, runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "", body).Code)
	third := <-observations

	require.NotEqual(t, first.auth, second.auth)
	require.Equal(t, first.auth, third.auth)
	require.Equal(t, first.upstream, third.upstream)
}

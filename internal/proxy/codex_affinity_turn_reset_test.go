package proxy

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexAffinityDoesNotPersistMissingTurnStateReset(t *testing.T) {
	cache := newCodexAffinityCache(time.Minute, 2)
	now := time.Unix(100, 0)

	cache.markStateReset("cache-key", codexStateDomainWithoutTurnState, now)

	require.False(t, cache.requiresStateReset("cache-key", codexStateDomainWithoutTurnState, now))
}

func TestCodexAffinityStateResetDoesNotShortenTTLForOutOfOrderTimestamp(t *testing.T) {
	cache := newCodexAffinityCache(time.Hour, 2)
	now := time.Unix(150, 0)

	cache.markStateReset("cache-key", "turn:1", now.Add(30*time.Minute))
	cache.markStateReset("cache-key", "turn:1", now)

	require.True(t, cache.requiresStateReset("cache-key", "turn:1", now.Add(75*time.Minute)))
}

func TestStandardCodexAffinityKeepsStateResetForFailedTurn(t *testing.T) {
	handler, group, requestCount, observations := setupRetryingStandardCodexAffinityGroup(t)
	body := codexAffinityTurnResetBody("turn-1")

	recorder := runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "stale-turn", body)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int32(2), requestCount.Load())
	<-observations
	<-observations

	recorder = runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "stale-turn", body)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int32(3), requestCount.Load())
	observation := <-observations

	require.Empty(t, observation.turn)
	require.NotContains(t, string(observation.body), "reasoning.encrypted_content")
	require.NotContains(t, string(observation.body), `"type":"reasoning"`)
}

func TestStandardCodexAffinityRestoresEncryptedStateForNextTurn(t *testing.T) {
	handler, group, requestCount, observations := setupRetryingStandardCodexAffinityGroup(t)
	failedTurnBody := codexAffinityTurnResetBody("turn-1")

	recorder := runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "failed-turn", failedTurnBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int32(2), requestCount.Load())
	<-observations
	<-observations

	nextTurnBody := codexAffinityTurnResetBody("turn-2")
	recorder = runStandardCodexAffinityRequest(t, handler, group.Name, "proxy-a", "next-turn", nextTurnBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int32(3), requestCount.Load())
	observation := <-observations

	require.Equal(t, "next-turn", observation.turn)
	require.Contains(t, string(observation.body), "reasoning.encrypted_content")
	require.Contains(t, string(observation.body), `"type":"reasoning"`)
}

func codexAffinityTurnResetBody(turnID string) []byte {
	turnMetadata := fmt.Sprintf(`{"thread_id":"thread-1","turn_id":%q,"request_kind":"turn"}`, turnID)
	return []byte(fmt.Sprintf(`{
  "model":"gpt-5","include":["reasoning.encrypted_content"],
  "client_metadata":{"thread_id":"thread-1","turn_id":%q,"x-codex-turn-metadata":%q},
  "input":[{"type":"message","role":"user","content":"hello"},{"type":"reasoning","encrypted_content":"cipher"}]
}`, turnID, turnMetadata))
}

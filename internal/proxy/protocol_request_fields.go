package proxy

import (
	"encoding/json"
	"sort"
)

var codexKnownRequestFields = map[string]struct{}{
	"model": {}, "input": {}, "instructions": {}, "max_output_tokens": {},
	"temperature": {}, "top_p": {}, "stream": {}, "tools": {}, "tool_choice": {},
	"parallel_tool_calls": {}, "reasoning": {}, "stream_options": {}, "service_tier": {},
	"prompt_cache_key": {}, "text": {}, "client_metadata": {}, "store": {}, "include": {},
	// Official Responses API advisory fields without a lossy-free target
	// mapping. metadata, truncation and user are transport-only annotations,
	// so they are recognized and stripped instead of failing.
	//
	// previous_response_id is deliberately NOT recognized: the Responses API
	// uses it to reference server-side conversation state ("manage prior
	// response context" per OpenAI docs), so silently dropping it during
	// conversion would make the target answer as a fresh conversation. The
	// strict field validator therefore rejects it (fail-closed).
	"metadata": {}, "truncation": {}, "user": {},
}

var claudeKnownRequestFields = map[string]struct{}{
	"model": {}, "prompt": {}, "system": {}, "messages": {}, "max_tokens": {},
	"max_tokens_to_sample": {}, "temperature": {}, "top_k": {}, "top_p": {}, "stream": {},
	"tools": {}, "stop_sequences": {}, "tool_choice": {}, "mcp_servers": {}, "metadata": {},
	"container": {}, "thinking": {}, "output_config": {}, "service_tier": {},
	// Official Anthropic Messages API (incl. beta) fields with no Chat
	// Completions equivalent. Claude Code sends context_management (server-side
	// compaction) on every request; these are recognized and stripped rather
	// than rejecting the conversion.
	//
	// compaction_control is deliberately NOT recognized: per Anthropic docs and
	// SDK sources it is a deprecated client-side tool_runner option (stripped
	// from the request before it is sent), not a top-level Messages API field.
	// A request carrying it therefore fails closed instead of silently dropping
	// an invalid field.
	"context_management": {}, "cache_control": {}, "diagnostics": {},
}

func captureUnsupportedJSONFields(data []byte, known map[string]struct{}) ([]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	unsupported := make([]string, 0)
	for field := range fields {
		if _, ok := known[field]; !ok {
			unsupported = append(unsupported, field)
		}
	}
	sort.Strings(unsupported)
	return unsupported, nil
}

func (r *CodexRequest) UnmarshalJSON(data []byte) error {
	type plain CodexRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	unsupported, err := captureUnsupportedJSONFields(data, codexKnownRequestFields)
	if err != nil {
		return err
	}
	*r = CodexRequest(decoded)
	r.unsupportedFields = unsupported
	return nil
}

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
}

var claudeKnownRequestFields = map[string]struct{}{
	"model": {}, "prompt": {}, "system": {}, "messages": {}, "max_tokens": {},
	"max_tokens_to_sample": {}, "temperature": {}, "top_k": {}, "top_p": {}, "stream": {},
	"tools": {}, "stop_sequences": {}, "tool_choice": {}, "mcp_servers": {}, "metadata": {},
	"container": {}, "thinking": {}, "output_config": {}, "service_tier": {},
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

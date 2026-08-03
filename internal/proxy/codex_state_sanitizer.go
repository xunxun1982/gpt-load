package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func sanitizeCodexStateDomain(body []byte, supportsEncryptedReasoning bool) ([]byte, bool, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false, err
	}
	if payload == nil {
		return body, false, fmt.Errorf("responses request must be a JSON object")
	}

	changed := false
	for _, key := range []string{"previous_response_id", "conversation"} {
		if _, ok := payload[key]; ok {
			delete(payload, key)
			changed = true
		}
	}
	if rawMetadata, ok := payload["client_metadata"]; ok {
		var metadata map[string]json.RawMessage
		if json.Unmarshal(rawMetadata, &metadata) == nil && metadata != nil {
			removedTurnState := false
			for key := range metadata {
				if strings.EqualFold(key, "x-codex-turn-state") {
					delete(metadata, key)
					removedTurnState = true
				}
			}
			if removedTurnState {
				encoded, err := json.Marshal(metadata)
				if err != nil {
					return body, false, err
				}
				payload["client_metadata"] = encoded
				changed = true
			}
		}
	}
	if rawInput, ok := payload["input"]; ok {
		sanitized, inputChanged, err := sanitizeCodexInput(rawInput)
		if err != nil {
			return body, false, err
		}
		if inputChanged {
			payload["input"] = sanitized
			changed = true
		}
	}
	if !supportsEncryptedReasoning {
		if includeChanged, err := stripCodexEncryptedReasoningInclude(payload); err != nil {
			return body, false, err
		} else if includeChanged {
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return body, false, err
	}
	return result, true, nil
}

func stripCodexEncryptedReasoningInclude(payload map[string]json.RawMessage) (bool, error) {
	raw, ok := payload["include"]
	if !ok {
		return false, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return false, nil
	}
	filtered := make([]json.RawMessage, 0, len(values))
	removed := false
	for _, value := range values {
		var text string
		if json.Unmarshal(value, &text) == nil && text == responsesEncryptedReasoning {
			removed = true
			continue
		}
		filtered = append(filtered, value)
	}
	if !removed {
		return false, nil
	}
	if len(filtered) == 0 {
		delete(payload, "include")
		return true, nil
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return false, err
	}
	payload["include"] = encoded
	return true, nil
}

func sanitizeCodexInput(raw json.RawMessage) (json.RawMessage, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, false, nil
	}
	if trimmed[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return raw, false, err
		}
		return sanitizeCodexInputItems(items)
	}
	if trimmed[0] != '{' {
		return raw, false, nil
	}
	result, drop, _, changed, err := sanitizeCodexInputItem(raw)
	if err != nil {
		return raw, false, err
	}
	if drop {
		return json.RawMessage("[]"), true, nil
	}
	return result, changed, nil
}

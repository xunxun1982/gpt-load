package proxy

import (
	"encoding/json"
	"strings"
)

type codexItemMeta struct {
	typeName string
	callID   string
}

func sanitizeCodexInputItems(items []json.RawMessage) (json.RawMessage, bool, error) {
	results := make([]json.RawMessage, 0, len(items))
	metas := make([]codexItemMeta, 0, len(items))
	droppedCalls := make(map[string]struct{})
	changed := false
	for _, item := range items {
		result, drop, pairKey, itemChanged, err := sanitizeCodexInputItem(item)
		if err != nil {
			return nil, false, err
		}
		if pairKey != "" {
			droppedCalls[pairKey] = struct{}{}
		}
		if drop {
			changed = true
			continue
		}
		results = append(results, result)
		metas = append(metas, codexItemMetadata(result))
		changed = changed || itemChanged
	}
	if len(droppedCalls) > 0 {
		filtered := results[:0]
		for i, item := range results {
			if _, drop := droppedCalls[codexCallPairKey(metas[i])]; drop {
				changed = true
				continue
			}
			filtered = append(filtered, item)
		}
		results = filtered
	}
	if !changed {
		return nil, false, nil
	}
	encoded, err := json.Marshal(results)
	return encoded, true, err
}

func sanitizeCodexInputItem(raw json.RawMessage) (json.RawMessage, bool, string, bool, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return raw, false, "", false, nil
	}
	meta := codexItemMetadataFromMap(item)
	switch meta.typeName {
	case "reasoning", "item_reference", "compaction", "compaction_summary", "context_compaction":
		return nil, true, "", true, nil
	case "message", "agent_message":
		if codexContentHasEncryptedBlock(item) {
			return nil, true, "", true, nil
		}
	case "encrypted_content":
		return nil, true, "", true, nil
	case "function_call":
		if _, ok := item["encrypted_function_args"]; ok {
			delete(item, "encrypted_function_args")
			encoded, err := json.Marshal(item)
			return encoded, false, "", true, err
		}
	}
	if codexOutputPairKey(meta) != "" {
		return sanitizeCodexToolOutput(raw, item, meta)
	}
	if _, hasEncryptedContent := item["encrypted_content"]; hasEncryptedContent {
		return nil, true, "", true, nil
	}
	return raw, false, "", false, nil
}

func sanitizeCodexToolOutput(raw json.RawMessage, item map[string]json.RawMessage, meta codexItemMeta) (json.RawMessage, bool, string, bool, error) {
	var blocks []json.RawMessage
	if json.Unmarshal(item["output"], &blocks) != nil {
		return raw, false, "", false, nil
	}
	filtered := make([]json.RawMessage, 0, len(blocks))
	removed := false
	for _, block := range blocks {
		if codexItemMetadata(block).typeName == "encrypted_content" {
			removed = true
			continue
		}
		filtered = append(filtered, block)
	}
	if !removed {
		return raw, false, "", false, nil
	}
	if len(filtered) == 0 {
		return nil, true, codexOutputPairKey(meta), true, nil
	}
	encodedOutput, err := json.Marshal(filtered)
	if err != nil {
		return raw, false, "", false, err
	}
	item["output"] = encodedOutput
	encoded, err := json.Marshal(item)
	return encoded, false, "", true, err
}

func codexContentHasEncryptedBlock(item map[string]json.RawMessage) bool {
	var blocks []json.RawMessage
	if json.Unmarshal(item["content"], &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if codexItemMetadata(block).typeName == "encrypted_content" {
			return true
		}
	}
	return false
}

func codexItemMetadata(raw json.RawMessage) codexItemMeta {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil {
		return codexItemMeta{}
	}
	return codexItemMetadataFromMap(item)
}

func codexItemMetadataFromMap(item map[string]json.RawMessage) codexItemMeta {
	var meta codexItemMeta
	_ = json.Unmarshal(item["type"], &meta.typeName)
	_ = json.Unmarshal(item["call_id"], &meta.callID)
	return meta
}

func codexCallPairKey(meta codexItemMeta) string {
	if meta.callID == "" || !strings.HasSuffix(meta.typeName, "_call") {
		return ""
	}
	return meta.typeName + "\x00" + meta.callID
}

func codexOutputPairKey(meta codexItemMeta) string {
	if meta.callID == "" || !strings.HasSuffix(meta.typeName, "_output") {
		return ""
	}
	callType := strings.TrimSuffix(meta.typeName, "_output")
	if !strings.HasSuffix(callType, "_call") {
		callType += "_call"
	}
	return callType + "\x00" + meta.callID
}

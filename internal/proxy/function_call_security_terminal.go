package proxy

import "strings"

func chatFunctionCallComplete(choice map[string]any) bool {
	finishReason, ok := choice["finish_reason"].(string)
	return ok && strings.EqualFold(strings.TrimSpace(finishReason), "stop")
}

func responsesFunctionCallComplete(payload map[string]any) bool {
	status, ok := payload["status"].(string)
	return ok && strings.EqualFold(strings.TrimSpace(status), "completed") && payload["error"] == nil
}

func anthropicFunctionCallComplete(payload map[string]any) bool {
	payloadType, ok := payload["type"].(string)
	if !ok || !strings.EqualFold(strings.TrimSpace(payloadType), "message") || payload["error"] != nil {
		return false
	}
	stopReason, ok := payload["stop_reason"].(string)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "end_turn", "stop_sequence":
		return true
	default:
		return false
	}
}

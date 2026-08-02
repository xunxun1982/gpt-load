package proxy

import "strings"

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

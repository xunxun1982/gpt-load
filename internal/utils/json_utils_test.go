package utils

import (
	"encoding/json"
	"testing"
)

func TestUnmarshalJSONUseNumber(t *testing.T) {
	t.Parallel()

	var payload map[string]any
	if err := UnmarshalJSONUseNumber([]byte(`{"id":9007199254740993}`), &payload); err != nil {
		t.Fatalf("UnmarshalJSONUseNumber() error = %v", err)
	}
	number, ok := payload["id"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("UnmarshalJSONUseNumber() id = %#v", payload["id"])
	}

	if err := UnmarshalJSONUseNumber([]byte(`{"id":1} {"id":2}`), &payload); err == nil {
		t.Fatal("UnmarshalJSONUseNumber() accepted multiple JSON values")
	}
}

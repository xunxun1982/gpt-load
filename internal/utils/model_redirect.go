package utils

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MigrateModelMappingToRedirectRules migrates from old ModelMapping format to new ModelRedirectRules format
// Old format: "gpt-3.5-turbo:gpt-3.5-turbo-0301 gpt-4:gpt-4-0314"
// New format: {"gpt-3.5-turbo": "gpt-3.5-turbo-0301", "gpt-4": "gpt-4-0314"}
func MigrateModelMappingToRedirectRules(modelMapping string) (map[string]string, error) {
	if modelMapping == "" {
		return nil, nil
	}

	// Try to parse as JSON first (in case it's already in new format)
	var jsonMap map[string]string
	if err := json.Unmarshal([]byte(modelMapping), &jsonMap); err == nil {
		return jsonMap, nil
	}

	// Parse old format: space-separated key:value pairs
	result := make(map[string]string)
	pairs := strings.Fields(modelMapping)
	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid model mapping format: %s", pair)
		}
		if parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("empty key or value in model mapping: %s", pair)
		}
		result[parts[0]] = parts[1]
	}

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

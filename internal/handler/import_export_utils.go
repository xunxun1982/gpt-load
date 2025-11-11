package handler

import (
	"encoding/json"

	"gpt-load/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

// ConvertModelRedirectRulesToExport converts datatypes.JSONMap to map[string]string for export
// This ensures the ModelRedirectRules field is properly serialized in export files
func ConvertModelRedirectRulesToExport(redirectRules datatypes.JSONMap) map[string]string {
	if len(redirectRules) == 0 {
		return nil
	}

	result := make(map[string]string, len(redirectRules))
	for k, v := range redirectRules {
		if strVal, ok := v.(string); ok {
			result[k] = strVal
		}
	}
	return result
}

// ConvertModelRedirectRulesToImport converts map[string]string to datatypes.JSONMap for import
// This ensures the ModelRedirectRules field is properly stored in the database
func ConvertModelRedirectRulesToImport(redirectRules map[string]string) datatypes.JSONMap {
	if len(redirectRules) == 0 {
		return nil
	}

	result := make(map[string]any, len(redirectRules))
	for k, v := range redirectRules {
		result[k] = v
	}
	return datatypes.JSONMap(result)
}

// ParsePathRedirectsForExport parses PathRedirects JSON to array for export
// Returns nil if parsing fails or input is empty
func ParsePathRedirectsForExport(pathRedirectsJSON []byte) []models.PathRedirectRule {
	if len(pathRedirectsJSON) == 0 {
		return nil
	}

	var pathRedirects []models.PathRedirectRule
	if err := json.Unmarshal(pathRedirectsJSON, &pathRedirects); err != nil {
		logrus.WithError(err).Warn("Failed to parse PathRedirects for export")
		return nil
	}
	return pathRedirects
}

// ConvertPathRedirectsToJSON converts PathRedirects array to JSON for import
// Returns nil if input is empty or marshaling fails
func ConvertPathRedirectsToJSON(pathRedirects []models.PathRedirectRule) []byte {
	if len(pathRedirects) == 0 {
		return nil
	}

	pathRedirectsJSON, err := json.Marshal(pathRedirects)
	if err != nil {
		logrus.WithError(err).Warn("Failed to marshal PathRedirects")
		return nil
	}
	return pathRedirectsJSON
}

// ParseHeaderRulesForExport parses HeaderRules JSON to array for export
// Returns empty array if parsing fails to prevent export failures
func ParseHeaderRulesForExport(headerRulesJSON []byte, groupID uint) []models.HeaderRule {
	if len(headerRulesJSON) == 0 {
		return []models.HeaderRule{}
	}

	var headerRules []models.HeaderRule
	if err := json.Unmarshal(headerRulesJSON, &headerRules); err != nil {
		logrus.WithError(err).WithField("group_id", groupID).Warn("Failed to parse HeaderRules for export")
		return []models.HeaderRule{}
	}
	return headerRules
}

// ConvertHeaderRulesToJSON converts HeaderRules array to JSON for import
// Returns empty JSON array if marshaling fails to maintain consistency
func ConvertHeaderRulesToJSON(headerRules []models.HeaderRule) []byte {
	headerRulesJSON, err := json.Marshal(headerRules)
	if err != nil {
		logrus.WithError(err).Warn("Failed to marshal HeaderRules")
		return []byte("[]")
	}
	return headerRulesJSON
}

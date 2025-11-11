package handler

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
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

// GetExportMode returns "plain" or "encrypted" based on query params. Default is "encrypted".
// Query keys supported: mode, export_mode
func GetExportMode(c *gin.Context) string {
	mode := strings.ToLower(strings.TrimSpace(c.DefaultQuery("mode", "")))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(c.DefaultQuery("export_mode", "")))
	}
	if mode != "plain" && mode != "encrypted" {
		mode = "encrypted"
	}
	return mode
}

// GetImportMode returns "plain" or "encrypted" based on query params, filename, or content heuristic.
// Query keys supported: mode, import_mode, filename
// sampleKeys can be a small subset of key values for heuristic detection.
func GetImportMode(c *gin.Context, sampleKeys []string) string {
	mode := strings.ToLower(strings.TrimSpace(c.DefaultQuery("mode", "")))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(c.DefaultQuery("import_mode", "")))
	}
	if mode == "plain" || mode == "encrypted" {
		return mode
	}

	filename := strings.ToLower(strings.TrimSpace(c.DefaultQuery("filename", "")))
	if filename != "" {
		if strings.Contains(filename, "-plain") || strings.Contains(filename, ".plain.") || strings.HasSuffix(filename, ".plain") {
			return "plain"
		}
		if strings.Contains(filename, "-enc") || strings.Contains(filename, ".enc.") || strings.HasSuffix(filename, ".enc.json") || strings.HasSuffix(filename, ".enc") {
			return "encrypted"
		}
	}

	// Heuristic: if most sample keys look like hex data, treat as encrypted; otherwise plain
	hexLike := 0
	limit := 0
	for _, k := range sampleKeys {
		if k == "" {
			continue
		}
		limit++
		if looksLikeHex(k) {
			hexLike++
		}
		if limit >= 5 { // only check first few keys
			break
		}
	}
	if limit > 0 && hexLike*2 >= limit { // majority hex-like
		return "encrypted"
	}
	return "plain"
}

// looksLikeHex checks if a string appears to be hex-encoded (even length and valid hex chars)
func looksLikeHex(s string) bool {
	if len(s) < 16 || len(s)%2 != 0 { // quick rejects
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

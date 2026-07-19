package handler

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

var (
	errPlainExportKeyDecrypt            = errors.New("failed to decrypt key for plain export")
	errEncryptedExportProxySanitization = errors.New("failed to sanitize proxy configuration for encrypted export")
)

func exportKeyValue(value, exportMode string, decrypt func(string) (string, error)) (string, error) {
	if exportMode != "plain" {
		return value, nil
	}
	if decrypt == nil {
		return "", errPlainExportKeyDecrypt
	}
	decrypted, err := decrypt(value)
	if err != nil {
		// Never include the key value, ciphertext, or provider error in logs or returned errors.
		return "", errPlainExportKeyDecrypt
	}
	return decrypted, nil
}

func sanitizeSystemSettingsForExport(settings map[string]string, plainMode bool) map[string]string {
	if plainMode || settings == nil {
		return settings
	}
	containsProxyURL := false
	for key := range settings {
		if isProxyURLKey(key) {
			containsProxyURL = true
			break
		}
	}
	if !containsProxyURL {
		return settings
	}
	sanitized := maps.Clone(settings)
	for key := range sanitized {
		if isProxyURLKey(key) {
			sanitized[key] = ""
		}
	}
	return sanitized
}

func sanitizeGroupProxyFieldsForExport(group *GroupExportInfo, plainMode bool) error {
	if group == nil || plainMode {
		return nil
	}
	group.Config = sanitizeGroupConfigForExport(group.Config, false)
	if len(group.Upstreams) == 0 {
		return nil
	}
	var upstreams []map[string]json.RawMessage
	if err := json.Unmarshal(group.Upstreams, &upstreams); err != nil {
		// Fail closed instead of returning malformed JSON that may still contain credentials.
		return errEncryptedExportProxySanitization
	}
	changed := false
	for i := range upstreams {
		for key := range upstreams[i] {
			if isProxyURLKey(key) {
				upstreams[i][key] = json.RawMessage(`""`)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(upstreams)
	if err != nil {
		return errEncryptedExportProxySanitization
	}
	group.Upstreams = encoded
	return nil
}

func sanitizeGroupConfigForExport(config map[string]any, plainMode bool) map[string]any {
	if plainMode || config == nil {
		return config
	}
	containsProxyURL := false
	for key := range config {
		if isProxyURLKey(key) {
			containsProxyURL = true
			break
		}
	}
	if !containsProxyURL {
		return config
	}
	sanitized := maps.Clone(config)
	for key := range sanitized {
		if isProxyURLKey(key) {
			sanitized[key] = ""
		}
	}
	return sanitized
}

func isProxyURLKey(key string) bool {
	return strings.EqualFold(key, "proxy_url")
}

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

// ConvertChildGroupsForExport converts service child groups to the handler JSON format.
func ConvertChildGroupsForExport(childGroups []services.ChildGroupExport, exportMode string, decrypt func(string) (string, error)) ([]ChildGroupExportInfo, error) {
	if len(childGroups) == 0 {
		return nil, nil
	}

	result := make([]ChildGroupExportInfo, 0, len(childGroups))
	for _, cg := range childGroups {
		childExport := ChildGroupExportInfo{
			Name:                 cg.Name,
			DisplayName:          cg.DisplayName,
			Description:          cg.Description,
			Enabled:              cg.Enabled,
			ProxyKeys:            cg.ProxyKeys,
			Sort:                 cg.Sort,
			TestModel:            cg.TestModel,
			ParamOverrides:       rawJSONMapForExport(cg.ParamOverrides, cg.Name, "ParamOverrides"),
			Config:               sanitizeGroupConfigForExport(rawJSONMapForExport(cg.Config, cg.Name, "Config"), exportMode == "plain"),
			HeaderRules:          ParseHeaderRulesForExport(cg.HeaderRules, 0),
			ModelMapping:         cg.ModelMapping,
			ModelRedirectRules:   rawModelRedirectRulesForExport(cg.ModelRedirectRules, cg.Name),
			ModelRedirectRulesV2: mergedRawJSONForExport(cg.ModelRedirectRulesV2, cg.Name, "model redirect rules V2"),
			ModelRedirectStrict:  cg.ModelRedirectStrict,
			CustomModelNames:     rawJSONForExport(cg.CustomModelNames),
			Preconditions:        rawJSONMapForExport(cg.Preconditions, cg.Name, "Preconditions"),
			PathRedirects:        ParsePathRedirectsForExport(cg.PathRedirects),
			Keys:                 make([]KeyExportInfo, 0, len(cg.Keys)),
		}
		for _, key := range cg.Keys {
			kv, err := exportKeyValue(key.KeyValue, exportMode, decrypt)
			if err != nil {
				return nil, err
			}
			childExport.Keys = append(childExport.Keys, KeyExportInfo{
				KeyValue: kv,
				Status:   key.Status,
			})
		}

		result = append(result, childExport)
	}

	return result, nil
}

// ConvertChildGroupsForImport converts handler child groups to the service import format.
func ConvertChildGroupsForImport(childGroups []ChildGroupExportInfo, inputIsPlain bool, encrypt func(string) (string, error)) []services.ChildGroupExport {
	if len(childGroups) == 0 {
		return nil
	}

	result := make([]services.ChildGroupExport, 0, len(childGroups))
	for _, cg := range childGroups {
		childExport := services.ChildGroupExport{
			Name:                cg.Name,
			DisplayName:         cg.DisplayName,
			Description:         cg.Description,
			Enabled:             cg.Enabled,
			ProxyKeys:           cg.ProxyKeys,
			Sort:                cg.Sort,
			TestModel:           cg.TestModel,
			ParamOverrides:      jsonMapForImport(cg.ParamOverrides, cg.Name, "ParamOverrides"),
			Config:              jsonMapForImport(cg.Config, cg.Name, "Config"),
			HeaderRules:         jsonSliceForImport(cg.HeaderRules, cg.Name, "HeaderRules"),
			ModelMapping:        cg.ModelMapping,
			ModelRedirectRules:  jsonMapForImport(cg.ModelRedirectRules, cg.Name, "ModelRedirectRules"),
			ModelRedirectStrict: cg.ModelRedirectStrict,
			CustomModelNames:    rawJSONForExport(cg.CustomModelNames),
			Preconditions:       jsonMapForImport(cg.Preconditions, cg.Name, "Preconditions"),
			PathRedirects:       jsonSliceForImport(cg.PathRedirects, cg.Name, "PathRedirects"),
			Keys:                make([]services.KeyExportInfo, 0, len(cg.Keys)),
		}
		if len(cg.ModelRedirectRulesV2) > 0 {
			childExport.ModelRedirectRulesV2 = json.RawMessage(cg.ModelRedirectRulesV2)
		}

		for _, key := range cg.Keys {
			kv := key.KeyValue
			if inputIsPlain && encrypt != nil {
				if encrypted, err := encrypt(kv); err == nil {
					kv = encrypted
				} else {
					logrus.WithError(err).WithField("child_group", cg.Name).Warn("Failed to encrypt plaintext key during child group import, skipping")
					continue
				}
			}
			childExport.Keys = append(childExport.Keys, services.KeyExportInfo{
				KeyValue: kv,
				Status:   key.Status,
			})
		}

		result = append(result, childExport)
	}

	return result
}

func CountGroupExportKeys(groups []GroupExportData) int {
	total := 0
	for _, group := range groups {
		total += len(group.Keys)
		for _, child := range group.ChildGroups {
			total += len(child.Keys)
		}
	}
	return total
}

func countServiceGroupExportKeys(group services.GroupExportData) int {
	total := len(group.Keys)
	for _, child := range group.ChildGroups {
		total += len(child.Keys)
	}
	return total
}

const importModeSampleLimit = 5

// AES-GCM ciphertext contains a 12-byte nonce and 16-byte tag before any plaintext bytes.
const minimumEncryptedValueHexLength = (12 + 16) * 2

func appendImportModeSample(sample []string, value string) []string {
	if value == "" || len(sample) >= importModeSampleLimit {
		return sample
	}
	return append(sample, value)
}

func appendManagedSiteImportSamples(sample []string, userID, authType, authValue string) []string {
	sample = appendImportModeSample(sample, userID)
	if len(sample) >= importModeSampleLimit {
		return sample
	}
	authType = services.NormalizeManagedSiteAuthType(authType)
	if services.ManagedSiteAuthTypeRequiresCredential(authType) {
		sample = appendImportModeSample(sample, authValue)
	}
	return sample
}

func CollectGroupImportSampleKeys(groups []GroupExportData) []string {
	sample := make([]string, 0, importModeSampleLimit)
	for _, group := range groups {
		for _, key := range group.Keys {
			sample = appendImportModeSample(sample, key.KeyValue)
			if len(sample) >= importModeSampleLimit {
				return sample
			}
		}
		for _, child := range group.ChildGroups {
			for _, key := range child.Keys {
				sample = appendImportModeSample(sample, key.KeyValue)
				if len(sample) >= importModeSampleLimit {
					return sample
				}
			}
		}
	}
	return sample
}

func ConvertGroupForImport(groupExport GroupExportData, inputIsPlain bool, encrypt func(string) (string, error)) services.GroupExportData {
	headerRulesJSON := ConvertHeaderRulesToJSON(groupExport.Group.HeaderRules)
	pathRedirectsJSON := ConvertPathRedirectsToJSON(groupExport.Group.PathRedirects)
	modelRedirectRules := ConvertModelRedirectRulesToImport(groupExport.Group.ModelRedirectRules)

	var modelRedirectRulesV2 []byte
	if len(groupExport.Group.ModelRedirectRulesV2) > 0 {
		rawJSON := []byte(groupExport.Group.ModelRedirectRulesV2)
		merged, err := utils.MergeModelRedirectRulesV2(rawJSON)
		if err != nil {
			logrus.WithError(err).Warn("Failed to merge model redirect rules V2 during import, using original")
			modelRedirectRulesV2 = rawJSON
		} else {
			modelRedirectRulesV2 = merged
		}
	}

	groupData := services.GroupExportData{
		Group: models.Group{
			Name:                 groupExport.Group.Name,
			DisplayName:          groupExport.Group.DisplayName,
			Description:          groupExport.Group.Description,
			GroupType:            groupExport.Group.GroupType,
			ChannelType:          groupExport.Group.ChannelType,
			Enabled:              groupExport.Group.Enabled,
			TestModel:            groupExport.Group.TestModel,
			ValidationEndpoint:   groupExport.Group.ValidationEndpoint,
			Upstreams:            []byte(groupExport.Group.Upstreams),
			ParamOverrides:       groupExport.Group.ParamOverrides,
			Config:               groupExport.Group.Config,
			HeaderRules:          headerRulesJSON,
			ModelMapping:         groupExport.Group.ModelMapping,
			ModelRedirectRules:   modelRedirectRules,
			ModelRedirectRulesV2: modelRedirectRulesV2,
			ModelRedirectStrict:  groupExport.Group.ModelRedirectStrict,
			PathRedirects:        pathRedirectsJSON,
			ProxyKeys:            groupExport.Group.ProxyKeys,
			Sort:                 groupExport.Group.Sort,
		},
		Keys:      make([]services.KeyExportInfo, 0, len(groupExport.Keys)),
		SubGroups: make([]services.SubGroupInfo, 0, len(groupExport.SubGroups)),
	}

	for _, key := range groupExport.Keys {
		kv := key.KeyValue
		if inputIsPlain && encrypt != nil {
			encrypted, err := encrypt(kv)
			if err != nil {
				logrus.WithError(err).WithField("group", groupExport.Group.Name).Warn("Failed to encrypt plaintext key during group import, skipping")
				continue
			}
			kv = encrypted
		}
		groupData.Keys = append(groupData.Keys, services.KeyExportInfo{
			KeyValue: kv,
			Status:   key.Status,
		})
	}

	for _, sg := range groupExport.SubGroups {
		groupData.SubGroups = append(groupData.SubGroups, services.SubGroupInfo{
			GroupName:          sg.GroupName,
			Weight:             sg.Weight,
			MinEffectiveWeight: sg.MinEffectiveWeight,
		})
	}

	if childGroups := ConvertChildGroupsForImport(groupExport.ChildGroups, inputIsPlain, encrypt); len(childGroups) > 0 {
		groupData.ChildGroups = childGroups
	}

	return groupData
}

func rawJSONMapForExport(raw json.RawMessage, groupName string, fieldName string) map[string]any {
	if len(raw) == 0 {
		return nil
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		logrus.WithError(err).WithField("child_group", groupName).Warnf("Failed to parse child group %s for export", fieldName)
		return nil
	}
	return result
}

func rawModelRedirectRulesForExport(raw json.RawMessage, groupName string) map[string]string {
	if len(raw) == 0 {
		return nil
	}

	var tempMap map[string]any
	if err := json.Unmarshal(raw, &tempMap); err != nil {
		logrus.WithError(err).WithField("child_group", groupName).Warn("Failed to parse child group ModelRedirectRules for export")
		return nil
	}

	result := make(map[string]string, len(tempMap))
	for k, v := range tempMap {
		if strVal, ok := v.(string); ok {
			result[k] = strVal
		}
	}
	return result
}

func mergedRawJSONForExport(raw json.RawMessage, groupName string, fieldName string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}

	merged, err := utils.MergeModelRedirectRulesV2(raw)
	if err != nil {
		logrus.WithError(err).WithField("child_group", groupName).Warnf("Failed to merge child group %s, using original", fieldName)
		return raw
	}
	return merged
}

func rawJSONForExport(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func jsonMapForImport[T any](value map[string]T, groupName string, fieldName string) json.RawMessage {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		logrus.WithError(err).WithField("child_group", groupName).Warnf("Failed to marshal child group %s for import", fieldName)
		return nil
	}
	return data
}

func jsonSliceForImport[T any](value []T, groupName string, fieldName string) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		logrus.WithError(err).WithField("child_group", groupName).Warnf("Failed to marshal child group %s for import", fieldName)
		return nil
	}
	return data
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
		if limit >= importModeSampleLimit { // only check first few keys
			break
		}
	}
	if limit > 0 && hexLike*2 > limit { // strict majority avoids treating a 1:1 tie as encrypted
		return "encrypted"
	}
	// With no sensitive samples, mode cannot transform any credential; preserve the legacy default.
	return "plain"
}

// looksLikeHex checks if a string appears to be hex-encoded (even length and valid hex chars)
func looksLikeHex(s string) bool {
	// Short hex-looking plaintext (for example, numeric user IDs) must remain plain.
	if len(s) < minimumEncryptedValueHexLength || len(s)%2 != 0 { // quick rejects
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

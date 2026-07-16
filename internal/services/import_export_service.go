package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sentinel errors for import/export operations
var (
	// ErrChildGroupCannotExportIndividually is returned when attempting to export a child group individually
	ErrChildGroupCannotExportIndividually = errors.New("child groups cannot be exported individually, please export the parent group instead")
	// ErrChildGroupCannotImportIndividually is returned when attempting to import a child group individually
	ErrChildGroupCannotImportIndividually = errors.New("child groups cannot be imported individually, they are automatically imported with their parent group")
)

// ImportExportService provides unified import/export functionality
// This service handles both group-level and system-level import/export operations
// It solves the FindInBatches limitation and reduces code duplication
type ImportExportService struct {
	db                *gorm.DB
	bulkImportService *BulkImportService
	encryptionService encryption.Service
}

type importGroupOptions struct {
	ImportAggregateSubGroups bool
}

// NewImportExportService creates a new import/export service
func NewImportExportService(db *gorm.DB, bulkImport *BulkImportService, encryptionSvc encryption.Service) *ImportExportService {
	return &ImportExportService{
		db:                db,
		bulkImportService: bulkImport,
		encryptionService: encryptionSvc,
	}
}

// GenerateUniqueGroupName generates a unique group name by appending a random suffix if needed
// This is the centralized function for all group name conflict resolution
// It appends a random 4-character suffix (e.g., "api-keys" -> "api-keysx9k2")
func (s *ImportExportService) GenerateUniqueGroupName(tx *gorm.DB, baseName string) (string, error) {
	groupName := baseName
	maxAttempts := 10

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Check if this name already exists
		var count int64
		if err := tx.Model(&models.Group{}).Where("name = ?", groupName).Count(&count).Error; err != nil {
			return "", fmt.Errorf("failed to check group name: %w", err)
		}

		// If name is unique, we're done
		if count == 0 {
			if groupName != baseName {
				logrus.Debugf("Generated unique group name: %s (original: %s)", groupName, baseName)
			}
			return groupName, nil
		}

		// Generate a new name with random suffix for next attempt
		if attempt < maxAttempts-1 {
			// Ensure the name doesn't exceed database limits
			// Most databases have a limit around 100-255 chars for names
			if len(baseName)+4 > 100 {
				baseName = baseName[:96] // Leave room for 4-char suffix
			}
			// Append random suffix directly without underscore
			// e.g., "api-keys" becomes "api-keysx9k2"
			groupName = baseName + utils.GenerateRandomSuffix()
		} else {
			return "", fmt.Errorf("failed to generate unique group name for %s after %d attempts", baseName, maxAttempts)
		}
	}

	return groupName, nil
}

// KeyExportInfo represents exported key information
type KeyExportInfo struct {
	KeyValue string `json:"key_value"`
	Status   string `json:"status"`
}

// ExportKeysResult contains the exported keys and count
type ExportKeysResult struct {
	Keys  []KeyExportInfo
	Count int
}

// ExportKeysForGroup exports all keys for a specific group using ID keyset pagination.
func (s *ImportExportService) ExportKeysForGroup(groupID uint) (*ExportKeysResult, error) {
	var allKeys []KeyExportInfo
	var lastID uint
	totalExported := 0

	// Get total count for progress tracking
	var totalCount int64
	if err := s.db.Model(&models.APIKey{}).Where("group_id = ?", groupID).Count(&totalCount).Error; err != nil {
		logrus.WithError(err).Warnf("Failed to get total count for group %d", groupID)
	}

	// Log start with expected count
	if totalCount > 0 {
		logrus.Infof("Exporting %d keys for group ID: %d", totalCount, groupID)
	} else {
		logrus.Debugf("Starting key export for group ID: %d", groupID)
	}

	// Track progress percentage
	lastLoggedPercent := 0

	for {
		var batchKeys []struct {
			ID       uint
			KeyValue string
			Status   string
		}

		err := s.db.Model(&models.APIKey{}).
			Select("id, key_value, status").
			Where("group_id = ? AND id > ?", groupID, lastID).
			Order("id ASC").
			Limit(ExportBatchSize).
			Find(&batchKeys).Error

		if err != nil {
			return nil, fmt.Errorf("failed to export keys batch after id %d: %w", lastID, err)
		}

		// If no more records, we're done
		if len(batchKeys) == 0 {
			break
		}

		// Add keys from this batch
		for _, key := range batchKeys {
			allKeys = append(allKeys, KeyExportInfo{
				KeyValue: key.KeyValue,
				Status:   key.Status,
			})
		}

		lastID = batchKeys[len(batchKeys)-1].ID
		totalExported += len(batchKeys)

		// Only log progress at 25%, 50%, 75% intervals for large exports
		if totalCount > LargeExportThreshold && totalExported > 0 {
			currentPercent := (totalExported * 100) / int(totalCount)
			if currentPercent >= lastLoggedPercent+25 {
				logrus.Infof("Export progress: %d%% (%d/%d keys)", currentPercent, totalExported, totalCount)
				lastLoggedPercent = currentPercent
			}
		}

		// Debug level logging for detailed progress
		logrus.Debugf("Exported batch: %d keys after id %d (total: %d)",
			len(batchKeys), lastID, totalExported)

		// If we got less than batchSize, we've reached the end
		if len(batchKeys) < ExportBatchSize {
			break
		}
	}

	// Final summary log
	logrus.Infof("Export completed: %d keys for group ID %d", totalExported, groupID)

	return &ExportKeysResult{
		Keys:  allKeys,
		Count: totalExported,
	}, nil
}

// ExportKeysForGroups exports keys for multiple groups
// Returns a map of group ID to keys
func (s *ImportExportService) ExportKeysForGroups(groupIDs []uint) (map[uint][]KeyExportInfo, error) {
	if len(groupIDs) == 0 {
		return make(map[uint][]KeyExportInfo), nil
	}

	result := make(map[uint][]KeyExportInfo)
	totalExported := 0

	// Get total count for progress tracking
	var totalCount int64
	if err := s.db.Model(&models.APIKey{}).Where("group_id IN ?", groupIDs).Count(&totalCount).Error; err != nil {
		logrus.WithError(err).Warn("Failed to get total count for groups")
	}

	var lastGroupID uint
	var lastID uint

	// Log start with expected count
	if totalCount > 0 {
		logrus.Infof("Exporting %d keys from %d groups", totalCount, len(groupIDs))
	} else {
		logrus.Debugf("Starting key export for %d groups", len(groupIDs))
	}

	lastLoggedPercent := 0

	for {
		var batchKeys []struct {
			ID       uint
			GroupID  uint
			KeyValue string
			Status   string
		}

		// Query keys for all groups
		// Order by group_id and id to ensure stable pagination across groups
		err := s.db.Model(&models.APIKey{}).
			Select("id, group_id, key_value, status").
			Where("group_id IN ? AND (group_id > ? OR (group_id = ? AND id > ?))", groupIDs, lastGroupID, lastGroupID, lastID).
			Order("group_id ASC, id ASC").
			Limit(ExportMultiGroupBatchSize).
			Find(&batchKeys).Error

		if err != nil {
			return nil, fmt.Errorf("failed to export keys batch after group %d id %d: %w", lastGroupID, lastID, err)
		}

		// If no more records, we're done
		if len(batchKeys) == 0 {
			break
		}

		// Group keys by group ID
		for _, key := range batchKeys {
			if _, exists := result[key.GroupID]; !exists {
				result[key.GroupID] = []KeyExportInfo{}
			}
			result[key.GroupID] = append(result[key.GroupID], KeyExportInfo{
				KeyValue: key.KeyValue,
				Status:   key.Status,
			})
		}
		lastKey := batchKeys[len(batchKeys)-1]
		lastGroupID = lastKey.GroupID
		lastID = lastKey.ID
		totalExported += len(batchKeys)

		// Only log progress at 25%, 50%, 75% intervals for large exports
		if totalCount > LargeExportThreshold && totalExported > 0 {
			currentPercent := (totalExported * 100) / int(totalCount)
			if currentPercent >= lastLoggedPercent+25 {
				logrus.Infof("System export progress: %d%% (%d/%d keys)", currentPercent, totalExported, totalCount)
				lastLoggedPercent = currentPercent
			}
		}

		// Debug level logging for detailed progress
		logrus.Debugf("Exported batch: %d keys after group %d id %d (total: %d)",
			len(batchKeys), lastGroupID, lastID, totalExported)

		// If we got less than batchSize, we've reached the end
		if len(batchKeys) < ExportMultiGroupBatchSize {
			break
		}
	}

	logrus.Infof("System export completed: %d keys from %d groups", totalExported, len(groupIDs))

	return result, nil
}

// ImportKeys imports keys for a group using the bulk import service
// progressCallback is an optional callback function to report progress during import
// Keys are processed in batches: decrypt -> insert -> report progress
// This provides real-time feedback and avoids memory issues with large imports
//
// IMPORTANT: This function assumes key_value is already encrypted.
// For plaintext imports, the handler layer must encrypt keys before calling this function.
// See system_import_export_handler.go:409-415 for plaintext handling example.
func (s *ImportExportService) ImportKeys(tx *gorm.DB, groupID uint, keys []KeyExportInfo, progressCallback func(processed int)) error {
	if len(keys) == 0 {
		return nil
	}

	totalKeys := len(keys)
	totalProcessed := 0
	totalSkipped := 0

	// Track last reported progress to avoid regressions (monotonic progress)
	// Initialize to -1 to allow reporting 0% progress
	lastReported := -1
	reportProgress := func(v int) {
		if progressCallback == nil {
			return
		}
		if v > lastReported {
			lastReported = v
			progressCallback(v)
		}
	}

	// Report initial progress
	reportProgress(0)

	// Dynamic batch size based on total key count for optimal performance
	// Strategy: Balance memory usage, progress granularity, and transaction overhead
	// - Small imports (≤5K): Use default batch size (1000) for good progress feedback
	// - Medium imports (5K-20K): Use larger batches (2000) to reduce overhead
	// - Large imports (20K-100K): Use even larger batches (5000) for efficiency
	// - Massive imports (>100K): Use very large batches (10000) optimized for 500K+ operations
	decryptBatchSize := ImportDecryptBatchSize // Default: 1000
	if totalKeys > MassiveAsyncThreshold {
		// Tier 6: Massive imports (>100K keys)
		decryptBatchSize = 10000
	} else if totalKeys > AsyncThreshold {
		// Tier 5: Large imports (20K-100K keys)
		decryptBatchSize = 5000
	} else if totalKeys > BulkSyncThreshold {
		// Tier 3-4: Medium imports (5K-20K keys)
		decryptBatchSize = 2000
	}

	logrus.Infof("Importing %d keys for group ID: %d (batch size: %d)", totalKeys, groupID, decryptBatchSize)

	// Process keys in batches: decrypt batch -> insert batch -> report progress
	// This provides real-time progress feedback during both decrypt and insert phases
	for batchStart := 0; batchStart < totalKeys; batchStart += decryptBatchSize {
		batchEnd := batchStart + decryptBatchSize
		if batchEnd > totalKeys {
			batchEnd = totalKeys
		}

		batchKeys := keys[batchStart:batchEnd]
		keyModels := make([]models.APIKey, 0, len(batchKeys))
		batchSkipped := 0

		// Decrypt and prepare keys for this batch
		for i, keyInfo := range batchKeys {
			// Decrypt key_value to calculate key_hash
			decryptedKey, err := s.encryptionService.Decrypt(keyInfo.KeyValue)
			if err != nil {
				logrus.WithError(err).Debug("Failed to decrypt key during import, skipping")
				batchSkipped++
				totalSkipped++
				continue
			}

			keyHash := s.encryptionService.Hash(decryptedKey)

			// Import keys with clean state:
			// - Always set status to "active" for fresh start (ignore exported status)
			// - Always set FailureCount to 0 for fresh start (ignore exported failure_count)
			// This ensures imported keys start fresh without carrying over failure history
			// This prevents immediate blacklisting by CronChecker after import
			keyModels = append(keyModels, models.APIKey{
				GroupID:      groupID,
				KeyValue:     keyInfo.KeyValue,       // Keep encrypted value
				KeyHash:      keyHash,                // Calculated hash
				Status:       models.KeyStatusActive, // Always start as active
				FailureCount: 0,                      // Always reset to 0 for fresh start
			})

			// Report progress during decryption phase (within batch)
			// Report every ImportProgressReportInterval keys to provide feedback during CPU-intensive decryption
			currentProcessed := batchStart + i + 1
			if currentProcessed%ImportProgressReportInterval == 0 {
				reportProgress(currentProcessed)
			}
		}

		// Insert this batch into database
		if len(keyModels) > 0 {
			// Create a callback for bulk insert progress
			// Map insert progress to overall progress (decrypt phase already reported)
			insertCallback := func(inserted int) {
				// Calculate overall progress: batchStart (already decrypted) + inserted
				reportProgress(batchStart + inserted)
			}

			if err := s.bulkImportService.BulkInsertAPIKeysWithTx(tx, keyModels, insertCallback); err != nil {
				return fmt.Errorf("bulk import failed at batch %d-%d: %w", batchStart, batchEnd, err)
			}
			totalProcessed += len(keyModels)
		}

		// Report progress after each batch is inserted
		// This ensures progress reflects both decryption AND insertion completion
		// Use batchEnd instead of batchStart + len(batchKeys) for accuracy
		reportProgress(batchEnd)

		logrus.Debugf("Imported batch %d-%d: %d keys inserted, %d skipped (total: %d/%d)",
			batchStart, batchEnd, len(keyModels), batchSkipped, totalProcessed, totalKeys)
	}

	// Final summary and validation
	if totalSkipped > 0 {
		logrus.Warnf("Import completed with %d keys skipped due to decryption errors (total: %d/%d)",
			totalSkipped, totalProcessed, totalKeys)

		// Fail fast if decryption failure rate is too high (>50%)
		// This prevents silent data loss when:
		// 1. Plaintext keys are passed instead of encrypted keys
		// 2. Wrong encryption key is used
		// 3. Data corruption in the import file
		if totalSkipped > totalKeys/2 {
			return fmt.Errorf("import failed: %d/%d keys could not be decrypted (>50%% failure rate), possible encryption key mismatch or plaintext data", totalSkipped, totalKeys)
		}
	} else {
		logrus.Infof("Import completed successfully: %d keys imported", totalProcessed)
	}

	return nil
}

// ExportGroupData exports a complete group with all its data
type GroupExportData struct {
	Group       models.Group       `json:"group"`
	Keys        []KeyExportInfo    `json:"keys"`
	SubGroups   []SubGroupInfo     `json:"sub_groups,omitempty"`
	ChildGroups []ChildGroupExport `json:"child_groups,omitempty"` // Child groups for standard groups
}

// ChildGroupExport represents exported child group data
// Includes all configuration fields to ensure complete export/import
type ChildGroupExport struct {
	Name                 string          `json:"name"`
	DisplayName          string          `json:"display_name"`
	Description          string          `json:"description"`
	Enabled              bool            `json:"enabled"`
	ProxyKeys            string          `json:"proxy_keys"`
	Sort                 int             `json:"sort"`
	TestModel            string          `json:"test_model"`
	ParamOverrides       json.RawMessage `json:"param_overrides,omitempty"`
	Config               json.RawMessage `json:"config,omitempty"`
	HeaderRules          json.RawMessage `json:"header_rules,omitempty"`
	ModelMapping         string          `json:"model_mapping,omitempty"`
	ModelRedirectRules   json.RawMessage `json:"model_redirect_rules,omitempty"`
	ModelRedirectRulesV2 json.RawMessage `json:"model_redirect_rules_v2,omitempty"`
	ModelRedirectStrict  bool            `json:"model_redirect_strict"`
	CustomModelNames     json.RawMessage `json:"custom_model_names,omitempty"`
	Preconditions        json.RawMessage `json:"preconditions,omitempty"`
	PathRedirects        json.RawMessage `json:"path_redirects,omitempty"`
	Keys                 []KeyExportInfo `json:"keys"`
}

// SubGroupInfo represents sub-group relationship
type SubGroupInfo struct {
	GroupName                  string `json:"group_name"`
	Weight                     int    `json:"weight"`
	MinEffectiveWeight         int    `json:"min_effective_weight,omitempty"`
	HealthResetIntervalSeconds int64  `json:"health_reset_interval_seconds,omitempty"`
}

// DynamicWeightMetricExportInfo stores dynamic health metrics using stable group names.
type DynamicWeightMetricExportInfo struct {
	MetricType            models.MetricType `json:"metric_type"`
	GroupName             string            `json:"group_name"`
	SubGroupName          string            `json:"sub_group_name,omitempty"`
	SourceModel           string            `json:"source_model,omitempty"`
	TargetModel           string            `json:"target_model,omitempty"`
	ConsecutiveFailures   int64             `json:"consecutive_failures"`
	LastFailureAt         *time.Time        `json:"last_failure_at,omitempty"`
	LastSuccessAt         *time.Time        `json:"last_success_at,omitempty"`
	ConsecutiveRateLimits int64             `json:"consecutive_rate_limits"`
	LastRateLimitAt       *time.Time        `json:"last_rate_limit_at,omitempty"`
	Requests7d            int64             `json:"requests_7d"`
	Successes7d           int64             `json:"successes_7d"`
	RateLimits7d          int64             `json:"rate_limits_7d"`
	Requests14d           int64             `json:"requests_14d"`
	Successes14d          int64             `json:"successes_14d"`
	RateLimits14d         int64             `json:"rate_limits_14d"`
	Requests30d           int64             `json:"requests_30d"`
	Successes30d          int64             `json:"successes_30d"`
	RateLimits30d         int64             `json:"rate_limits_30d"`
	Requests90d           int64             `json:"requests_90d"`
	Successes90d          int64             `json:"successes_90d"`
	RateLimits90d         int64             `json:"rate_limits_90d"`
	Requests180d          int64             `json:"requests_180d"`
	Successes180d         int64             `json:"successes_180d"`
	RateLimits180d        int64             `json:"rate_limits_180d"`
	LastRolloverAt        *time.Time        `json:"last_rollover_at,omitempty"`
	UpdatedAt             time.Time         `json:"updated_at"`
	DeletedAt             *time.Time        `json:"deleted_at,omitempty"`
}

// ExportGroup exports a complete group with keys and sub-groups
func (s *ImportExportService) ExportGroup(groupID uint) (*GroupExportData, error) {
	var group models.Group
	if err := s.db.First(&group, groupID).Error; err != nil {
		return nil, fmt.Errorf("failed to find group: %w", err)
	}

	// Child groups cannot be exported individually - they must be exported with their parent
	if group.ParentGroupID != nil {
		return nil, ErrChildGroupCannotExportIndividually
	}

	// Export keys
	keysResult, err := s.ExportKeysForGroup(groupID)
	if err != nil {
		return nil, fmt.Errorf("failed to export keys: %w", err)
	}

	result := &GroupExportData{
		Group: group,
		Keys:  keysResult.Keys,
	}

	// If it's an aggregate group, export sub-groups
	if group.GroupType == "aggregate" {
		var subGroupRelations []models.GroupSubGroup
		err := s.db.Where("group_id = ?", groupID).
			Find(&subGroupRelations).Error

		if err == nil && len(subGroupRelations) > 0 {
			// Get sub-group IDs
			subGroupIDs := make([]uint, 0, len(subGroupRelations))
			relationMap := make(map[uint]models.GroupSubGroup, len(subGroupRelations))
			for _, rel := range subGroupRelations {
				subGroupIDs = append(subGroupIDs, rel.SubGroupID)
				relationMap[rel.SubGroupID] = rel
			}

			// Get sub-group details
			var subGroups []models.Group
			if err := s.db.Where("id IN ?", subGroupIDs).Find(&subGroups).Error; err == nil {
				result.SubGroups = make([]SubGroupInfo, 0, len(subGroups))
				for _, sg := range subGroups {
					relation := relationMap[sg.ID]
					result.SubGroups = append(result.SubGroups, SubGroupInfo{
						GroupName:                  sg.Name,
						Weight:                     relation.Weight,
						MinEffectiveWeight:         normalizeSubGroupMinEffectiveWeight(relation.Weight, relation.MinEffectiveWeight),
						HealthResetIntervalSeconds: relation.HealthResetIntervalSeconds,
					})
				}
			}
		}
	}

	// If it's a standard group (not aggregate), export child groups
	if group.GroupType == "standard" {
		var childGroups []models.Group
		if err := s.db.Where("parent_group_id = ?", groupID).
			Order("sort ASC, name ASC").
			Find(&childGroups).Error; err == nil && len(childGroups) > 0 {

			// Get all child group IDs for batch key export
			childGroupIDs := make([]uint, 0, len(childGroups))
			for _, cg := range childGroups {
				childGroupIDs = append(childGroupIDs, cg.ID)
			}

			// Export keys for all child groups in one batch
			childKeysMap, err := s.ExportKeysForGroups(childGroupIDs)
			if err != nil {
				logrus.WithError(err).Warn("Failed to export child group keys")
				childKeysMap = make(map[uint][]KeyExportInfo)
			}

			result.ChildGroups = make([]ChildGroupExport, 0, len(childGroups))
			for _, cg := range childGroups {
				result.ChildGroups = append(result.ChildGroups, buildChildGroupExport(cg, childKeysMap[cg.ID]))
			}

			logrus.Infof("Exported %d child groups for parent group %s", len(childGroups), group.Name)
		}
	}

	return result, nil
}

// ImportGroup imports a complete group with keys and sub-groups
// progressCallback is an optional callback function to report progress during import
func (s *ImportExportService) ImportGroup(tx *gorm.DB, data *GroupExportData, progressCallback func(processed int)) (uint, error) {
	importedGroup, _, err := s.importGroup(tx, data, progressCallback, importGroupOptions{
		ImportAggregateSubGroups: true,
	})
	if err != nil {
		return 0, err
	}
	return importedGroup.ID, nil
}

func (s *ImportExportService) importGroup(tx *gorm.DB, data *GroupExportData, progressCallback func(processed int), options importGroupOptions) (*models.Group, map[string]models.Group, error) {
	// Child groups cannot be imported individually - they are imported with their parent
	if data.Group.ParentGroupID != nil {
		return nil, nil, ErrChildGroupCannotImportIndividually
	}

	// Use the centralized unique name generation function
	groupName, err := s.GenerateUniqueGroupName(tx, data.Group.Name)
	if err != nil {
		return nil, nil, err
	}

	// Create the group with cleaned configuration
	newGroup := data.Group
	newGroup.ID = 0 // Reset ID for new record
	newGroup.Name = groupName
	newGroup.ParentGroupID = nil // Ensure parent group ID is nil for imported groups
	newGroup.BoundSiteID = nil   // Clear site binding - must be re-established after import

	// Calculate the suffix that was added to the name (if any)
	var nameSuffix string
	if groupName != data.Group.Name && strings.HasPrefix(groupName, data.Group.Name) {
		nameSuffix = groupName[len(data.Group.Name):]
	}

	// Apply suffix to display name too
	if newGroup.DisplayName != "" && nameSuffix != "" {
		newGroup.DisplayName = newGroup.DisplayName + nameSuffix
	}

	// Clean Config values to remove leading/trailing whitespace
	// This fixes issues like ' http://...' which cause URL parsing errors
	if newGroup.Config != nil {
		cleanedConfig := make(map[string]any)
		for key, value := range newGroup.Config {
			if strValue, ok := value.(string); ok {
				cleanedConfig[key] = strings.TrimSpace(strValue)
			} else {
				cleanedConfig[key] = value
			}
		}
		newGroup.Config = cleanedConfig
	}
	if err := validateParamOverrides(newGroup.ParamOverrides); err != nil {
		return nil, nil, err
	}

	if err := tx.Create(&newGroup).Error; err != nil {
		return nil, nil, fmt.Errorf("failed to create group: %w", err)
	}
	if !data.Group.Enabled {
		// GORM skips zero values for fields with DB defaults during Create; preserve disabled imports explicitly.
		if err := tx.Model(&newGroup).Update("enabled", false).Error; err != nil {
			return nil, nil, fmt.Errorf("failed to restore imported group enabled state: %w", err)
		}
		newGroup.Enabled = false
	}

	if groupName != data.Group.Name {
		logrus.Infof("Imported group %s (renamed from %s) with ID %d", groupName, data.Group.Name, newGroup.ID)
	} else {
		logrus.Debugf("Imported group %s with ID %d", groupName, newGroup.ID)
	}

	// Import keys
	if len(data.Keys) > 0 {
		if err := s.ImportKeys(tx, newGroup.ID, data.Keys, progressCallback); err != nil {
			return nil, nil, fmt.Errorf("failed to import keys: %w", err)
		}
	}

	// Import sub-groups for aggregate groups
	if options.ImportAggregateSubGroups && newGroup.GroupType == "aggregate" && len(data.SubGroups) > 0 {
		imported := map[string]models.Group{
			data.Group.Name: newGroup,
		}
		if _, skipped := s.importAggregateSubGroupRelations(tx, []GroupExportData{*data}, imported); skipped > 0 {
			logrus.Warnf("Skipped %d sub-group relation(s) while importing aggregate group %s", skipped, newGroup.Name)
		}
	}

	// Import child groups for standard groups
	importedChildGroups := make(map[string]models.Group)
	if newGroup.GroupType == "standard" && len(data.ChildGroups) > 0 {
		logrus.Infof("Importing %d child groups for standard group %s (ID: %d)", len(data.ChildGroups), newGroup.Name, newGroup.ID)
		importedChildren, err := s.importChildGroups(tx, newGroup.ID, newGroup.Name, newGroup.ChannelType, newGroup.TestModel, data.ChildGroups)
		if err != nil {
			logrus.WithError(err).Errorf("Failed to import child groups for group %s", newGroup.Name)
			return nil, nil, fmt.Errorf("failed to import child groups: %w", err)
		}
		importedChildGroups = importedChildren
		logrus.Infof("Successfully imported child groups for standard group %s", newGroup.Name)
	}

	return &newGroup, importedChildGroups, nil
}

// importChildGroups imports child groups for a parent group
// parentTestModel is used as fallback when child group's TestModel is empty (backward compatibility)
func (s *ImportExportService) importChildGroups(tx *gorm.DB, parentGroupID uint, parentName string, parentChannelType string, parentTestModel string, childGroups []ChildGroupExport) (map[string]models.Group, error) {
	logrus.Infof("Starting import of %d child groups for parent %s (ID: %d)", len(childGroups), parentName, parentGroupID)
	importedChildrenByOriginalName := make(map[string]models.Group, len(childGroups))

	for i, childData := range childGroups {
		logrus.Debugf("Processing child group %d/%d: %s", i+1, len(childGroups), childData.Name)

		// Each child group should independently check for name conflicts
		// Try the original name, and add a random suffix if there's a conflict
		childName, err := s.GenerateUniqueGroupName(tx, childData.Name)
		if err != nil {
			logrus.WithError(err).Errorf("Failed to generate unique name for child group %s (index %d)", childData.Name, i)
			continue
		}

		if childName != childData.Name {
			logrus.Infof("Child group renamed: %s -> %s", childData.Name, childName)
		}

		// Calculate the suffix that was added (if any)
		var childNameSuffix string
		if childName != childData.Name && strings.HasPrefix(childName, childData.Name) {
			childNameSuffix = childName[len(childData.Name):]
		}

		// Build upstream URL pointing to parent group (use actual parent name, not original)
		port := utils.ParseInteger(os.Getenv("PORT"), 3001)
		upstreamURL := fmt.Sprintf("http://127.0.0.1:%d/proxy/%s", port, parentName)
		upstreamsJSON := fmt.Sprintf(`[{"url":"%s","weight":1}]`, upstreamURL)

		logrus.Debugf("Child group %s upstream: %s", childName, upstreamURL)

		// Fallback to parent's TestModel if child's is empty (backward compatibility)
		// Older exports may not include test_model for child groups
		testModel := childData.TestModel
		if testModel == "" {
			testModel = parentTestModel
			logrus.Debugf("Child group %s inheriting TestModel from parent: %s", childName, testModel)
		}

		// Create child group with inherited channel_type from parent
		childGroup := models.Group{
			Name:                childName,
			DisplayName:         childData.DisplayName,
			Description:         childData.Description,
			GroupType:           "standard",
			ChannelType:         parentChannelType, // Inherit from parent
			Enabled:             childData.Enabled,
			ParentGroupID:       &parentGroupID,
			ProxyKeys:           childData.ProxyKeys,
			Sort:                childData.Sort,
			Upstreams:           []byte(upstreamsJSON),
			TestModel:           testModel, // Use fallback if empty
			ModelMapping:        childData.ModelMapping,
			ModelRedirectStrict: childData.ModelRedirectStrict,
		}

		// Import JSON fields from exported data
		if len(childData.ParamOverrides) > 0 {
			var paramOverrides map[string]any
			if err := json.Unmarshal(childData.ParamOverrides, &paramOverrides); err != nil {
				logrus.WithError(err).Warnf("Invalid param_overrides for child group %s, skipping", childName)
			} else if err := validateParamOverrides(paramOverrides); err != nil {
				logrus.WithError(err).Warnf("Invalid param_overrides for child group %s, skipping", childName)
			} else {
				childGroup.ParamOverrides = paramOverrides
			}
		}
		if len(childData.Config) > 0 {
			var config map[string]any
			if err := json.Unmarshal(childData.Config, &config); err != nil {
				logrus.WithError(err).Warnf("Invalid config for child group %s, skipping", childName)
			} else {
				childGroup.Config = config
			}
		}
		if len(childData.HeaderRules) > 0 {
			childGroup.HeaderRules = []byte(childData.HeaderRules)
		}
		if len(childData.ModelRedirectRules) > 0 {
			var redirectRules map[string]any
			if err := json.Unmarshal(childData.ModelRedirectRules, &redirectRules); err == nil {
				childGroup.ModelRedirectRules = redirectRules
			}
		}
		if len(childData.ModelRedirectRulesV2) > 0 {
			// Merge duplicate targets in V2 rules (consistent with parent group import)
			merged, err := utils.MergeModelRedirectRulesV2([]byte(childData.ModelRedirectRulesV2))
			if err != nil {
				logrus.WithError(err).Warnf("Failed to merge child group model redirect rules V2 for %s, using original", childName)
				childGroup.ModelRedirectRulesV2 = []byte(childData.ModelRedirectRulesV2)
			} else {
				childGroup.ModelRedirectRulesV2 = merged
			}
		}
		if len(childData.CustomModelNames) > 0 {
			childGroup.CustomModelNames = []byte(childData.CustomModelNames)
		}
		if len(childData.Preconditions) > 0 {
			var preconditions map[string]any
			if err := json.Unmarshal(childData.Preconditions, &preconditions); err == nil {
				childGroup.Preconditions = preconditions
			}
		}
		if len(childData.PathRedirects) > 0 {
			childGroup.PathRedirects = []byte(childData.PathRedirects)
		}

		// Apply suffix to display name if a suffix was added to the name
		if childGroup.DisplayName != "" && childNameSuffix != "" {
			childGroup.DisplayName = childData.DisplayName + childNameSuffix
		}

		logrus.Debugf("Creating child group in database: name=%s, display_name=%s, enabled=%v, parent_id=%d",
			childGroup.Name, childGroup.DisplayName, childGroup.Enabled, parentGroupID)

		if err := tx.Create(&childGroup).Error; err != nil {
			logrus.WithError(err).Errorf("Failed to create child group %s in database", childName)
			continue
		}
		if !childData.Enabled {
			// GORM skips zero values for fields with DB defaults during Create; preserve disabled child imports explicitly.
			if err := tx.Model(&childGroup).Update("enabled", false).Error; err != nil {
				logrus.WithError(err).Errorf("Failed to restore enabled state for child group %s", childName)
				continue
			}
			childGroup.Enabled = false
		}

		logrus.Infof("Successfully created child group %s (ID: %d) for parent %s", childName, childGroup.ID, parentName)

		// Import keys for child group
		if len(childData.Keys) > 0 {
			logrus.Debugf("Importing %d keys for child group %s", len(childData.Keys), childName)
			if err := s.ImportKeys(tx, childGroup.ID, childData.Keys, nil); err != nil {
				logrus.WithError(err).Errorf("Failed to import keys for child group %s", childName)
			} else {
				logrus.Debugf("Successfully imported %d keys for child group %s", len(childData.Keys), childName)
			}
		} else {
			logrus.Debugf("No keys to import for child group %s", childName)
		}

		importedChildrenByOriginalName[childData.Name] = childGroup
	}

	logrus.Infof("Completed import of child groups for parent %s", parentName)
	return importedChildrenByOriginalName, nil
}

// SystemExportData represents full system export
type SystemExportData struct {
	Version        string                          `json:"version"`
	ExportedAt     string                          `json:"exported_at"`
	SystemSettings map[string]string               `json:"system_settings"`
	Groups         []GroupExportData               `json:"groups"`
	ManagedSites   *ManagedSitesExportData         `json:"managed_sites,omitempty"`
	HubAccessKeys  []HubAccessKeyExportInfo        `json:"hub_access_keys,omitempty"`
	DynamicWeights []DynamicWeightMetricExportInfo `json:"dynamic_weights,omitempty"`
}

// HubAccessKeyExportInfo represents exported Hub access key data.
// Key values remain encrypted (same as database storage) for security.
// Note: This type is intentionally duplicated from centralizedmgmt package
// to avoid circular dependency between services and centralizedmgmt packages.
type HubAccessKeyExportInfo struct {
	Name          string   `json:"name"`
	KeyValue      string   `json:"key_value"`      // Encrypted value (same as storage)
	AllowedModels []string `json:"allowed_models"` // Parsed from JSON for readability
	Enabled       bool     `json:"enabled"`
}

// ManagedSitesExportData represents exported managed sites data
type ManagedSitesExportData struct {
	AutoCheckin *ManagedSiteAutoCheckinConfig `json:"auto_checkin,omitempty"`
	AutoBalance *ManagedSiteAutoBalanceConfig `json:"auto_balance,omitempty"`
	Sites       []ManagedSiteExportInfo       `json:"sites"`
}

type ManagedSiteAutoBalanceConfig struct {
	GlobalEnabled bool `json:"global_enabled"`
	IntervalHours int  `json:"interval_hours"`
}

const (
	minManagedSiteAutoBalanceIntervalHours     = 1
	maxManagedSiteAutoBalanceIntervalHours     = 24
	defaultManagedSiteAutoBalanceIntervalHours = 24
	maxManagedSiteScheduleTimesStorageLength   = 255
)

// ManagedSiteAutoCheckinConfig represents auto-checkin configuration for export
type ManagedSiteAutoCheckinConfig struct {
	GlobalEnabled     bool                              `json:"global_enabled"`
	ScheduleTimes     []string                          `json:"schedule_times,omitempty"`
	WindowStart       string                            `json:"window_start"`
	WindowEnd         string                            `json:"window_end"`
	ScheduleMode      string                            `json:"schedule_mode"`
	DeterministicTime string                            `json:"deterministic_time,omitempty"`
	RetryStrategy     ManagedSiteAutoCheckinRetryConfig `json:"retry_strategy"`
}

// ManagedSiteAutoCheckinRetryConfig represents retry strategy for export
type ManagedSiteAutoCheckinRetryConfig struct {
	Enabled           bool `json:"enabled"`
	IntervalMinutes   int  `json:"interval_minutes"`
	MaxAttemptsPerDay int  `json:"max_attempts_per_day"`
}

// ManagedSiteExportInfo represents exported site information
type ManagedSiteExportInfo struct {
	Name               string `json:"name"`
	Notes              string `json:"notes"`
	Description        string `json:"description"`
	Sort               int    `json:"sort"`
	Enabled            bool   `json:"enabled"`
	BaseURL            string `json:"base_url"`
	SiteType           string `json:"site_type"`
	UserID             string `json:"user_id"`
	CheckInPageURL     string `json:"checkin_page_url"`
	CheckInAvailable   bool   `json:"checkin_available"`
	CheckInEnabled     bool   `json:"checkin_enabled"`
	AutoCheckInEnabled bool   `json:"auto_checkin_enabled"`
	CustomCheckInURL   string `json:"custom_checkin_url"`
	AuthType           string `json:"auth_type"`
	AuthValue          string `json:"auth_value,omitempty"`
}

// managedSiteModel represents the database model for managed sites (minimal for export/import).
// Note: This is intentionally separate from sitemanagement.ManagedSite to:
// 1. Avoid circular dependency between services and sitemanagement packages
// 2. Keep export/import logic self-contained with only the fields needed
// 3. Maintain clear module boundaries for easier maintenance
type managedSiteModel struct {
	ID                 uint   `gorm:"primaryKey"`
	Name               string `gorm:"column:name"`
	Notes              string `gorm:"column:notes"`
	Description        string `gorm:"column:description"`
	Sort               int    `gorm:"column:sort"`
	Enabled            bool   `gorm:"column:enabled"`
	BaseURL            string `gorm:"column:base_url"`
	SiteType           string `gorm:"column:site_type"`
	UserID             string `gorm:"column:user_id"`
	CheckInPageURL     string `gorm:"column:checkin_page_url"`
	CheckInAvailable   bool   `gorm:"column:checkin_available"`
	CheckInEnabled     bool   `gorm:"column:checkin_enabled"`
	AutoCheckInEnabled bool   `gorm:"column:auto_checkin_enabled"`
	CustomCheckInURL   string `gorm:"column:custom_checkin_url"`
	AuthType           string `gorm:"column:auth_type"`
	AuthValue          string `gorm:"column:auth_value"`
}

func (managedSiteModel) TableName() string {
	return "managed_sites"
}

// ManagedSiteSetting represents the database model for managed site settings
type managedSiteSettingModel struct {
	ID                          uint   `gorm:"primaryKey"`
	AutoCheckinEnabled          bool   `gorm:"column:auto_checkin_enabled"`
	AutoBalanceEnabled          bool   `gorm:"column:auto_balance_enabled"`
	BalanceRefreshIntervalHours int    `gorm:"column:balance_refresh_interval_hours"`
	ScheduleTimes               string `gorm:"column:schedule_times"`
	WindowStart                 string `gorm:"column:window_start"`
	WindowEnd                   string `gorm:"column:window_end"`
	ScheduleMode                string `gorm:"column:schedule_mode"`
	DeterministicTime           string `gorm:"column:deterministic_time"`
	RetryEnabled                bool   `gorm:"column:retry_enabled"`
	RetryIntervalMinutes        int    `gorm:"column:retry_interval_minutes"`
	RetryMaxAttemptsPerDay      int    `gorm:"column:retry_max_attempts_per_day"`
}

func (managedSiteSettingModel) TableName() string {
	return "managed_site_settings"
}

// ValidateManagedSiteAutoBalanceConfig validates the full-system import contract.
func ValidateManagedSiteAutoBalanceConfig(config *ManagedSiteAutoBalanceConfig) error {
	if config == nil {
		return nil
	}
	if config.IntervalHours < minManagedSiteAutoBalanceIntervalHours || config.IntervalHours > maxManagedSiteAutoBalanceIntervalHours {
		return fmt.Errorf(
			"auto balance interval_hours must be between %d and %d",
			minManagedSiteAutoBalanceIntervalHours,
			maxManagedSiteAutoBalanceIntervalHours,
		)
	}
	return nil
}

// ValidateManagedSiteAutoCheckinConfig validates the full-system import contract.
func ValidateManagedSiteAutoCheckinConfig(config *ManagedSiteAutoCheckinConfig) error {
	if config == nil {
		return nil
	}

	mode := strings.TrimSpace(config.ScheduleMode)
	if mode == "" {
		mode = "multiple"
	}
	// Reject oversized serialized values instead of truncating them and changing schedule semantics.
	if len(joinManagedSiteScheduleTimes(config.ScheduleTimes)) > maxManagedSiteScheduleTimesStorageLength {
		return fmt.Errorf("auto check-in schedule_times must not exceed %d characters", maxManagedSiteScheduleTimesStorageLength)
	}
	switch mode {
	case "multiple":
		// A nil slice identifies backups created before schedule_times was exported.
		if config.ScheduleTimes == nil {
			return nil
		}
		if len(config.ScheduleTimes) == 0 {
			return errors.New("auto check-in schedule_times must not be empty")
		}
		seen := make(map[string]struct{}, len(config.ScheduleTimes))
		for i, value := range config.ScheduleTimes {
			value = strings.TrimSpace(value)
			if !validManagedSiteScheduleTime(value) {
				return fmt.Errorf("auto check-in schedule_times[%d] must be a valid time", i)
			}
			if _, ok := seen[value]; ok {
				return fmt.Errorf("auto check-in schedule_times[%d] duplicates an earlier time", i)
			}
			seen[value] = struct{}{}
		}
	case "random":
		if !validManagedSiteScheduleTime(config.WindowStart) || !validManagedSiteScheduleTime(config.WindowEnd) {
			return errors.New("auto check-in random window must contain valid start and end times")
		}
	case "deterministic":
		if !validManagedSiteScheduleTime(config.DeterministicTime) {
			return errors.New("auto check-in deterministic_time must be a valid time")
		}
	default:
		return errors.New("unsupported auto check-in schedule_mode")
	}
	return nil
}

func joinManagedSiteScheduleTimes(scheduleTimes []string) string {
	normalized := make([]string, len(scheduleTimes))
	for i, value := range scheduleTimes {
		normalized[i] = strings.TrimSpace(value)
	}
	return strings.Join(normalized, ",")
}

func validManagedSiteScheduleTime(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return false
	}
	minute, err := strconv.Atoi(parts[1])
	return err == nil && minute >= 0 && minute <= 59
}

func normalizeManagedSiteAutoBalanceIntervalHours(intervalHours int) int {
	if intervalHours < minManagedSiteAutoBalanceIntervalHours || intervalHours > maxManagedSiteAutoBalanceIntervalHours {
		return defaultManagedSiteAutoBalanceIntervalHours
	}
	return intervalHours
}

// exportManagedSites exports all managed sites and their configuration.
func (s *ImportExportService) exportManagedSites() (*ManagedSitesExportData, error) {
	hasSitesTable := s.db.Migrator().HasTable(&managedSiteModel{})
	hasSettingsTable := s.db.Migrator().HasTable(&managedSiteSettingModel{})
	if !hasSitesTable && !hasSettingsTable {
		return nil, nil
	}

	var sites []managedSiteModel
	if hasSitesTable {
		if err := s.db.Order("sort ASC, id ASC").Find(&sites).Error; err != nil {
			return nil, fmt.Errorf("failed to export managed sites: %w", err)
		}
	}

	result := &ManagedSitesExportData{
		Sites: make([]ManagedSiteExportInfo, 0, len(sites)),
	}

	// Export auto-checkin config
	// Note: Settings row always has ID=1 (single-row config pattern used throughout the app)
	var setting managedSiteSettingModel
	hasScheduleConfig := false
	if hasSettingsTable {
		err := s.db.First(&setting, 1).Error
		if err == nil {
			hasScheduleConfig = true
			var scheduleTimes []string
			for _, value := range strings.Split(setting.ScheduleTimes, ",") {
				if value = strings.TrimSpace(value); value != "" {
					scheduleTimes = append(scheduleTimes, value)
				}
			}
			result.AutoCheckin = &ManagedSiteAutoCheckinConfig{
				GlobalEnabled:     setting.AutoCheckinEnabled,
				ScheduleTimes:     scheduleTimes,
				WindowStart:       setting.WindowStart,
				WindowEnd:         setting.WindowEnd,
				ScheduleMode:      setting.ScheduleMode,
				DeterministicTime: setting.DeterministicTime,
				RetryStrategy: ManagedSiteAutoCheckinRetryConfig{
					Enabled:           setting.RetryEnabled,
					IntervalMinutes:   setting.RetryIntervalMinutes,
					MaxAttemptsPerDay: setting.RetryMaxAttemptsPerDay,
				},
			}
			result.AutoBalance = &ManagedSiteAutoBalanceConfig{
				GlobalEnabled: setting.AutoBalanceEnabled,
				IntervalHours: normalizeManagedSiteAutoBalanceIntervalHours(setting.BalanceRefreshIntervalHours),
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to export managed-site schedule config: %w", err)
		}
	}
	if len(sites) == 0 && !hasScheduleConfig {
		return nil, nil
	}

	// Export sites (keep auth_value encrypted)
	for _, site := range sites {
		result.Sites = append(result.Sites, ManagedSiteExportInfo{
			Name:               site.Name,
			Notes:              site.Notes,
			Description:        site.Description,
			Sort:               site.Sort,
			Enabled:            site.Enabled,
			BaseURL:            site.BaseURL,
			SiteType:           site.SiteType,
			UserID:             site.UserID,
			CheckInPageURL:     site.CheckInPageURL,
			CheckInAvailable:   site.CheckInAvailable,
			CheckInEnabled:     site.CheckInEnabled,
			AutoCheckInEnabled: site.AutoCheckInEnabled,
			CustomCheckInURL:   site.CustomCheckInURL,
			AuthType:           site.AuthType,
			AuthValue:          site.AuthValue, // Keep encrypted
		})
	}

	return result, nil
}

// ExportSystem exports the entire system configuration
func (s *ImportExportService) ExportSystem() (*SystemExportData, error) {
	// Export system settings
	var settings []models.SystemSetting
	if err := s.db.Find(&settings).Error; err != nil {
		return nil, fmt.Errorf("failed to export system settings: %w", err)
	}

	settingsMap := make(map[string]string)
	for _, setting := range settings {
		settingsMap[setting.SettingKey] = setting.SettingValue
	}

	// Export all groups (excluding child groups - they will be nested under their parents)
	var groups []models.Group
	if err := s.db.Where("parent_group_id IS NULL").Order(GroupListOrderClause).Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("failed to export groups: %w", err)
	}

	// Get all group IDs (including child groups) for key export
	var allGroups []models.Group
	if err := s.db.Order(GroupListOrderClause).Find(&allGroups).Error; err != nil {
		return nil, fmt.Errorf("failed to export all groups: %w", err)
	}

	allGroupIDs := make([]uint, 0, len(allGroups))
	for _, group := range allGroups {
		allGroupIDs = append(allGroupIDs, group.ID)
	}

	// Export all keys at once
	keysMap, err := s.ExportKeysForGroups(allGroupIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to export keys: %w", err)
	}

	// Build a map of child groups by parent ID
	childGroupsByParent := make(map[uint][]models.Group)
	for _, group := range allGroups {
		if group.ParentGroupID != nil {
			parentID := *group.ParentGroupID
			childGroupsByParent[parentID] = append(childGroupsByParent[parentID], group)
		}
	}

	// Build export data
	groupExports := make([]GroupExportData, 0, len(groups))
	for _, group := range groups {
		groupData := GroupExportData{
			Group: group,
			Keys:  keysMap[group.ID],
		}

		// Export sub-groups for aggregate groups
		if group.GroupType == "aggregate" {
			var subGroupRelations []models.GroupSubGroup
			err := s.db.Where("group_id = ?", group.ID).
				Find(&subGroupRelations).Error

			if err == nil && len(subGroupRelations) > 0 {
				// Get sub-group IDs
				subGroupIDs := make([]uint, 0, len(subGroupRelations))
				relationMap := make(map[uint]models.GroupSubGroup, len(subGroupRelations))
				for _, rel := range subGroupRelations {
					subGroupIDs = append(subGroupIDs, rel.SubGroupID)
					relationMap[rel.SubGroupID] = rel
				}

				// Get sub-group details
				var subGroups []models.Group
				if err := s.db.Where("id IN ?", subGroupIDs).Find(&subGroups).Error; err == nil {
					groupData.SubGroups = make([]SubGroupInfo, 0, len(subGroups))
					for _, sg := range subGroups {
						relation := relationMap[sg.ID]
						groupData.SubGroups = append(groupData.SubGroups, SubGroupInfo{
							GroupName:                  sg.Name,
							Weight:                     relation.Weight,
							HealthResetIntervalSeconds: relation.HealthResetIntervalSeconds,
						})
					}
				}
			}
		}

		// Export child groups for standard groups (nested under parent)
		if group.GroupType == "standard" {
			if childGroups, exists := childGroupsByParent[group.ID]; exists && len(childGroups) > 0 {
				groupData.ChildGroups = make([]ChildGroupExport, 0, len(childGroups))
				for _, cg := range childGroups {
					groupData.ChildGroups = append(groupData.ChildGroups, buildChildGroupExport(cg, keysMap[cg.ID]))
				}
			}
		}

		groupExports = append(groupExports, groupData)
	}

	// Export managed sites
	managedSitesData, err := s.exportManagedSites()
	if err != nil {
		return nil, err
	}

	// Export Hub access keys
	hubAccessKeysData := s.exportHubAccessKeys()

	// Export dynamic health metrics after groups are known so IDs can be converted to names.
	dynamicWeightsData := s.exportDynamicWeights()

	return &SystemExportData{
		Version:        "2.0",
		ExportedAt:     time.Now().Format(time.RFC3339),
		SystemSettings: settingsMap,
		Groups:         groupExports,
		ManagedSites:   managedSitesData,
		HubAccessKeys:  hubAccessKeysData,
		DynamicWeights: dynamicWeightsData,
	}, nil
}

func (s *ImportExportService) exportDynamicWeights() []DynamicWeightMetricExportInfo {
	var metrics []models.DynamicWeightMetric
	if err := s.db.Order("id ASC").Find(&metrics).Error; err != nil {
		logrus.WithError(err).Warn("Failed to export dynamic weight metrics")
		return nil
	}
	if len(metrics) == 0 {
		return nil
	}

	groupIDs := make([]uint, 0, len(metrics)*2)
	for _, metric := range metrics {
		groupIDs = append(groupIDs, metric.GroupID)
		if metric.SubGroupID > 0 {
			groupIDs = append(groupIDs, metric.SubGroupID)
		}
	}

	var groups []models.Group
	if err := s.db.Select("id", "name").Where("id IN ?", groupIDs).Find(&groups).Error; err != nil {
		logrus.WithError(err).Warn("Failed to load group names for dynamic weight export")
		return nil
	}

	groupNamesByID := make(map[uint]string, len(groups))
	for _, group := range groups {
		groupNamesByID[group.ID] = group.Name
	}

	result := make([]DynamicWeightMetricExportInfo, 0, len(metrics))
	for _, metric := range metrics {
		groupName := groupNamesByID[metric.GroupID]
		if groupName == "" {
			continue
		}

		item := DynamicWeightMetricExportInfo{
			MetricType:            metric.MetricType,
			GroupName:             groupName,
			SourceModel:           metric.SourceModel,
			TargetModel:           metric.TargetModel,
			ConsecutiveFailures:   metric.ConsecutiveFailures,
			LastFailureAt:         metric.LastFailureAt,
			LastSuccessAt:         metric.LastSuccessAt,
			ConsecutiveRateLimits: metric.ConsecutiveRateLimits,
			LastRateLimitAt:       metric.LastRateLimitAt,
			Requests7d:            metric.Requests7d,
			Successes7d:           metric.Successes7d,
			RateLimits7d:          metric.RateLimits7d,
			Requests14d:           metric.Requests14d,
			Successes14d:          metric.Successes14d,
			RateLimits14d:         metric.RateLimits14d,
			Requests30d:           metric.Requests30d,
			Successes30d:          metric.Successes30d,
			RateLimits30d:         metric.RateLimits30d,
			Requests90d:           metric.Requests90d,
			Successes90d:          metric.Successes90d,
			RateLimits90d:         metric.RateLimits90d,
			Requests180d:          metric.Requests180d,
			Successes180d:         metric.Successes180d,
			RateLimits180d:        metric.RateLimits180d,
			LastRolloverAt:        metric.LastRolloverAt,
			UpdatedAt:             metric.UpdatedAt,
			DeletedAt:             metric.DeletedAt,
		}
		if metric.SubGroupID > 0 {
			item.SubGroupName = groupNamesByID[metric.SubGroupID]
			if item.SubGroupName == "" {
				continue
			}
		}
		result = append(result, item)
	}
	return result
}

func buildChildGroupExport(group models.Group, keys []KeyExportInfo) ChildGroupExport {
	childExport := ChildGroupExport{
		Name:                group.Name,
		DisplayName:         group.DisplayName,
		Description:         group.Description,
		Enabled:             group.Enabled,
		ProxyKeys:           group.ProxyKeys,
		Sort:                group.Sort,
		TestModel:           group.TestModel,
		ModelMapping:        group.ModelMapping,
		ModelRedirectStrict: group.ModelRedirectStrict,
		Keys:                keys,
	}

	// Export JSON fields as raw JSON to preserve exact child group behavior.
	if group.ParamOverrides != nil {
		if data, err := json.Marshal(group.ParamOverrides); err == nil {
			childExport.ParamOverrides = data
		}
	}
	if group.Config != nil {
		if data, err := json.Marshal(group.Config); err == nil {
			childExport.Config = data
		}
	}
	if len(group.HeaderRules) > 0 {
		childExport.HeaderRules = json.RawMessage(group.HeaderRules)
	}
	if group.ModelRedirectRules != nil {
		if data, err := json.Marshal(group.ModelRedirectRules); err == nil {
			childExport.ModelRedirectRules = data
		}
	}
	if len(group.ModelRedirectRulesV2) > 0 {
		childExport.ModelRedirectRulesV2 = json.RawMessage(group.ModelRedirectRulesV2)
	}
	if len(group.CustomModelNames) > 0 {
		childExport.CustomModelNames = json.RawMessage(group.CustomModelNames)
	}
	if group.Preconditions != nil {
		if data, err := json.Marshal(group.Preconditions); err == nil {
			childExport.Preconditions = data
		}
	}
	if len(group.PathRedirects) > 0 {
		childExport.PathRedirects = json.RawMessage(group.PathRedirects)
	}

	return childExport
}

func (s *ImportExportService) importAggregateSubGroupRelations(tx *gorm.DB, groups []GroupExportData, importedGroups map[string]models.Group) (int, int) {
	imported := 0
	skipped := 0

	for _, groupData := range groups {
		if groupData.Group.GroupType != "aggregate" || len(groupData.SubGroups) == 0 {
			continue
		}

		aggregateGroup, ok := importedGroups[groupData.Group.Name]
		if !ok {
			if err := tx.Where("name = ?", groupData.Group.Name).First(&aggregateGroup).Error; err != nil {
				logrus.WithError(err).Warnf("Skipping sub-group relations for missing aggregate group %s", groupData.Group.Name)
				skipped += len(groupData.SubGroups)
				continue
			}
		}

		for _, subGroupInfo := range groupData.SubGroups {
			if err := validateSubGroupWeight(subGroupInfo.Weight); err != nil {
				logrus.WithError(err).Warnf("Skipping sub-group relation %s -> %s with invalid weight", aggregateGroup.Name, subGroupInfo.GroupName)
				skipped++
				continue
			}
			if err := validateMinEffectiveWeight(subGroupInfo.Weight, subGroupInfo.MinEffectiveWeight); err != nil {
				logrus.WithError(err).Warnf("Skipping sub-group relation %s -> %s with invalid minimum effective weight", aggregateGroup.Name, subGroupInfo.GroupName)
				skipped++
				continue
			}
			if err := validateHealthResetIntervalSeconds(subGroupInfo.HealthResetIntervalSeconds); err != nil {
				logrus.WithError(err).Warnf("Skipping sub-group relation %s -> %s with invalid health reset interval", aggregateGroup.Name, subGroupInfo.GroupName)
				skipped++
				continue
			}

			subGroup, ok := importedGroups[subGroupInfo.GroupName]
			if !ok {
				if err := tx.Where("name = ?", subGroupInfo.GroupName).First(&subGroup).Error; err != nil {
					logrus.WithError(err).Warnf("Skipping missing sub-group relation %s -> %s", aggregateGroup.Name, subGroupInfo.GroupName)
					skipped++
					continue
				}
			}

			relation := models.GroupSubGroup{
				GroupID:                    aggregateGroup.ID,
				SubGroupID:                 subGroup.ID,
				Weight:                     subGroupInfo.Weight,
				MinEffectiveWeight:         normalizeSubGroupMinEffectiveWeight(subGroupInfo.Weight, subGroupInfo.MinEffectiveWeight),
				HealthResetIntervalSeconds: subGroupInfo.HealthResetIntervalSeconds,
			}

			if err := tx.Where("group_id = ? AND sub_group_id = ?", aggregateGroup.ID, subGroup.ID).
				Assign(map[string]any{
					"weight":                        subGroupInfo.Weight,
					"min_effective_weight":          normalizeSubGroupMinEffectiveWeight(subGroupInfo.Weight, subGroupInfo.MinEffectiveWeight),
					"health_reset_interval_seconds": subGroupInfo.HealthResetIntervalSeconds,
				}).
				FirstOrCreate(&relation).Error; err != nil {
				logrus.WithError(err).Warnf("Failed to create sub-group relation %s -> %s", aggregateGroup.Name, subGroup.Name)
				skipped++
				continue
			}
			imported++
		}
	}

	return imported, skipped
}

func (s *ImportExportService) importDynamicWeights(tx *gorm.DB, metrics []DynamicWeightMetricExportInfo, importedGroups map[string]models.Group) (int, int) {
	imported := 0
	skipped := 0

	for _, item := range metrics {
		group, ok := importedGroups[item.GroupName]
		if !ok {
			if err := tx.Where("name = ?", item.GroupName).First(&group).Error; err != nil {
				logrus.WithError(err).Warnf("Skipping dynamic weight metric for missing group %s", item.GroupName)
				skipped++
				continue
			}
		}

		subGroupID := uint(0)
		if item.MetricType == models.MetricTypeSubGroup {
			subGroup, ok := importedGroups[item.SubGroupName]
			if !ok {
				if err := tx.Where("name = ?", item.SubGroupName).First(&subGroup).Error; err != nil {
					logrus.WithError(err).Warnf("Skipping dynamic weight metric for missing sub-group %s", item.SubGroupName)
					skipped++
					continue
				}
			}
			subGroupID = subGroup.ID
		}

		metric := models.DynamicWeightMetric{
			MetricType:            item.MetricType,
			GroupID:               group.ID,
			SubGroupID:            subGroupID,
			SourceModel:           item.SourceModel,
			TargetModel:           item.TargetModel,
			ConsecutiveFailures:   item.ConsecutiveFailures,
			LastFailureAt:         item.LastFailureAt,
			LastSuccessAt:         item.LastSuccessAt,
			ConsecutiveRateLimits: item.ConsecutiveRateLimits,
			LastRateLimitAt:       item.LastRateLimitAt,
			Requests7d:            item.Requests7d,
			Successes7d:           item.Successes7d,
			RateLimits7d:          item.RateLimits7d,
			Requests14d:           item.Requests14d,
			Successes14d:          item.Successes14d,
			RateLimits14d:         item.RateLimits14d,
			Requests30d:           item.Requests30d,
			Successes30d:          item.Successes30d,
			RateLimits30d:         item.RateLimits30d,
			Requests90d:           item.Requests90d,
			Successes90d:          item.Successes90d,
			RateLimits90d:         item.RateLimits90d,
			Requests180d:          item.Requests180d,
			Successes180d:         item.Successes180d,
			RateLimits180d:        item.RateLimits180d,
			LastRolloverAt:        item.LastRolloverAt,
			UpdatedAt:             item.UpdatedAt,
			DeletedAt:             item.DeletedAt,
		}

		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "metric_type"},
				{Name: "group_id"},
				{Name: "sub_group_id"},
				{Name: "source_model"},
				{Name: "target_model"},
			},
			UpdateAll: true,
		}).Create(&metric).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to import dynamic weight metric for group %s", item.GroupName)
			skipped++
			continue
		}
		imported++
	}

	return imported, skipped
}

type legacyUpstreamInfo struct {
	URL string `json:"url"`
}

func normalizeLegacyChildGroups(groups []GroupExportData) []GroupExportData {
	if len(groups) == 0 || hasExplicitChildGroupMetadata(groups) {
		return groups
	}

	nameToIndex := make(map[string]int, len(groups))
	for i := range groups {
		nameToIndex[groups[i].Group.Name] = i
	}

	childToParent := make(map[int]int)
	for i := range groups {
		if parentName := legacyProxyParentName(groups[i]); parentName != "" {
			if parentIndex, ok := nameToIndex[parentName]; ok && parentIndex != i && canUseLegacyChildParent(groups[parentIndex], groups[i]) {
				childToParent[i] = parentIndex
			}
		}
	}

	for childIndex := range groups {
		if _, exists := childToParent[childIndex]; exists || groups[childIndex].Group.GroupType != "standard" {
			continue
		}
		parentIndex := findLegacyNameBasedParent(groups, childIndex)
		if parentIndex >= 0 {
			childToParent[childIndex] = parentIndex
		}
	}

	if len(childToParent) == 0 {
		return groups
	}

	normalized := make([]GroupExportData, len(groups))
	copy(normalized, groups)
	for childIndex, parentIndex := range childToParent {
		child := groups[childIndex]
		parent := &normalized[parentIndex]
		parent.ChildGroups = append(parent.ChildGroups, childGroupExportFromLegacyGroup(child))
	}

	result := make([]GroupExportData, 0, len(groups)-len(childToParent))
	for i := range normalized {
		if _, isChild := childToParent[i]; isChild {
			continue
		}
		result = append(result, normalized[i])
	}

	logrus.Infof("Inferred %d legacy child group relation(s) during system import", len(childToParent))
	return result
}

func hasExplicitChildGroupMetadata(groups []GroupExportData) bool {
	for _, group := range groups {
		if len(group.ChildGroups) > 0 || group.Group.ParentGroupID != nil {
			return true
		}
	}
	return false
}

func findLegacyNameBasedParent(groups []GroupExportData, childIndex int) int {
	child := groups[childIndex]
	bestIndex := -1
	bestNameLen := 0
	for parentIndex := range groups {
		if parentIndex == childIndex || !canUseLegacyChildParent(groups[parentIndex], child) {
			continue
		}
		if !isLegacyNameBasedChild(groups[parentIndex].Group, child.Group) {
			continue
		}
		if nameLen := len(groups[parentIndex].Group.Name); nameLen > bestNameLen {
			bestIndex = parentIndex
			bestNameLen = nameLen
		}
	}
	return bestIndex
}

func canUseLegacyChildParent(parent GroupExportData, child GroupExportData) bool {
	return parent.Group.GroupType == "standard" &&
		child.Group.GroupType == "standard" &&
		parent.Group.Name != "" &&
		child.Group.Name != "" &&
		parent.Group.ChannelType == child.Group.ChannelType &&
		legacyUpstreamSignature(parent.Group.Upstreams) == legacyUpstreamSignature(child.Group.Upstreams)
}

func isLegacyNameBasedChild(parent models.Group, child models.Group) bool {
	if strings.HasPrefix(child.Name, parent.Name+"_") {
		return true
	}
	if !isLegacyDefaultParent(parent) {
		return false
	}
	parentPrefix := legacyNamePrefix(parent.Name)
	return parentPrefix != "" && parentPrefix == legacyNamePrefix(child.Name)
}

func isLegacyDefaultParent(group models.Group) bool {
	name := strings.ToLower(group.Name)
	return strings.HasSuffix(name, "_default") ||
		strings.HasSuffix(name, "_defalut") ||
		strings.HasSuffix(name, "_main") ||
		strings.HasSuffix(name, "_base") ||
		strings.Contains(group.DisplayName, "默认")
}

func legacyNamePrefix(name string) string {
	index := strings.LastIndex(name, "_")
	if index <= 0 {
		return ""
	}
	return name[:index]
}

func legacyProxyParentName(group GroupExportData) string {
	for _, upstream := range legacyUpstreamURLs(group.Group.Upstreams) {
		if index := strings.Index(upstream, "/proxy/"); index >= 0 {
			parentName := upstream[index+len("/proxy/"):]
			if cut := strings.IndexAny(parentName, "?#"); cut >= 0 {
				parentName = parentName[:cut]
			}
			if decoded, err := url.PathUnescape(parentName); err == nil {
				parentName = decoded
			}
			return strings.TrimSpace(parentName)
		}
	}
	return ""
}

func legacyUpstreamSignature(upstreams []byte) string {
	urls := legacyUpstreamURLs(upstreams)
	if len(urls) == 0 {
		return ""
	}
	return strings.Join(urls, "\n")
}

func legacyUpstreamURLs(upstreams []byte) []string {
	if len(upstreams) == 0 {
		return nil
	}
	var parsed []legacyUpstreamInfo
	if err := json.Unmarshal(upstreams, &parsed); err != nil {
		return nil
	}
	urls := make([]string, 0, len(parsed))
	for _, upstream := range parsed {
		trimmed := strings.TrimSpace(upstream.URL)
		if trimmed != "" {
			urls = append(urls, trimmed)
		}
	}
	return urls
}

func childGroupExportFromLegacyGroup(groupData GroupExportData) ChildGroupExport {
	child := buildChildGroupExport(groupData.Group, groupData.Keys)
	return child
}

// ImportSystem imports the entire system configuration
func (s *ImportExportService) ImportSystem(tx *gorm.DB, data *SystemExportData) error {
	if data.ManagedSites != nil {
		if err := ValidateManagedSiteAutoCheckinConfig(data.ManagedSites.AutoCheckin); err != nil {
			return err
		}
		if err := ValidateManagedSiteAutoBalanceConfig(data.ManagedSites.AutoBalance); err != nil {
			return err
		}
	}

	// Count settings to import for logging
	settingsCount := len(data.SystemSettings)
	groupsToImport := normalizeLegacyChildGroups(data.Groups)
	groupsCount := len(groupsToImport)

	logrus.Infof("Starting system import: %d settings, %d groups", settingsCount, groupsCount)

	// Import system settings - ensure they are properly updated and cleaned
	updatedSettings := 0
	createdSettings := 0

	for key, value := range data.SystemSettings {
		// Clean the value to remove leading/trailing whitespace
		// This fixes issues like ' http://...' which cause URL parsing errors
		cleanedValue := strings.TrimSpace(value)

		var setting models.SystemSetting
		if err := tx.Where("setting_key = ?", key).First(&setting).Error; err == nil {
			// Update existing setting
			// Use Updates instead of Update to ensure all fields are updated
			if err := tx.Model(&setting).Updates(map[string]interface{}{
				"setting_value": cleanedValue,
				"updated_at":    time.Now(),
			}).Error; err != nil {
				return fmt.Errorf("failed to update setting %s: %w", key, err)
			}
			updatedSettings++
			logrus.Debugf("Updated setting: %s", key)
		} else {
			// Create new setting
			setting = models.SystemSetting{
				SettingKey:   key,
				SettingValue: cleanedValue,
			}
			if err := tx.Create(&setting).Error; err != nil {
				return fmt.Errorf("failed to create setting %s: %w", key, err)
			}
			createdSettings++
			logrus.Debugf("Created new setting: %s", key)
		}
	}

	logrus.Infof("System settings imported: %d updated, %d created", updatedSettings, createdSettings)

	// Import all groups with unique name handling
	importedGroups := 0
	importedGroupsByOriginalName := make(map[string]models.Group, len(data.Groups))
	for _, groupData := range groupsToImport {
		var importedGroup *models.Group
		var importedChildGroups map[string]models.Group
		err := tx.Transaction(func(groupTx *gorm.DB) error {
			var importErr error
			importedGroup, importedChildGroups, importErr = s.importGroup(groupTx, &groupData, nil, importGroupOptions{
				ImportAggregateSubGroups: false,
			})
			return importErr
		})
		if err != nil {
			// Nested transactions keep the best-effort import behavior without committing half-created groups.
			logrus.WithError(err).Warnf("Failed to import group %s, skipping", groupData.Group.Name)
			continue
		}
		importedGroups++
		importedGroupsByOriginalName[groupData.Group.Name] = *importedGroup
		for originalName, importedChildGroup := range importedChildGroups {
			importedGroupsByOriginalName[originalName] = importedChildGroup
		}
		logrus.Debugf("Imported group %s with ID %d", groupData.Group.Name, importedGroup.ID)
	}

	logrus.Infof("Groups imported: %d/%d successful", importedGroups, groupsCount)

	importedRelations, skippedRelations := s.importAggregateSubGroupRelations(tx, groupsToImport, importedGroupsByOriginalName)
	if importedRelations > 0 || skippedRelations > 0 {
		logrus.Infof("Aggregate sub-group relations imported: %d created, %d skipped", importedRelations, skippedRelations)
	}

	if len(data.DynamicWeights) > 0 {
		importedMetrics, skippedMetrics := s.importDynamicWeights(tx, data.DynamicWeights, importedGroupsByOriginalName)
		logrus.Infof("Dynamic weight metrics imported: %d upserted, %d skipped", importedMetrics, skippedMetrics)
	}

	// Import managed sites if present
	if data.ManagedSites != nil {
		imported, skipped, err := s.importManagedSites(tx, data.ManagedSites)
		if err != nil {
			return fmt.Errorf("failed to import managed-site configuration: %w", err)
		}
		logrus.Infof("Managed sites imported: %d imported, %d skipped", imported, skipped)
	}

	// Import Hub access keys if present
	if len(data.HubAccessKeys) > 0 {
		imported, skipped := s.importHubAccessKeys(tx, data.HubAccessKeys)
		logrus.Infof("Hub access keys imported: %d imported, %d skipped", imported, skipped)
	}

	// Note: Cache refresh should be handled by the handler after transaction commits
	// This ensures the database changes are visible when the cache is refreshed

	return nil
}

// importManagedSites imports managed sites from export data
func (s *ImportExportService) importManagedSites(tx *gorm.DB, data *ManagedSitesExportData) (int, int, error) {
	if data == nil {
		return 0, 0, nil
	}

	// Site rows and schedule settings are independent parts of the export.
	canImportSites := len(data.Sites) == 0 || tx.Migrator().HasTable(&managedSiteModel{})
	if !canImportSites {
		logrus.Warn("managed_sites table does not exist, skipping import")
	}

	imported := 0
	skipped := 0
	sitesToImport := data.Sites
	if !canImportSites {
		skipped = len(data.Sites)
		sitesToImport = nil
	}

	for _, siteInfo := range sitesToImport {
		name := strings.TrimSpace(siteInfo.Name)
		if name == "" {
			skipped++
			continue
		}

		baseURL := strings.TrimSpace(siteInfo.BaseURL)
		if baseURL == "" {
			skipped++
			continue
		}

		siteType := strings.TrimSpace(siteInfo.SiteType)
		if siteType == "" {
			siteType = "unknown"
		}

		authType := strings.TrimSpace(siteInfo.AuthType)
		if authType == "" {
			authType = "none"
		}

		// Validate encrypted auth value if present
		authValue := siteInfo.AuthValue
		if authValue != "" && authType != "none" {
			// Verify it can be decrypted
			if _, err := s.encryptionService.Decrypt(authValue); err != nil {
				logrus.WithError(err).Warnf("Failed to decrypt auth value for site %s, skipping", name)
				skipped++
				continue
			}
		}

		// Ensure checkin flags are consistent
		checkInEnabled := siteInfo.CheckInEnabled
		autoCheckInEnabled := siteInfo.AutoCheckInEnabled
		if autoCheckInEnabled {
			checkInEnabled = true
		}
		if !checkInEnabled {
			autoCheckInEnabled = false
		}

		// Generate unique site name if conflict exists
		uniqueName, err := s.generateUniqueSiteName(tx, name)
		if err != nil {
			logrus.WithError(err).Warnf("Failed to generate unique name for site %s", name)
			skipped++
			continue
		}

		site := &managedSiteModel{
			Name:               uniqueName,
			Notes:              strings.TrimSpace(siteInfo.Notes),
			Description:        strings.TrimSpace(siteInfo.Description),
			Sort:               siteInfo.Sort,
			Enabled:            siteInfo.Enabled,
			BaseURL:            baseURL,
			SiteType:           siteType,
			UserID:             strings.TrimSpace(siteInfo.UserID),
			CheckInPageURL:     strings.TrimSpace(siteInfo.CheckInPageURL),
			CheckInAvailable:   siteInfo.CheckInAvailable,
			CheckInEnabled:     checkInEnabled,
			AutoCheckInEnabled: autoCheckInEnabled,
			CustomCheckInURL:   strings.TrimSpace(siteInfo.CustomCheckInURL),
			AuthType:           authType,
			AuthValue:          authValue,
		}

		if err := tx.Create(site).Error; err != nil {
			// Return database errors so the outer transaction rolls back consistently across drivers.
			return imported, skipped, fmt.Errorf("failed to create managed site %q: %w", uniqueName, err)
		}

		if uniqueName != name {
			logrus.Infof("Imported site %s (renamed from %s)", uniqueName, name)
		}
		imported++
	}

	// Import managed-site schedule config if present.
	// Note: Using First/Create/Save pattern instead of FirstOrCreate+Assign because:
	// 1. This is a singleton config (ID=1) with no concurrent import scenarios
	// 2. Already protected by transaction isolation
	// 3. FirstOrCreate behavior varies across databases (not atomic on SQLite)
	// 4. Current pattern is clearer and has equivalent performance
	if data.AutoCheckin != nil || data.AutoBalance != nil {
		var setting managedSiteSettingModel
		err := tx.First(&setting, 1).Error
		create := errors.Is(err, gorm.ErrRecordNotFound)
		if create {
			setting = managedSiteSettingModel{
				ID:                          1,
				AutoBalanceEnabled:          true,
				BalanceRefreshIntervalHours: defaultManagedSiteAutoBalanceIntervalHours,
				ScheduleTimes:               "09:00",
				WindowStart:                 "09:00",
				WindowEnd:                   "18:00",
				ScheduleMode:                "multiple",
				RetryIntervalMinutes:        60,
				RetryMaxAttemptsPerDay:      2,
			}
		} else if err != nil {
			return imported, skipped, fmt.Errorf("failed to load managed-site schedule config: %w", err)
		}

		if data.AutoCheckin != nil {
			setting.AutoCheckinEnabled = data.AutoCheckin.GlobalEnabled
			if len(data.AutoCheckin.ScheduleTimes) > 0 {
				setting.ScheduleTimes = joinManagedSiteScheduleTimes(data.AutoCheckin.ScheduleTimes)
			}
			setting.WindowStart = data.AutoCheckin.WindowStart
			setting.WindowEnd = data.AutoCheckin.WindowEnd
			setting.ScheduleMode = strings.TrimSpace(data.AutoCheckin.ScheduleMode)
			if setting.ScheduleMode == "" {
				setting.ScheduleMode = "multiple"
			}
			setting.DeterministicTime = data.AutoCheckin.DeterministicTime
			setting.RetryEnabled = data.AutoCheckin.RetryStrategy.Enabled
			setting.RetryIntervalMinutes = min(max(data.AutoCheckin.RetryStrategy.IntervalMinutes, 1), 24*60)
			setting.RetryMaxAttemptsPerDay = min(max(data.AutoCheckin.RetryStrategy.MaxAttemptsPerDay, 1), 10)
		}
		if data.AutoBalance != nil {
			setting.AutoBalanceEnabled = data.AutoBalance.GlobalEnabled
			setting.BalanceRefreshIntervalHours = data.AutoBalance.IntervalHours
		}

		if create {
			err = tx.Create(&setting).Error
		} else {
			updates := make(map[string]any, 11)
			if data.AutoCheckin != nil {
				updates["auto_checkin_enabled"] = setting.AutoCheckinEnabled
				// Missing schedule_times identifies older backups and preserves the existing/default value.
				if len(data.AutoCheckin.ScheduleTimes) > 0 {
					updates["schedule_times"] = setting.ScheduleTimes
				}
				updates["window_start"] = setting.WindowStart
				updates["window_end"] = setting.WindowEnd
				updates["schedule_mode"] = setting.ScheduleMode
				updates["deterministic_time"] = setting.DeterministicTime
				updates["retry_enabled"] = setting.RetryEnabled
				updates["retry_interval_minutes"] = setting.RetryIntervalMinutes
				updates["retry_max_attempts_per_day"] = setting.RetryMaxAttemptsPerDay
			}
			if data.AutoBalance != nil {
				updates["auto_balance_enabled"] = setting.AutoBalanceEnabled
				updates["balance_refresh_interval_hours"] = setting.BalanceRefreshIntervalHours
			}
			err = tx.Model(&managedSiteSettingModel{}).Where("id = ?", setting.ID).Updates(updates).Error
		}
		if err != nil {
			return imported, skipped, fmt.Errorf("failed to persist managed-site schedule config: %w", err)
		}
	}

	return imported, skipped, nil
}

// generateUniqueSiteName generates a unique site name by appending a random suffix if needed.
//
// Note: This logic is similar to GenerateUniqueGroupName but intentionally kept separate:
// 1. Maintains clear module boundaries between group and site management
// 2. Avoids introducing generic table/field parameters which would add complexity
// 3. Each function operates on its own model type with type safety
// 4. Code duplication is minimal (~30 lines) and maintenance cost is acceptable
func (s *ImportExportService) generateUniqueSiteName(tx *gorm.DB, baseName string) (string, error) {
	siteName := baseName
	maxAttempts := 10

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Check if this name already exists
		var count int64
		if err := tx.Model(&managedSiteModel{}).Where("name = ?", siteName).Count(&count).Error; err != nil {
			return "", fmt.Errorf("failed to check site name: %w", err)
		}

		// If name is unique, we're done
		if count == 0 {
			if siteName != baseName {
				logrus.Debugf("Generated unique site name: %s (original: %s)", siteName, baseName)
			}
			return siteName, nil
		}

		// Generate a new name with random suffix for next attempt
		if attempt < maxAttempts-1 {
			// Ensure the name doesn't exceed database limits
			if len(baseName)+4 > 100 {
				baseName = baseName[:96]
			}
			// Append random suffix using shared utility (4 chars)
			siteName = baseName + utils.GenerateRandomSuffix()
		} else {
			return "", fmt.Errorf("failed to generate unique site name for %s after %d attempts", baseName, maxAttempts)
		}
	}

	return siteName, nil
}

// hubAccessKeyModel represents the database model for Hub access keys (minimal for export/import).
// Note: This is intentionally separate from centralizedmgmt.HubAccessKey to:
// 1. Avoid circular dependency between services and centralizedmgmt packages
// 2. Keep export/import logic self-contained with only the fields needed
type hubAccessKeyModel struct {
	ID            uint   `gorm:"primaryKey"`
	Name          string `gorm:"column:name"`
	KeyHash       string `gorm:"column:key_hash"`
	KeyValue      string `gorm:"column:key_value"`
	AllowedModels []byte `gorm:"column:allowed_models"`
	Enabled       bool   `gorm:"column:enabled"`
}

func (hubAccessKeyModel) TableName() string {
	return "hub_access_keys"
}

// exportHubAccessKeys exports all Hub access keys for system backup.
// Key values remain encrypted (same as database storage) for security.
func (s *ImportExportService) exportHubAccessKeys() []HubAccessKeyExportInfo {
	// Check if hub_access_keys table exists
	if !s.db.Migrator().HasTable(&hubAccessKeyModel{}) {
		return nil
	}

	var keys []hubAccessKeyModel
	if err := s.db.Order("id ASC").Find(&keys).Error; err != nil {
		logrus.WithError(err).Warn("Failed to export Hub access keys")
		return nil
	}

	if len(keys) == 0 {
		return nil
	}

	exports := make([]HubAccessKeyExportInfo, 0, len(keys))
	for _, key := range keys {
		// Parse allowed models from JSON
		var allowedModels []string
		if err := json.Unmarshal(key.AllowedModels, &allowedModels); err != nil {
			allowedModels = []string{}
		}

		exports = append(exports, HubAccessKeyExportInfo{
			Name:          key.Name,
			KeyValue:      key.KeyValue, // Keep encrypted
			AllowedModels: allowedModels,
			Enabled:       key.Enabled,
		})
	}

	logrus.Infof("Exported %d Hub access keys", len(exports))
	return exports
}

// importHubAccessKeys imports Hub access keys from export data.
// Validates decryption before import and generates unique names for conflicts.
func (s *ImportExportService) importHubAccessKeys(tx *gorm.DB, keys []HubAccessKeyExportInfo) (int, int) {
	if len(keys) == 0 {
		return 0, 0
	}

	// Check if hub_access_keys table exists
	if !tx.Migrator().HasTable(&hubAccessKeyModel{}) {
		logrus.Warn("hub_access_keys table does not exist, skipping import")
		return 0, len(keys)
	}

	imported := 0
	skipped := 0

	for _, keyInfo := range keys {
		name := strings.TrimSpace(keyInfo.Name)
		if name == "" {
			skipped++
			continue
		}

		// Validate encrypted key value can be decrypted
		if keyInfo.KeyValue == "" {
			skipped++
			continue
		}

		decryptedKey, err := s.encryptionService.Decrypt(keyInfo.KeyValue)
		if err != nil {
			// Skip keys with decryption errors (different ENCRYPTION_KEY)
			logrus.WithError(err).Warnf("Failed to decrypt Hub access key %s, skipping", name)
			skipped++
			continue
		}

		// Generate hash for the decrypted key
		keyHash := s.encryptionService.Hash(decryptedKey)

		// Check if key value already exists (by hash)
		var existingCount int64
		if err := tx.Model(&hubAccessKeyModel{}).Where("key_hash = ?", keyHash).Count(&existingCount).Error; err != nil {
			skipped++
			continue
		}
		if existingCount > 0 {
			// Key value already exists, skip
			logrus.Debugf("Hub access key %s already exists (by hash), skipping", name)
			skipped++
			continue
		}

		// Generate unique name if conflict exists
		uniqueName, err := s.generateUniqueHubAccessKeyName(tx, name)
		if err != nil {
			logrus.WithError(err).Warnf("Failed to generate unique name for Hub access key %s", name)
			skipped++
			continue
		}

		// Serialize allowed models to JSON
		allowedModelsJSON, err := json.Marshal(keyInfo.AllowedModels)
		if err != nil {
			allowedModelsJSON = []byte("[]")
		}

		// Create the key with the encrypted value from export
		key := &hubAccessKeyModel{
			Name:          uniqueName,
			KeyHash:       keyHash,
			KeyValue:      keyInfo.KeyValue, // Keep the encrypted value
			AllowedModels: allowedModelsJSON,
			Enabled:       keyInfo.Enabled,
		}

		if err := tx.Create(key).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to create Hub access key %s", uniqueName)
			skipped++
			continue
		}

		if uniqueName != name {
			logrus.Infof("Imported Hub access key %s (renamed from %s)", uniqueName, name)
		}
		imported++
	}

	return imported, skipped
}

// generateUniqueHubAccessKeyName generates a unique Hub access key name by appending a random suffix if needed.
func (s *ImportExportService) generateUniqueHubAccessKeyName(tx *gorm.DB, baseName string) (string, error) {
	name := baseName
	maxAttempts := 10

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var count int64
		if err := tx.Model(&hubAccessKeyModel{}).Where("name = ?", name).Count(&count).Error; err != nil {
			return "", fmt.Errorf("failed to check Hub access key name: %w", err)
		}

		if count == 0 {
			return name, nil
		}

		// Generate new name with random suffix
		if attempt < maxAttempts-1 {
			if len(baseName)+4 > 100 {
				baseName = baseName[:96]
			}
			name = baseName + utils.GenerateRandomSuffix()
		} else {
			return "", fmt.Errorf("failed to generate unique Hub access key name for %s after %d attempts", baseName, maxAttempts)
		}
	}

	return name, nil
}

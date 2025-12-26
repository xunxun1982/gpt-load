package services

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
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

// ExportKeysForGroup exports all keys for a specific group
// This method fixes the FindInBatches limitation by using manual offset pagination
func (s *ImportExportService) ExportKeysForGroup(groupID uint) (*ExportKeysResult, error) {
	var allKeys []KeyExportInfo
	offset := 0
	const batchSize = 2000
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
			KeyValue string
			Status   string
		}

		// Use Limit and Offset instead of FindInBatches to avoid its limitations
		// FindInBatches has known issues with primary key pagination
		err := s.db.Model(&models.APIKey{}).
			Select("key_value, status").
			Where("group_id = ?", groupID).
			Limit(batchSize).
			Offset(offset).
			Find(&batchKeys).Error

		if err != nil {
			return nil, fmt.Errorf("failed to export keys batch at offset %d: %w", offset, err)
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

		totalExported += len(batchKeys)

		// Only log progress at 25%, 50%, 75% intervals for large exports
		if totalCount > 10000 && totalExported > 0 {
			currentPercent := (totalExported * 100) / int(totalCount)
			if currentPercent >= lastLoggedPercent+25 {
				logrus.Infof("Export progress: %d%% (%d/%d keys)", currentPercent, totalExported, totalCount)
				lastLoggedPercent = currentPercent
			}
		}

		// Debug level logging for detailed progress
		logrus.Debugf("Exported batch: %d keys at offset %d (total: %d)",
			len(batchKeys), offset, totalExported)

		// If we got less than batchSize, we've reached the end
		if len(batchKeys) < batchSize {
			break
		}

		offset += batchSize
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
	mu := sync.Mutex{}
	totalExported := 0

	// Get total count for progress tracking
	var totalCount int64
	if err := s.db.Model(&models.APIKey{}).Where("group_id IN ?", groupIDs).Count(&totalCount).Error; err != nil {
		logrus.WithError(err).Warn("Failed to get total count for groups")
	}

	// Process all groups' keys in batches
	offset := 0
	const batchSize = 5000 // Larger batch for multiple groups

	// Log start with expected count
	if totalCount > 0 {
		logrus.Infof("Exporting %d keys from %d groups", totalCount, len(groupIDs))
	} else {
		logrus.Debugf("Starting key export for %d groups", len(groupIDs))
	}

	lastLoggedPercent := 0

	for {
		var batchKeys []struct {
			GroupID  uint
			KeyValue string
			Status   string
		}

		// Query keys for all groups
		err := s.db.Model(&models.APIKey{}).
			Select("group_id, key_value, status").
			Where("group_id IN ?", groupIDs).
			Limit(batchSize).
			Offset(offset).
			Find(&batchKeys).Error

		if err != nil {
			return nil, fmt.Errorf("failed to export keys batch at offset %d: %w", offset, err)
		}

		// If no more records, we're done
		if len(batchKeys) == 0 {
			break
		}

		// Group keys by group ID
		mu.Lock()
		for _, key := range batchKeys {
			if _, exists := result[key.GroupID]; !exists {
				result[key.GroupID] = []KeyExportInfo{}
			}
			result[key.GroupID] = append(result[key.GroupID], KeyExportInfo{
				KeyValue: key.KeyValue,
				Status:   key.Status,
			})
		}
		totalExported += len(batchKeys)
		mu.Unlock()

		// Only log progress at 25%, 50%, 75% intervals for large exports
		if totalCount > 10000 && totalExported > 0 {
			currentPercent := (totalExported * 100) / int(totalCount)
			if currentPercent >= lastLoggedPercent+25 {
				logrus.Infof("System export progress: %d%% (%d/%d keys)", currentPercent, totalExported, totalCount)
				lastLoggedPercent = currentPercent
			}
		}

		// Debug level logging for detailed progress
		logrus.Debugf("Exported batch: %d keys at offset %d (total: %d)",
			len(batchKeys), offset, totalExported)

		// If we got less than batchSize, we've reached the end
		if len(batchKeys) < batchSize {
			break
		}

		offset += batchSize
	}

	logrus.Infof("System export completed: %d keys from %d groups", totalExported, len(groupIDs))

	return result, nil
}

// ImportKeys imports keys for a group using the bulk import service
func (s *ImportExportService) ImportKeys(tx *gorm.DB, groupID uint, keys []KeyExportInfo) error {
	if len(keys) == 0 {
		return nil
	}

	keyModels := make([]models.APIKey, 0, len(keys))
	skippedKeys := 0

	for _, keyInfo := range keys {
		// Decrypt key_value to calculate key_hash
		decryptedKey, err := s.encryptionService.Decrypt(keyInfo.KeyValue)
		if err != nil {
			logrus.WithError(err).Debug("Failed to decrypt key during import, skipping")
			skippedKeys++
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
			KeyValue:     keyInfo.KeyValue,           // Keep encrypted value
			KeyHash:      keyHash,                    // Calculated hash
			Status:       models.KeyStatusActive,     // Always start as active
			FailureCount: 0,                          // Always reset to 0 for fresh start
		})
	}

	if len(keyModels) > 0 {
		logrus.Infof("Importing %d keys for group ID: %d", len(keyModels), groupID)
		if skippedKeys > 0 {
			logrus.Warnf("Skipped %d keys due to decryption errors", skippedKeys)
		}

		// Use the bulk import service with the provided transaction
		if err := s.bulkImportService.BulkInsertAPIKeysWithTx(tx, keyModels); err != nil {
			return fmt.Errorf("bulk import failed: %w", err)
		}
	} else if skippedKeys > 0 {
		logrus.Warnf("All %d keys were skipped due to decryption errors", skippedKeys)
	}

	return nil
}

// ExportGroupData exports a complete group with all its data
type GroupExportData struct {
	Group       models.Group         `json:"group"`
	Keys        []KeyExportInfo      `json:"keys"`
	SubGroups   []SubGroupInfo       `json:"sub_groups,omitempty"`
	ChildGroups []ChildGroupExport   `json:"child_groups,omitempty"` // Child groups for standard groups
}

// ChildGroupExport represents exported child group data
type ChildGroupExport struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	Enabled     bool            `json:"enabled"`
	ProxyKeys   string          `json:"proxy_keys"`
	Sort        int             `json:"sort"`
	Keys        []KeyExportInfo `json:"keys"`
}

// SubGroupInfo represents sub-group relationship
type SubGroupInfo struct {
	GroupName string `json:"group_name"`
	Weight    int    `json:"weight"`
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
			for _, rel := range subGroupRelations {
				subGroupIDs = append(subGroupIDs, rel.SubGroupID)
			}

			// Get sub-group details
			var subGroups []models.Group
			if err := s.db.Where("id IN ?", subGroupIDs).Find(&subGroups).Error; err == nil {
				result.SubGroups = make([]SubGroupInfo, 0, len(subGroups))
				for _, sg := range subGroups {
					// Find the weight for this sub-group
					weight := 0
					for _, rel := range subGroupRelations {
						if rel.SubGroupID == sg.ID {
							weight = rel.Weight
							break
						}
					}
					result.SubGroups = append(result.SubGroups, SubGroupInfo{
						GroupName: sg.Name,
						Weight:    weight,
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
				childExport := ChildGroupExport{
					Name:        cg.Name,
					DisplayName: cg.DisplayName,
					Description: cg.Description,
					Enabled:     cg.Enabled,
					ProxyKeys:   cg.ProxyKeys,
					Sort:        cg.Sort,
					Keys:        childKeysMap[cg.ID],
				}
				result.ChildGroups = append(result.ChildGroups, childExport)
			}

			logrus.Infof("Exported %d child groups for parent group %s", len(childGroups), group.Name)
		}
	}

	return result, nil
}

// ImportGroup imports a complete group with keys and sub-groups
func (s *ImportExportService) ImportGroup(tx *gorm.DB, data *GroupExportData) (uint, error) {
	// Child groups cannot be imported individually - they are imported with their parent
	if data.Group.ParentGroupID != nil {
		return 0, ErrChildGroupCannotImportIndividually
	}

	// Use the centralized unique name generation function
	groupName, err := s.GenerateUniqueGroupName(tx, data.Group.Name)
	if err != nil {
		return 0, err
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
		return 0, err
	}

	if err := tx.Create(&newGroup).Error; err != nil {
		return 0, fmt.Errorf("failed to create group: %w", err)
	}

	if groupName != data.Group.Name {
		logrus.Infof("Imported group %s (renamed from %s) with ID %d", groupName, data.Group.Name, newGroup.ID)
	} else {
		logrus.Debugf("Imported group %s with ID %d", groupName, newGroup.ID)
	}

	// Import keys
	if len(data.Keys) > 0 {
		if err := s.ImportKeys(tx, newGroup.ID, data.Keys); err != nil {
			return 0, fmt.Errorf("failed to import keys: %w", err)
		}
	}

	// Import sub-groups for aggregate groups
	if newGroup.GroupType == "aggregate" && len(data.SubGroups) > 0 {
		// Find sub-group IDs
		var groupNames []string
		for _, sg := range data.SubGroups {
			groupNames = append(groupNames, sg.GroupName)
		}

		var subGroups []models.Group
		if err := tx.Where("name IN ?", groupNames).Find(&subGroups).Error; err == nil {
			// Create relationships
			for _, subGroup := range subGroups {
				// Find the weight for this sub-group
				weight := 0
				for _, sg := range data.SubGroups {
					if sg.GroupName == subGroup.Name {
						weight = sg.Weight
						break
					}
				}

				relation := models.GroupSubGroup{
					GroupID:    newGroup.ID,
					SubGroupID: subGroup.ID,
					Weight:     weight,
				}

				if err := tx.Create(&relation).Error; err != nil {
					logrus.WithError(err).Warnf("Failed to create sub-group relation for %s", subGroup.Name)
				}
			}
		}
	}

	// Import child groups for standard groups
	if newGroup.GroupType == "standard" && len(data.ChildGroups) > 0 {
		if err := s.importChildGroups(tx, newGroup.ID, newGroup.Name, newGroup.ChannelType, data.ChildGroups); err != nil {
			return 0, fmt.Errorf("failed to import child groups: %w", err)
		}
	}

	return newGroup.ID, nil
}

// importChildGroups imports child groups for a parent group
func (s *ImportExportService) importChildGroups(tx *gorm.DB, parentGroupID uint, parentName string, parentChannelType string, childGroups []ChildGroupExport) error {
	for _, childData := range childGroups {
		// Each child group should independently check for name conflicts
		// Try the original name, and add a random suffix if there's a conflict
		childName, err := s.GenerateUniqueGroupName(tx, childData.Name)
		if err != nil {
			logrus.WithError(err).Warnf("Failed to generate unique name for child group %s", childData.Name)
			continue
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

		// Create child group with inherited channel_type from parent
		childGroup := models.Group{
			Name:          childName,
			DisplayName:   childData.DisplayName,
			Description:   childData.Description,
			GroupType:     "standard",
			ChannelType:   parentChannelType, // Inherit from parent
			Enabled:       childData.Enabled,
			ParentGroupID: &parentGroupID,
			ProxyKeys:     childData.ProxyKeys,
			Sort:          childData.Sort,
			Upstreams:     []byte(upstreamsJSON),
		}

		// Apply suffix to display name if a suffix was added to the name
		if childGroup.DisplayName != "" && childNameSuffix != "" {
			childGroup.DisplayName = childData.DisplayName + childNameSuffix
		}

		if err := tx.Create(&childGroup).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to create child group %s", childName)
			continue
		}

		// Import keys for child group
		if len(childData.Keys) > 0 {
			if err := s.ImportKeys(tx, childGroup.ID, childData.Keys); err != nil {
				logrus.WithError(err).Warnf("Failed to import keys for child group %s", childName)
			}
		}

		logrus.Infof("Imported child group %s (ID: %d) for parent %s", childName, childGroup.ID, parentName)
	}

	return nil
}

// SystemExportData represents full system export
type SystemExportData struct {
	Version        string                    `json:"version"`
	ExportedAt     string                    `json:"exported_at"`
	SystemSettings map[string]string         `json:"system_settings"`
	Groups         []GroupExportData         `json:"groups"`
	ManagedSites   *ManagedSitesExportData   `json:"managed_sites,omitempty"`
}

// ManagedSitesExportData represents exported managed sites data
type ManagedSitesExportData struct {
	AutoCheckin *ManagedSiteAutoCheckinConfig `json:"auto_checkin,omitempty"`
	Sites       []ManagedSiteExportInfo       `json:"sites"`
}

// ManagedSiteAutoCheckinConfig represents auto-checkin configuration for export
type ManagedSiteAutoCheckinConfig struct {
	GlobalEnabled     bool                              `json:"global_enabled"`
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
	ID                     uint   `gorm:"primaryKey"`
	AutoCheckinEnabled     bool   `gorm:"column:auto_checkin_enabled"`
	WindowStart            string `gorm:"column:window_start"`
	WindowEnd              string `gorm:"column:window_end"`
	ScheduleMode           string `gorm:"column:schedule_mode"`
	DeterministicTime      string `gorm:"column:deterministic_time"`
	RetryEnabled           bool   `gorm:"column:retry_enabled"`
	RetryIntervalMinutes   int    `gorm:"column:retry_interval_minutes"`
	RetryMaxAttemptsPerDay int    `gorm:"column:retry_max_attempts_per_day"`
}

func (managedSiteSettingModel) TableName() string {
	return "managed_site_settings"
}

// exportManagedSites exports all managed sites and their configuration
func (s *ImportExportService) exportManagedSites() *ManagedSitesExportData {
	// Check if managed_sites table exists
	if !s.db.Migrator().HasTable(&managedSiteModel{}) {
		return nil
	}

	var sites []managedSiteModel
	if err := s.db.Order("sort ASC, id ASC").Find(&sites).Error; err != nil {
		logrus.WithError(err).Warn("Failed to export managed sites")
		return nil
	}

	if len(sites) == 0 {
		return nil
	}

	result := &ManagedSitesExportData{
		Sites: make([]ManagedSiteExportInfo, 0, len(sites)),
	}

	// Export auto-checkin config
	// Note: Settings row always has ID=1 (single-row config pattern used throughout the app)
	var setting managedSiteSettingModel
	if err := s.db.First(&setting, 1).Error; err == nil {
		result.AutoCheckin = &ManagedSiteAutoCheckinConfig{
			GlobalEnabled:     setting.AutoCheckinEnabled,
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

	return result
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
				for _, rel := range subGroupRelations {
					subGroupIDs = append(subGroupIDs, rel.SubGroupID)
				}

				// Get sub-group details
				var subGroups []models.Group
				if err := s.db.Where("id IN ?", subGroupIDs).Find(&subGroups).Error; err == nil {
					groupData.SubGroups = make([]SubGroupInfo, 0, len(subGroups))
					for _, sg := range subGroups {
						// Find the weight for this sub-group
						weight := 0
						for _, rel := range subGroupRelations {
							if rel.SubGroupID == sg.ID {
								weight = rel.Weight
								break
							}
						}
						groupData.SubGroups = append(groupData.SubGroups, SubGroupInfo{
							GroupName: sg.Name,
							Weight:    weight,
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
					childExport := ChildGroupExport{
						Name:        cg.Name,
						DisplayName: cg.DisplayName,
						Description: cg.Description,
						Enabled:     cg.Enabled,
						ProxyKeys:   cg.ProxyKeys,
						Sort:        cg.Sort,
						Keys:        keysMap[cg.ID],
					}
					groupData.ChildGroups = append(groupData.ChildGroups, childExport)
				}
			}
		}

		groupExports = append(groupExports, groupData)
	}

	// Export managed sites
	managedSitesData := s.exportManagedSites()

	return &SystemExportData{
		Version:        "2.0",
		ExportedAt:     time.Now().Format(time.RFC3339),
		SystemSettings: settingsMap,
		Groups:         groupExports,
		ManagedSites:   managedSitesData,
	}, nil
}

// ImportSystem imports the entire system configuration
func (s *ImportExportService) ImportSystem(tx *gorm.DB, data *SystemExportData) error {
	// Count settings to import for logging
	settingsCount := len(data.SystemSettings)
	groupsCount := len(data.Groups)

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
	for _, groupData := range data.Groups {
		groupID, err := s.ImportGroup(tx, &groupData)
		if err != nil {
			// Log error but continue with other groups
			logrus.WithError(err).Warnf("Failed to import group %s, skipping", groupData.Group.Name)
			continue
		}
		importedGroups++
		logrus.Debugf("Imported group %s with ID %d", groupData.Group.Name, groupID)
	}

	logrus.Infof("Groups imported: %d/%d successful", importedGroups, groupsCount)

	// Import managed sites if present
	if data.ManagedSites != nil && len(data.ManagedSites.Sites) > 0 {
		imported, skipped := s.importManagedSites(tx, data.ManagedSites)
		logrus.Infof("Managed sites imported: %d imported, %d skipped", imported, skipped)
	}

	// Note: Cache refresh should be handled by the handler after transaction commits
	// This ensures the database changes are visible when the cache is refreshed

	return nil
}

// importManagedSites imports managed sites from export data
func (s *ImportExportService) importManagedSites(tx *gorm.DB, data *ManagedSitesExportData) (int, int) {
	if data == nil || len(data.Sites) == 0 {
		return 0, 0
	}

	// Check if managed_sites table exists
	if !tx.Migrator().HasTable(&managedSiteModel{}) {
		logrus.Warn("managed_sites table does not exist, skipping import")
		return 0, len(data.Sites)
	}

	imported := 0
	skipped := 0

	for _, siteInfo := range data.Sites {
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
			logrus.WithError(err).Warnf("Failed to create site %s", uniqueName)
			skipped++
			continue
		}

		if uniqueName != name {
			logrus.Infof("Imported site %s (renamed from %s)", uniqueName, name)
		}
		imported++
	}

	// Import auto-checkin config if present
	// Note: Using First/Create/Save pattern instead of FirstOrCreate+Assign because:
	// 1. This is a singleton config (ID=1) with no concurrent import scenarios
	// 2. Already protected by transaction isolation
	// 3. FirstOrCreate behavior varies across databases (not atomic on SQLite)
	// 4. Current pattern is clearer and has equivalent performance
	if data.AutoCheckin != nil {
		var setting managedSiteSettingModel
		if err := tx.First(&setting, 1).Error; err != nil {
			// Create new setting
			setting = managedSiteSettingModel{
				ID:                     1,
				AutoCheckinEnabled:     data.AutoCheckin.GlobalEnabled,
				WindowStart:            data.AutoCheckin.WindowStart,
				WindowEnd:              data.AutoCheckin.WindowEnd,
				ScheduleMode:           data.AutoCheckin.ScheduleMode,
				DeterministicTime:      data.AutoCheckin.DeterministicTime,
				RetryEnabled:           data.AutoCheckin.RetryStrategy.Enabled,
				RetryIntervalMinutes:   data.AutoCheckin.RetryStrategy.IntervalMinutes,
				RetryMaxAttemptsPerDay: data.AutoCheckin.RetryStrategy.MaxAttemptsPerDay,
			}
			if err := tx.Create(&setting).Error; err != nil {
				logrus.WithError(err).Warn("Failed to create auto-checkin config")
			}
		} else {
			// Update existing setting
			setting.AutoCheckinEnabled = data.AutoCheckin.GlobalEnabled
			setting.WindowStart = data.AutoCheckin.WindowStart
			setting.WindowEnd = data.AutoCheckin.WindowEnd
			setting.ScheduleMode = data.AutoCheckin.ScheduleMode
			setting.DeterministicTime = data.AutoCheckin.DeterministicTime
			setting.RetryEnabled = data.AutoCheckin.RetryStrategy.Enabled
			setting.RetryIntervalMinutes = data.AutoCheckin.RetryStrategy.IntervalMinutes
			setting.RetryMaxAttemptsPerDay = data.AutoCheckin.RetryStrategy.MaxAttemptsPerDay
			if err := tx.Save(&setting).Error; err != nil {
				logrus.WithError(err).Warn("Failed to update auto-checkin config")
			}
		}
	}

	return imported, skipped
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

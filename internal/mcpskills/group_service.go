package mcpskills

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/services"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// GroupService handles MCP service group management operations
type GroupService struct {
	db         *gorm.DB
	mcpService *Service

	// Group list cache
	groupListCache    *groupListCacheEntry
	groupListCacheMu  sync.RWMutex
	groupListCacheTTL time.Duration
}

// groupListCacheEntry holds cached group list data
type groupListCacheEntry struct {
	Groups    []MCPServiceGroupDTO
	ExpiresAt time.Time
}

// NewGroupService creates a new group service instance
func NewGroupService(db *gorm.DB, mcpService *Service) *GroupService {
	return &GroupService{
		db:                db,
		mcpService:        mcpService,
		groupListCacheTTL: 30 * time.Second,
	}
}

// InvalidateGroupListCache clears the group list cache
func (s *GroupService) InvalidateGroupListCache() {
	s.groupListCacheMu.Lock()
	s.groupListCache = nil
	s.groupListCacheMu.Unlock()
}

// ListGroups returns all MCP service groups (non-paginated, cached)
func (s *GroupService) ListGroups(ctx context.Context) ([]MCPServiceGroupDTO, error) {
	// Check cache first
	s.groupListCacheMu.RLock()
	if s.groupListCache != nil && time.Now().Before(s.groupListCache.ExpiresAt) {
		groups := s.groupListCache.Groups
		s.groupListCacheMu.RUnlock()
		return groups, nil
	}
	s.groupListCacheMu.RUnlock()

	// Cache miss - fetch from database
	var groups []MCPServiceGroup
	if err := s.db.WithContext(ctx).
		Order("id DESC").
		Find(&groups).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	dtos := s.convertGroupsToDTOs(groups)

	// Update cache
	s.groupListCacheMu.Lock()
	if s.groupListCache != nil && time.Now().Before(s.groupListCache.ExpiresAt) {
		cachedGroups := s.groupListCache.Groups
		s.groupListCacheMu.Unlock()
		return cachedGroups, nil
	}
	s.groupListCache = &groupListCacheEntry{
		Groups:    dtos,
		ExpiresAt: time.Now().Add(s.groupListCacheTTL),
	}
	s.groupListCacheMu.Unlock()

	return dtos, nil
}

// ListGroupsPaginated returns paginated group list with optional filters
func (s *GroupService) ListGroupsPaginated(ctx context.Context, params GroupListParams) (*GroupListResult, error) {
	// Validate and normalize pagination params
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PageSize < 1 {
		params.PageSize = 50
	}
	if params.PageSize > 200 {
		params.PageSize = 200
	}

	// Build base query
	query := s.db.WithContext(ctx).Model(&MCPServiceGroup{})

	// Apply filters
	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		query = query.Where(
			"name LIKE ? OR display_name LIKE ? OR description LIKE ?",
			searchPattern, searchPattern, searchPattern,
		)
	}
	if params.Enabled != nil {
		query = query.Where("enabled = ?", *params.Enabled)
	}

	// Get total count
	var total int64
	countQuery := query.Session(&gorm.Session{})
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Calculate pagination
	offset := (params.Page - 1) * params.PageSize
	totalPages := int((total + int64(params.PageSize) - 1) / int64(params.PageSize))

	// Fetch paginated data
	var groups []MCPServiceGroup
	if err := query.
		Order("id DESC").
		Offset(offset).
		Limit(params.PageSize).
		Find(&groups).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	dtos := s.convertGroupsToDTOs(groups)

	return &GroupListResult{
		Groups:     dtos,
		Total:      total,
		Page:       params.Page,
		PageSize:   params.PageSize,
		TotalPages: totalPages,
	}, nil
}

// convertGroupsToDTOs converts group models to DTOs
func (s *GroupService) convertGroupsToDTOs(groups []MCPServiceGroup) []MCPServiceGroupDTO {
	dtos := make([]MCPServiceGroupDTO, 0, len(groups))
	for i := range groups {
		dtos = append(dtos, s.groupToDTO(&groups[i]))
	}
	return dtos
}

// groupToDTO converts a single group to DTO
func (s *GroupService) groupToDTO(group *MCPServiceGroup) MCPServiceGroupDTO {
	serviceIDs := group.GetServiceIDs()
	return MCPServiceGroupDTO{
		ID:                 group.ID,
		Name:               group.Name,
		DisplayName:        group.DisplayName,
		Description:        group.Description,
		ServiceIDs:         serviceIDs,
		ServiceCount:       len(serviceIDs),
		Enabled:            group.Enabled,
		AggregationEnabled: group.AggregationEnabled,
		HasAccessToken:     group.AccessToken != "",
		CreatedAt:          group.CreatedAt,
		UpdatedAt:          group.UpdatedAt,
	}
}

// groupToDTOWithEndpoints converts a single group to DTO with endpoint URLs
func (s *GroupService) groupToDTOWithEndpoints(group *MCPServiceGroup, serverAddress string) MCPServiceGroupDTO {
	dto := s.groupToDTO(group)

	// Generate endpoint URLs
	if group.AggregationEnabled {
		dto.AggregationEndpoint = fmt.Sprintf("%s/mcp/aggregation/%s", serverAddress, group.Name)
	}
	dto.SkillExportEndpoint = fmt.Sprintf("%s/api/mcp-skills/groups/%d/export", serverAddress, group.ID)

	return dto
}

// GetGroupByID retrieves a group by ID with services populated
func (s *GroupService) GetGroupByID(ctx context.Context, id uint) (*MCPServiceGroupDTO, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	dto := s.groupToDTO(&group)

	// Populate services
	serviceIDs := group.GetServiceIDs()
	if len(serviceIDs) > 0 {
		var services []MCPService
		if err := s.db.WithContext(ctx).Where("id IN ?", serviceIDs).Find(&services).Error; err == nil {
			serviceDTOs := make([]MCPServiceDTO, 0, len(services))
			for i := range services {
				serviceDTOs = append(serviceDTOs, s.mcpService.serviceToDTO(&services[i]))
			}
			dto.Services = serviceDTOs
		}
	}

	return &dto, nil
}

// GetGroupByName retrieves a group by name
func (s *GroupService) GetGroupByName(ctx context.Context, name string) (*MCPServiceGroupDTO, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	dto := s.groupToDTO(&group)
	return &dto, nil
}

// CreateGroup creates a new MCP service group
func (s *GroupService) CreateGroup(ctx context.Context, params CreateGroupParams) (*MCPServiceGroupDTO, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.group_name_required", nil)
	}

	// Validate name format
	if !isValidServiceName(name) {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_name_format", nil)
	}

	// Check for duplicate name
	var count int64
	if err := s.db.WithContext(ctx).Model(&MCPServiceGroup{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	if count > 0 {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.group_name_duplicate", map[string]any{"name": name})
	}

	displayName := strings.TrimSpace(params.DisplayName)
	if displayName == "" {
		displayName = name
	}

	// Validate service IDs exist
	if len(params.ServiceIDs) > 0 {
		var existCount int64
		if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("id IN ?", params.ServiceIDs).Count(&existCount).Error; err != nil {
			return nil, app_errors.ParseDBError(err)
		}
		if int(existCount) != len(params.ServiceIDs) {
			return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_service_ids", nil)
		}
	}

	group := &MCPServiceGroup{
		Name:               name,
		DisplayName:        displayName,
		Description:        strings.TrimSpace(params.Description),
		Enabled:            params.Enabled,
		AggregationEnabled: params.AggregationEnabled,
	}
	group.SetServiceIDs(params.ServiceIDs)

	// Generate access token if aggregation is enabled
	if params.AggregationEnabled {
		if params.AccessToken != "" {
			group.AccessToken = params.AccessToken
		} else {
			group.AccessToken = generateAccessToken()
		}
	}

	if err := s.db.WithContext(ctx).Create(group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateGroupListCache()

	dto := s.groupToDTO(group)
	return &dto, nil
}

// UpdateGroup updates an existing MCP service group
func (s *GroupService) UpdateGroup(ctx context.Context, id uint, params UpdateGroupParams) (*MCPServiceGroupDTO, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if params.Name != nil {
		name := strings.TrimSpace(*params.Name)
		if name == "" {
			return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.group_name_required", nil)
		}
		if !isValidServiceName(name) {
			return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_name_format", nil)
		}
		// Check for duplicate name (exclude current group)
		if name != group.Name {
			var count int64
			if err := s.db.WithContext(ctx).Model(&MCPServiceGroup{}).Where("name = ? AND id != ?", name, id).Count(&count).Error; err != nil {
				return nil, app_errors.ParseDBError(err)
			}
			if count > 0 {
				return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.group_name_duplicate", map[string]any{"name": name})
			}
		}
		group.Name = name
	}

	if params.DisplayName != nil {
		group.DisplayName = strings.TrimSpace(*params.DisplayName)
	}
	if params.Description != nil {
		group.Description = strings.TrimSpace(*params.Description)
	}
	if params.Enabled != nil {
		group.Enabled = *params.Enabled
	}
	if params.ServiceIDs != nil {
		// Validate service IDs exist
		if len(*params.ServiceIDs) > 0 {
			var existCount int64
			if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("id IN ?", *params.ServiceIDs).Count(&existCount).Error; err != nil {
				return nil, app_errors.ParseDBError(err)
			}
			if int(existCount) != len(*params.ServiceIDs) {
				return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_service_ids", nil)
			}
		}
		group.SetServiceIDs(*params.ServiceIDs)
	}
	if params.AggregationEnabled != nil {
		group.AggregationEnabled = *params.AggregationEnabled
		// Generate access token if enabling aggregation and no token exists
		if *params.AggregationEnabled && group.AccessToken == "" {
			group.AccessToken = generateAccessToken()
		}
	}
	if params.AccessToken != nil {
		if *params.AccessToken == "" {
			// Clear token
			group.AccessToken = ""
		} else {
			group.AccessToken = *params.AccessToken
		}
	}

	if err := s.db.WithContext(ctx).Save(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateGroupListCache()

	dto := s.groupToDTO(&group)
	return &dto, nil
}

// DeleteGroup deletes an MCP service group
func (s *GroupService) DeleteGroup(ctx context.Context, id uint) error {
	if err := s.db.WithContext(ctx).Delete(&MCPServiceGroup{}, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	s.InvalidateGroupListCache()
	return nil
}

// ToggleGroupEnabled toggles the enabled status of a group
func (s *GroupService) ToggleGroupEnabled(ctx context.Context, id uint) (*MCPServiceGroupDTO, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	group.Enabled = !group.Enabled
	if err := s.db.WithContext(ctx).Save(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateGroupListCache()

	dto := s.groupToDTO(&group)
	return &dto, nil
}

// AddServicesToGroup adds services to a group
func (s *GroupService) AddServicesToGroup(ctx context.Context, groupID uint, serviceIDs []uint) (*MCPServiceGroupDTO, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Validate service IDs exist
	if len(serviceIDs) > 0 {
		var existCount int64
		if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("id IN ?", serviceIDs).Count(&existCount).Error; err != nil {
			return nil, app_errors.ParseDBError(err)
		}
		if int(existCount) != len(serviceIDs) {
			return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_service_ids", nil)
		}
	}

	// Merge service IDs (avoid duplicates)
	existingIDs := group.GetServiceIDs()
	idSet := make(map[uint]bool)
	for _, id := range existingIDs {
		idSet[id] = true
	}
	for _, id := range serviceIDs {
		idSet[id] = true
	}
	newIDs := make([]uint, 0, len(idSet))
	for id := range idSet {
		newIDs = append(newIDs, id)
	}
	group.SetServiceIDs(newIDs)

	if err := s.db.WithContext(ctx).Save(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateGroupListCache()

	dto := s.groupToDTO(&group)
	return &dto, nil
}

// RemoveServicesFromGroup removes services from a group
func (s *GroupService) RemoveServicesFromGroup(ctx context.Context, groupID uint, serviceIDs []uint) (*MCPServiceGroupDTO, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Remove specified service IDs
	existingIDs := group.GetServiceIDs()
	removeSet := make(map[uint]bool)
	for _, id := range serviceIDs {
		removeSet[id] = true
	}
	newIDs := make([]uint, 0, len(existingIDs))
	for _, id := range existingIDs {
		if !removeSet[id] {
			newIDs = append(newIDs, id)
		}
	}
	group.SetServiceIDs(newIDs)

	if err := s.db.WithContext(ctx).Save(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateGroupListCache()

	dto := s.groupToDTO(&group)
	return &dto, nil
}


// GetGroupByNameWithToken retrieves a group by name and validates access token
// Only returns enabled services for aggregation use
func (s *GroupService) GetGroupByNameWithToken(ctx context.Context, name string, token string) (*MCPServiceGroupDTO, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Validate access token if aggregation is enabled
	if group.AggregationEnabled && group.AccessToken != "" {
		if token != group.AccessToken {
			return nil, services.NewI18nError(app_errors.ErrUnauthorized, "mcp_skills.invalid_access_token", nil)
		}
	}

	dto := s.groupToDTO(&group)

	// Populate services - only include enabled services for aggregation
	serviceIDs := group.GetServiceIDs()
	if len(serviceIDs) > 0 {
		var svcs []MCPService
		// Only fetch enabled services
		if err := s.db.WithContext(ctx).Where("id IN ? AND enabled = ?", serviceIDs, true).Find(&svcs).Error; err == nil {
			serviceDTOs := make([]MCPServiceDTO, 0, len(svcs))
			for i := range svcs {
				serviceDTOs = append(serviceDTOs, s.mcpService.serviceToDTO(&svcs[i]))
			}
			dto.Services = serviceDTOs
		}
	}

	return &dto, nil
}

// GetGroupEndpointInfo returns endpoint information for a group
func (s *GroupService) GetGroupEndpointInfo(ctx context.Context, groupID uint, serverAddress string) (*GroupEndpointInfo, error) {
	group, err := s.GetGroupByID(ctx, groupID)
	if err != nil {
		return nil, err
	}

	info := &GroupEndpointInfo{
		GroupID:        group.ID,
		GroupName:      group.Name,
		SkillExportURL: fmt.Sprintf("%s/api/mcp-skills/groups/%d/export", serverAddress, group.ID),
	}

	if group.AggregationEnabled {
		info.AggregationEndpoint = fmt.Sprintf("%s/mcp/aggregation/%s", serverAddress, group.Name)
	}

	// Generate MCP config JSON for clients
	mcpConfig := s.generateMCPConfigForGroup(group, serverAddress)
	info.MCPConfigJSON = mcpConfig

	return info, nil
}

// generateMCPConfigForGroup generates MCP configuration JSON for a group
func (s *GroupService) generateMCPConfigForGroup(group *MCPServiceGroupDTO, serverAddress string) string {
	if !group.AggregationEnabled {
		return ""
	}

	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			group.Name: map[string]string{
				"url": fmt.Sprintf("%s/mcp/aggregation/%s", serverAddress, group.Name),
			},
		},
	}

	jsonBytes, _ := json.Marshal(config)
	return string(jsonBytes)
}

// RegenerateAccessToken generates a new access token for a group
func (s *GroupService) RegenerateAccessToken(ctx context.Context, groupID uint) (string, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return "", app_errors.ParseDBError(err)
	}

	group.AccessToken = generateAccessToken()
	if err := s.db.WithContext(ctx).Save(&group).Error; err != nil {
		return "", app_errors.ParseDBError(err)
	}

	s.InvalidateGroupListCache()
	return group.AccessToken, nil
}

// GetGroupAccessToken returns the access token for a group (admin only)
func (s *GroupService) GetGroupAccessToken(ctx context.Context, groupID uint) (string, error) {
	var group MCPServiceGroup
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return "", app_errors.ParseDBError(err)
	}
	return group.AccessToken, nil
}

// generateAccessToken generates a random access token
func generateAccessToken() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// CalculateTotalToolCount calculates total tool count for a group
func (s *GroupService) CalculateTotalToolCount(ctx context.Context, groupID uint) (int, error) {
	group, err := s.GetGroupByID(ctx, groupID)
	if err != nil {
		return 0, err
	}

	total := 0
	for _, svc := range group.Services {
		total += svc.ToolCount
	}
	return total, nil
}


// ExportGroups exports all MCP service groups
func (s *GroupService) ExportGroups(ctx context.Context) ([]MCPServiceGroupExportInfo, error) {
	var groups []MCPServiceGroup
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&groups).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Build service ID to name map for all services
	var services []MCPService
	if err := s.db.WithContext(ctx).Select("id", "name").Find(&services).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	serviceIDToName := make(map[uint]string)
	for _, svc := range services {
		serviceIDToName[svc.ID] = svc.Name
	}

	exportData := make([]MCPServiceGroupExportInfo, 0, len(groups))
	for _, group := range groups {
		// Convert service IDs to names for portability
		serviceIDs := group.GetServiceIDs()
		serviceNames := make([]string, 0, len(serviceIDs))
		for _, id := range serviceIDs {
			if name, ok := serviceIDToName[id]; ok {
				serviceNames = append(serviceNames, name)
			}
		}

		info := MCPServiceGroupExportInfo{
			Name:               group.Name,
			DisplayName:        group.DisplayName,
			Description:        group.Description,
			ServiceNames:       serviceNames,
			Enabled:            group.Enabled,
			AggregationEnabled: group.AggregationEnabled,
		}
		exportData = append(exportData, info)
	}

	return exportData, nil
}

// ImportGroups imports groups from export data
// Returns (imported count, skipped count, error)
func (s *GroupService) ImportGroups(ctx context.Context, groups []MCPServiceGroupExportInfo) (int, int, error) {
	if len(groups) == 0 {
		return 0, 0, nil
	}

	// Build service name to ID map
	var services []MCPService
	if err := s.db.WithContext(ctx).Select("id", "name").Find(&services).Error; err != nil {
		return 0, 0, app_errors.ParseDBError(err)
	}
	serviceNameToID := make(map[string]uint)
	for _, svc := range services {
		serviceNameToID[svc.Name] = svc.ID
	}

	imported := 0
	skipped := 0

	for _, info := range groups {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			skipped++
			continue
		}

		// Check if group already exists
		var count int64
		if err := s.db.WithContext(ctx).Model(&MCPServiceGroup{}).Where("name = ?", name).Count(&count).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to check group existence for %s", name)
			skipped++
			continue
		}
		if count > 0 {
			// Generate unique name
			uniqueName, err := s.generateUniqueGroupName(ctx, name)
			if err != nil {
				logrus.WithError(err).Warnf("Failed to generate unique name for group %s", name)
				skipped++
				continue
			}
			name = uniqueName
		}

		displayName := strings.TrimSpace(info.DisplayName)
		if displayName == "" {
			displayName = name
		}

		// Convert service names to IDs
		serviceIDs := make([]uint, 0, len(info.ServiceNames))
		for _, svcName := range info.ServiceNames {
			if id, ok := serviceNameToID[svcName]; ok {
				serviceIDs = append(serviceIDs, id)
			}
		}

		group := &MCPServiceGroup{
			Name:               name,
			DisplayName:        displayName,
			Description:        strings.TrimSpace(info.Description),
			Enabled:            info.Enabled,
			AggregationEnabled: info.AggregationEnabled,
		}
		group.SetServiceIDs(serviceIDs)

		// Generate access token if aggregation is enabled
		if info.AggregationEnabled {
			group.AccessToken = generateAccessToken()
		}

		if err := s.db.WithContext(ctx).Create(group).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to create group %s", name)
			skipped++
			continue
		}

		imported++
	}

	if imported > 0 {
		s.InvalidateGroupListCache()
	}

	return imported, skipped, nil
}

// generateUniqueGroupName generates a unique group name by appending a suffix
func (s *GroupService) generateUniqueGroupName(ctx context.Context, baseName string) (string, error) {
	name := baseName
	maxAttempts := 10
	for i := 1; i <= maxAttempts; i++ {
		var count int64
		if err := s.db.WithContext(ctx).Model(&MCPServiceGroup{}).Where("name = ?", name).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return name, nil
		}
		name = fmt.Sprintf("%s-%d", baseName, i)
	}
	return "", fmt.Errorf("failed to generate unique name after %d attempts", maxAttempts)
}

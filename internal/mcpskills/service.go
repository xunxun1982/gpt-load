package mcpskills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/services"
	"gpt-load/internal/store"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// MCPService handles MCP service management operations
type Service struct {
	db            *gorm.DB
	encryptionSvc encryption.Service
	kvStore       store.Store // Optional KV store for caching (Redis or memory)

	// Service list cache
	serviceListCache    *serviceListCacheEntry
	serviceListCacheMu  sync.RWMutex
	serviceListCacheTTL time.Duration

	// Tool cache TTL settings (Stale-While-Revalidate strategy)
	// SoftTTL: cache is considered stale after this, triggers background refresh
	// HardTTL: cache is invalid after this, must refresh synchronously
	toolCacheSoftTTL time.Duration
	toolCacheHardTTL time.Duration

	// Background refresh tracking to prevent duplicate refreshes
	refreshingServices   map[uint]bool
	refreshingServicesMu sync.Mutex
}

// serviceListCacheEntry holds cached service list data
type serviceListCacheEntry struct {
	Services  []MCPServiceDTO
	ExpiresAt time.Time
}

// NewService creates a new MCP service instance
func NewService(db *gorm.DB, encryptionSvc encryption.Service, kvStore store.Store) *Service {
	return &Service{
		db:                  db,
		encryptionSvc:       encryptionSvc,
		kvStore:             kvStore,
		serviceListCacheTTL: 30 * time.Second,
		toolCacheSoftTTL:    30 * time.Minute, // Stale after 30 minutes
		toolCacheHardTTL:    24 * time.Hour,   // Invalid after 24 hours
		refreshingServices:  make(map[uint]bool),
	}
}

// InvalidateServiceListCache clears the service list cache
func (s *Service) InvalidateServiceListCache() {
	s.serviceListCacheMu.Lock()
	s.serviceListCache = nil
	s.serviceListCacheMu.Unlock()
}

// discoverToolsForNewService attempts to discover tools for a newly created MCP service.
// This is a best-effort operation - if discovery fails, the service is created without tools.
// It also updates the service's DisplayName, Description, and Category based on discovery results.
func (s *Service) discoverToolsForNewService(ctx context.Context, svc *MCPService) []ToolDefinition {
	// Check if we can attempt discovery
	canDiscover := false
	switch svc.Type {
	case string(ServiceTypeStdio):
		if svc.Command != "" && CheckCommandExists(svc.Command) {
			canDiscover = true
		}
	case string(ServiceTypeSSE), string(ServiceTypeStreamableHTTP):
		if svc.APIEndpoint != "" {
			canDiscover = true
		}
	}

	if !canDiscover {
		// Guess category from name if we can't discover
		if svc.Category == string(CategoryCustom) {
			svc.Category = guessCategoryFromName(svc.Name)
		}
		return nil
	}

	logrus.WithField("service", svc.Name).Info("Attempting to discover tools for service")

	toolDiscovery := NewMCPToolDiscovery()
	discoveryResult, err := toolDiscovery.DiscoverToolsForService(ctx, svc)
	if err != nil || !discoveryResult.Success {
		errMsg := "unknown error"
		if err != nil {
			errMsg = err.Error()
		} else if discoveryResult.Error != "" {
			errMsg = discoveryResult.Error
		}
		logrus.WithFields(logrus.Fields{
			"service": svc.Name,
			"error":   errMsg,
		}).Warn("Failed to discover tools for service")
		// Guess category from name as fallback
		if svc.Category == string(CategoryCustom) {
			svc.Category = guessCategoryFromName(svc.Name)
		}
		return nil
	}

	// Update service display name and description if discovered
	if discoveryResult.ServerName != "" && (svc.DisplayName == svc.Name || svc.DisplayName == "") {
		svc.DisplayName = discoveryResult.ServerName
	}
	if discoveryResult.Description != "" && svc.Description == "" {
		svc.Description = discoveryResult.Description
	}

	tools := ConvertDiscoveredToolsToDefinitions(discoveryResult.Tools)

	// Guess category from discovered tools
	if svc.Category == string(CategoryCustom) {
		guessedCategory := guessCategoryFromTools(tools)
		if guessedCategory != "" && guessedCategory != string(CategoryCustom) {
			svc.Category = guessedCategory
		}
	}

	logrus.WithFields(logrus.Fields{
		"service":    svc.Name,
		"tool_count": len(tools),
	}).Info("Successfully discovered tools for service")

	return tools
}

// ListServices returns all MCP services (non-paginated, cached)
func (s *Service) ListServices(ctx context.Context) ([]MCPServiceDTO, error) {
	// Check cache first
	s.serviceListCacheMu.RLock()
	if s.serviceListCache != nil && time.Now().Before(s.serviceListCache.ExpiresAt) {
		services := s.serviceListCache.Services
		s.serviceListCacheMu.RUnlock()
		return services, nil
	}
	s.serviceListCacheMu.RUnlock()

	// Cache miss - fetch from database
	var services []MCPService
	if err := s.db.WithContext(ctx).
		Order("sort ASC, id ASC").
		Find(&services).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	dtos := s.convertServicesToDTOs(services)

	// Update cache
	s.serviceListCacheMu.Lock()
	if s.serviceListCache != nil && time.Now().Before(s.serviceListCache.ExpiresAt) {
		cachedServices := s.serviceListCache.Services
		s.serviceListCacheMu.Unlock()
		return cachedServices, nil
	}
	s.serviceListCache = &serviceListCacheEntry{
		Services:  dtos,
		ExpiresAt: time.Now().Add(s.serviceListCacheTTL),
	}
	s.serviceListCacheMu.Unlock()

	return dtos, nil
}

// ListServicesPaginated returns paginated service list with optional filters
func (s *Service) ListServicesPaginated(ctx context.Context, params ServiceListParams) (*ServiceListResult, error) {
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
	query := s.db.WithContext(ctx).Model(&MCPService{})

	// Apply filters
	if params.Search != "" {
		// AI Review Note: LIKE wildcards (% and _) in user input are intentionally NOT escaped.
		// Reasons: 1) This is a search feature where users may expect wildcard behavior
		// 2) GORM parameterized queries already prevent SQL injection
		// 3) Escaping syntax varies by database (SQLite/PostgreSQL/MySQL)
		// 4) The search is for internal admin use, not public-facing
		searchPattern := "%" + params.Search + "%"
		query = query.Where(
			"name LIKE ? OR display_name LIKE ? OR description LIKE ?",
			searchPattern, searchPattern, searchPattern,
		)
	}
	if params.Category != "" {
		query = query.Where("category = ?", params.Category)
	}
	if params.Enabled != nil {
		query = query.Where("enabled = ?", *params.Enabled)
	}
	if params.Type != "" {
		query = query.Where("type = ?", params.Type)
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
	var services []MCPService
	if err := query.
		Order("sort ASC, id ASC").
		Offset(offset).
		Limit(params.PageSize).
		Find(&services).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	dtos := s.convertServicesToDTOs(services)

	return &ServiceListResult{
		Services:   dtos,
		Total:      total,
		Page:       params.Page,
		PageSize:   params.PageSize,
		TotalPages: totalPages,
	}, nil
}

// convertServicesToDTOs converts service models to DTOs
func (s *Service) convertServicesToDTOs(services []MCPService) []MCPServiceDTO {
	dtos := make([]MCPServiceDTO, 0, len(services))
	for i := range services {
		dtos = append(dtos, s.serviceToDTO(&services[i]))
	}
	return dtos
}

// serviceToDTO converts a single service to DTO
func (s *Service) serviceToDTO(svc *MCPService) MCPServiceDTO {
	dto := MCPServiceDTO{
		ID:              svc.ID,
		Name:            svc.Name,
		DisplayName:     svc.DisplayName,
		Description:     svc.Description,
		Category:        svc.Category,
		Icon:            svc.Icon,
		Sort:            svc.Sort,
		Enabled:         svc.Enabled,
		Type:            svc.Type,
		Command:         svc.Command,
		Cwd:             svc.Cwd,
		APIEndpoint:     svc.APIEndpoint,
		APIKeyName:      svc.APIKeyName,
		HasAPIKey:       strings.TrimSpace(svc.APIKeyValue) != "",
		APIKeyHeader:    svc.APIKeyHeader,
		APIKeyPrefix:    svc.APIKeyPrefix,
		RPDLimit:        svc.RPDLimit,
		MCPEnabled:      svc.MCPEnabled,
		HasAccessToken:  strings.TrimSpace(svc.AccessToken) != "",
		HealthStatus:    string(svc.HealthStatus),
		LastHealthCheck: svc.LastHealthCheck,
		CreatedAt:       svc.CreatedAt,
		UpdatedAt:       svc.UpdatedAt,
	}

	// Parse args
	if args, err := svc.GetArgs(); err == nil {
		dto.Args = args
	}

	// Parse required env vars
	if envVars, err := svc.GetRequiredEnvVars(); err == nil {
		dto.RequiredEnvVars = envVars
	}

	// Parse default envs
	if envs, err := svc.GetDefaultEnvs(); err == nil {
		dto.DefaultEnvs = envs
	}

	// Parse headers
	if headers, err := svc.GetHeaders(); err == nil {
		dto.Headers = headers
	}

	// Parse tools
	if tools, err := svc.GetTools(); err == nil {
		dto.Tools = tools
		dto.ToolCount = len(tools)
	}

	return dto
}

// GetServiceByID retrieves a service by ID
func (s *Service) GetServiceByID(ctx context.Context, id uint) (*MCPServiceDTO, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	dto := s.serviceToDTO(&svc)
	return &dto, nil
}

// GetServiceByName retrieves a service by name
func (s *Service) GetServiceByName(ctx context.Context, name string) (*MCPServiceDTO, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&svc).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	dto := s.serviceToDTO(&svc)
	return &dto, nil
}

// GetServiceByNameWithToken retrieves a service by name and validates access token
// Returns error if service not found, MCP not enabled, or token invalid
func (s *Service) GetServiceByNameWithToken(ctx context.Context, name string, token string) (*MCPServiceDTO, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&svc).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if !svc.MCPEnabled {
		return nil, services.NewI18nError(app_errors.ErrForbidden, "mcp_skills.mcp_not_enabled", nil)
	}

	if !svc.Enabled {
		return nil, services.NewI18nError(app_errors.ErrForbidden, "mcp_skills.service_disabled", nil)
	}

	// Validate access token if set
	if svc.AccessToken != "" && svc.AccessToken != token {
		return nil, services.NewI18nError(app_errors.ErrUnauthorized, "mcp_skills.invalid_access_token", nil)
	}

	dto := s.serviceToDTO(&svc)
	return &dto, nil
}

// GetServiceEndpointInfo returns endpoint information for a service
func (s *Service) GetServiceEndpointInfo(ctx context.Context, serviceID uint, serverAddress string) (*ServiceEndpointInfo, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, serviceID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	info := &ServiceEndpointInfo{
		ServiceID:   svc.ID,
		ServiceName: svc.Name,
		ServiceType: svc.Type,
	}

	// MCP endpoint (for MCP protocol access)
	if svc.MCPEnabled {
		info.MCPEndpoint = fmt.Sprintf("%s/mcp/service/%s", serverAddress, svc.Name)
	}

	// API endpoint (for direct API bridge access)
	if svc.Type == string(ServiceTypeAPIBridge) && svc.APIEndpoint != "" {
		info.APIEndpoint = svc.APIEndpoint
	}

	// Generate MCP config JSON
	info.MCPConfigJSON = s.generateMCPConfigForService(&svc, serverAddress)

	return info, nil
}

// generateMCPConfigForService generates MCP configuration JSON for a service
func (s *Service) generateMCPConfigForService(svc *MCPService, serverAddress string) string {
	if !svc.MCPEnabled {
		return ""
	}

	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			svc.Name: map[string]interface{}{
				"url": fmt.Sprintf("%s/mcp/service/%s", serverAddress, svc.Name),
			},
		},
	}

	// Add headers with actual access token
	if svc.AccessToken != "" {
		config["mcpServers"].(map[string]interface{})[svc.Name].(map[string]interface{})["headers"] = map[string]string{
			"Authorization": fmt.Sprintf("Bearer %s", svc.AccessToken),
		}
	}

	// Use encoder with SetEscapeHTML(false) to avoid escaping < and >
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(config)
	// Remove trailing newline added by Encode
	return strings.TrimSpace(buf.String())
}

// ToggleMCPEnabled toggles the MCP endpoint enabled status
func (s *Service) ToggleMCPEnabled(ctx context.Context, id uint) (*MCPServiceDTO, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	svc.MCPEnabled = !svc.MCPEnabled
	if err := s.db.WithContext(ctx).Save(&svc).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateServiceListCache()
	dto := s.serviceToDTO(&svc)
	return &dto, nil
}

// RegenerateServiceAccessToken generates a new access token for a service
func (s *Service) RegenerateServiceAccessToken(ctx context.Context, id uint) (string, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
		return "", app_errors.ParseDBError(err)
	}

	token, err := generateAccessToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate access token: %w", err)
	}
	svc.AccessToken = token
	if err := s.db.WithContext(ctx).Save(&svc).Error; err != nil {
		return "", app_errors.ParseDBError(err)
	}

	s.InvalidateServiceListCache()
	return svc.AccessToken, nil
}

// CreateService creates a new MCP service
func (s *Service) CreateService(ctx context.Context, params CreateServiceParams) (*MCPServiceDTO, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.name_required", nil)
	}

	// Validate name format (alphanumeric, hyphens, underscores only)
	if !isValidServiceName(name) {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_name_format", nil)
	}

	// Check for duplicate name
	var count int64
	if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	if count > 0 {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.name_duplicate", map[string]any{"name": name})
	}

	displayName := strings.TrimSpace(params.DisplayName)
	if displayName == "" {
		displayName = name
	}

	category := strings.TrimSpace(params.Category)
	if category == "" {
		category = string(CategoryCustom)
	}

	serviceType := strings.TrimSpace(params.Type)
	if serviceType == "" {
		serviceType = string(ServiceTypeAPIBridge)
	}

	svc := &MCPService{
		Name:         name,
		DisplayName:  displayName,
		Description:  strings.TrimSpace(params.Description),
		Category:     category,
		Icon:         strings.TrimSpace(params.Icon),
		Sort:         params.Sort,
		Enabled:      params.Enabled,
		Type:         serviceType,
		Command:      strings.TrimSpace(params.Command),
		Cwd:          strings.TrimSpace(params.Cwd),
		APIEndpoint:  strings.TrimSpace(params.APIEndpoint),
		APIKeyName:   strings.TrimSpace(params.APIKeyName),
		APIKeyHeader: strings.TrimSpace(params.APIKeyHeader),
		APIKeyPrefix: strings.TrimSpace(params.APIKeyPrefix),
		RPDLimit:     params.RPDLimit,
		MCPEnabled:   params.MCPEnabled,
	}

	// Set access token if MCP enabled
	if params.MCPEnabled {
		if params.AccessToken != "" {
			svc.AccessToken = params.AccessToken
		} else {
			token, err := generateAccessToken()
			if err != nil {
				return nil, fmt.Errorf("failed to generate access token: %w", err)
			}
			svc.AccessToken = token
		}
	}

	// Set args
	if len(params.Args) > 0 {
		if err := svc.SetArgs(params.Args); err != nil {
			return nil, fmt.Errorf("failed to set args: %w", err)
		}
	}

	// Encrypt and set API key
	if params.APIKeyValue != "" {
		encrypted, err := s.encryptionSvc.Encrypt(params.APIKeyValue)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt API key: %w", err)
		}
		svc.APIKeyValue = encrypted
	}

	// Set required env vars
	if len(params.RequiredEnvVars) > 0 {
		if err := svc.SetRequiredEnvVars(params.RequiredEnvVars); err != nil {
			return nil, fmt.Errorf("failed to set required env vars: %w", err)
		}
	}

	// Set default envs
	if len(params.DefaultEnvs) > 0 {
		if err := svc.SetDefaultEnvs(params.DefaultEnvs); err != nil {
			return nil, fmt.Errorf("failed to set default envs: %w", err)
		}
	}

	// Set headers
	if len(params.Headers) > 0 {
		if err := svc.SetHeaders(params.Headers); err != nil {
			return nil, fmt.Errorf("failed to set headers: %w", err)
		}
	}

	// Set tools - if provided, use them; otherwise try to discover
	if len(params.Tools) > 0 {
		if err := svc.SetTools(params.Tools); err != nil {
			return nil, fmt.Errorf("failed to set tools: %w", err)
		}
	} else if serviceType != string(ServiceTypeAPIBridge) {
		// Try to discover tools for MCP services (stdio, sse, streamable_http)
		// Skip discovery for api_bridge type as it uses predefined tools
		discoveredTools := s.discoverToolsForNewService(ctx, svc)
		if len(discoveredTools) > 0 {
			if err := svc.SetTools(discoveredTools); err != nil {
				logrus.WithError(err).Warn("Failed to set discovered tools")
			}
		}
	}

	// Auto-enable MCP endpoint if tools are available (tool_count > 0)
	// Skip for api_bridge type - it will be enabled after successful test in CreateServiceFromTemplate
	tools, _ := svc.GetTools()
	if len(tools) > 0 && !svc.MCPEnabled && serviceType != string(ServiceTypeAPIBridge) {
		svc.MCPEnabled = true
		// Generate access token for MCP endpoint
		if svc.AccessToken == "" {
			token, err := generateAccessToken()
			if err != nil {
				return nil, fmt.Errorf("failed to generate access token: %w", err)
			}
			svc.AccessToken = token
		}
	}

	if err := s.db.WithContext(ctx).Create(svc).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateServiceListCache()

	dto := s.serviceToDTO(svc)
	return &dto, nil
}

// UpdateService updates an existing MCP service
func (s *Service) UpdateService(ctx context.Context, id uint, params UpdateServiceParams) (*MCPServiceDTO, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if params.Name != nil {
		name := strings.TrimSpace(*params.Name)
		if name == "" {
			return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.name_required", nil)
		}
		if !isValidServiceName(name) {
			return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_name_format", nil)
		}
		// Check for duplicate name (exclude current service)
		if name != svc.Name {
			var count int64
			if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("name = ? AND id != ?", name, id).Count(&count).Error; err != nil {
				return nil, app_errors.ParseDBError(err)
			}
			if count > 0 {
				return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.name_duplicate", map[string]any{"name": name})
			}
		}
		svc.Name = name
	}

	if params.DisplayName != nil {
		svc.DisplayName = strings.TrimSpace(*params.DisplayName)
	}
	if params.Description != nil {
		svc.Description = strings.TrimSpace(*params.Description)
	}
	if params.Category != nil {
		svc.Category = strings.TrimSpace(*params.Category)
	}
	if params.Icon != nil {
		svc.Icon = strings.TrimSpace(*params.Icon)
	}
	if params.Sort != nil {
		svc.Sort = *params.Sort
	}
	if params.Enabled != nil {
		svc.Enabled = *params.Enabled
	}
	if params.Type != nil {
		svc.Type = strings.TrimSpace(*params.Type)
	}
	if params.Command != nil {
		svc.Command = strings.TrimSpace(*params.Command)
	}
	if params.Args != nil {
		if err := svc.SetArgs(*params.Args); err != nil {
			return nil, fmt.Errorf("failed to set args: %w", err)
		}
	}
	if params.Cwd != nil {
		svc.Cwd = strings.TrimSpace(*params.Cwd)
	}
	if params.APIEndpoint != nil {
		svc.APIEndpoint = strings.TrimSpace(*params.APIEndpoint)
	}
	if params.APIKeyName != nil {
		svc.APIKeyName = strings.TrimSpace(*params.APIKeyName)
	}
	if params.APIKeyValue != nil {
		value := strings.TrimSpace(*params.APIKeyValue)
		if value == "" {
			svc.APIKeyValue = ""
		} else {
			encrypted, err := s.encryptionSvc.Encrypt(value)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt API key: %w", err)
			}
			svc.APIKeyValue = encrypted
		}
	}
	if params.APIKeyHeader != nil {
		svc.APIKeyHeader = strings.TrimSpace(*params.APIKeyHeader)
	}
	if params.APIKeyPrefix != nil {
		svc.APIKeyPrefix = strings.TrimSpace(*params.APIKeyPrefix)
	}
	if params.RequiredEnvVars != nil {
		if err := svc.SetRequiredEnvVars(*params.RequiredEnvVars); err != nil {
			return nil, fmt.Errorf("failed to set required env vars: %w", err)
		}
	}
	if params.DefaultEnvs != nil {
		if err := svc.SetDefaultEnvs(*params.DefaultEnvs); err != nil {
			return nil, fmt.Errorf("failed to set default envs: %w", err)
		}
	}
	if params.Headers != nil {
		if err := svc.SetHeaders(*params.Headers); err != nil {
			return nil, fmt.Errorf("failed to set headers: %w", err)
		}
	}
	if params.Tools != nil {
		if err := svc.SetTools(*params.Tools); err != nil {
			return nil, fmt.Errorf("failed to set tools: %w", err)
		}
	}
	if params.RPDLimit != nil {
		svc.RPDLimit = *params.RPDLimit
	}
	if params.MCPEnabled != nil {
		svc.MCPEnabled = *params.MCPEnabled
		// Generate access token if enabling MCP and no token exists
		if *params.MCPEnabled && svc.AccessToken == "" {
			token, err := generateAccessToken()
			if err != nil {
				return nil, fmt.Errorf("failed to generate access token: %w", err)
			}
			svc.AccessToken = token
		}
	}
	if params.AccessToken != nil {
		svc.AccessToken = strings.TrimSpace(*params.AccessToken)
	}

	if err := s.db.WithContext(ctx).Save(&svc).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateServiceListCache()

	dto := s.serviceToDTO(&svc)
	return &dto, nil
}

// DeleteService deletes an MCP service
func (s *Service) DeleteService(ctx context.Context, id uint) error {
	// Check if service is used in any group
	// Note: We check all groups and parse their service IDs to avoid false positives
	// from LIKE pattern matching (e.g., ID 1 matching "10", "21", etc.)
	var groups []MCPServiceGroup
	if err := s.db.WithContext(ctx).Select("id", "service_ids_json").Find(&groups).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	for _, group := range groups {
		serviceIDs := group.GetServiceIDs()
		for _, svcID := range serviceIDs {
			if svcID == id {
				return services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.service_in_use", nil)
			}
		}
	}

	// Delete associated logs
	if err := s.db.WithContext(ctx).Where("service_id = ?", id).Delete(&MCPLog{}).Error; err != nil {
		logrus.WithError(err).Warn("Failed to delete MCP logs for service")
	}

	// Delete associated tool cache
	if err := s.db.WithContext(ctx).Where("service_id = ?", id).Delete(&MCPToolCache{}).Error; err != nil {
		logrus.WithError(err).Warn("Failed to delete MCP tool cache for service")
	}

	if err := s.db.WithContext(ctx).Delete(&MCPService{}, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	s.InvalidateServiceListCache()
	return nil
}

// DeleteAllServices deletes ALL MCP services and clears service references from groups.
// This is a destructive operation that removes all services regardless of usage.
// Returns the count of deleted services.
func (s *Service) DeleteAllServices(ctx context.Context) (int64, error) {
	var deletedCount int64

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get total count of services
		var totalCount int64
		if err := tx.Model(&MCPService{}).Count(&totalCount).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		if totalCount == 0 {
			return nil
		}

		// Clear service_ids from all groups first to maintain referential integrity
		var groups []MCPServiceGroup
		if err := tx.Find(&groups).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		for i := range groups {
			if len(groups[i].GetServiceIDs()) > 0 {
				groups[i].ServiceIDsJSON = "[]"
				if err := tx.Save(&groups[i]).Error; err != nil {
					return app_errors.ParseDBError(err)
				}
			}
		}

		// Delete all associated logs
		if err := tx.Where("1 = 1").Delete(&MCPLog{}).Error; err != nil {
			logrus.WithError(err).Warn("Failed to delete MCP logs")
		}

		// Delete all services
		result := tx.Where("1 = 1").Delete(&MCPService{})
		if result.Error != nil {
			return app_errors.ParseDBError(result.Error)
		}
		deletedCount = result.RowsAffected

		return nil
	})

	if err != nil {
		return 0, err
	}

	if deletedCount > 0 {
		s.InvalidateServiceListCache()
	}

	return deletedCount, nil
}

// CountAllServices returns the total count of all services.
func (s *Service) CountAllServices(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&MCPService{}).Count(&count).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}
	return count, nil
}

// ToggleServiceEnabled toggles the enabled status of a service
func (s *Service) ToggleServiceEnabled(ctx context.Context, id uint) (*MCPServiceDTO, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	svc.Enabled = !svc.Enabled
	if err := s.db.WithContext(ctx).Save(&svc).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.InvalidateServiceListCache()

	dto := s.serviceToDTO(&svc)
	return &dto, nil
}

// GetAPIBridgeTemplates returns all predefined API bridge templates
func (s *Service) GetAPIBridgeTemplates() []APIBridgeTemplate {
	return APIBridgeTemplates
}

// CreateServiceFromTemplate creates a new service from a predefined template
// customEndpoint allows overriding the default API endpoint (e.g., for third-party proxies)
func (s *Service) CreateServiceFromTemplate(ctx context.Context, templateID string, apiKeyValue string, customEndpoint string) (*MCPServiceDTO, error) {
	var template *APIBridgeTemplate
	for i := range APIBridgeTemplates {
		if APIBridgeTemplates[i].ID == templateID {
			template = &APIBridgeTemplates[i]
			break
		}
	}
	if template == nil {
		return nil, services.NewI18nError(app_errors.ErrResourceNotFound, "mcp_skills.template_not_found", nil)
	}

	// Use custom endpoint if provided, otherwise use template default
	endpoint := template.APIEndpoint
	if customEndpoint != "" {
		endpoint = customEndpoint
	}

	// Create service with MCP disabled initially for api_bridge type
	// It will be enabled after successful test
	dto, err := s.CreateService(ctx, CreateServiceParams{
		Name:         template.Name,
		DisplayName:  template.DisplayName,
		Description:  template.Description,
		Category:     template.Category,
		Icon:         template.Icon,
		Type:         string(ServiceTypeAPIBridge),
		APIEndpoint:  endpoint,
		APIKeyName:   template.APIKeyName,
		APIKeyValue:  apiKeyValue,
		APIKeyHeader: template.APIKeyHeader,
		APIKeyPrefix: template.APIKeyPrefix,
		Tools:        template.Tools,
		Enabled:      true,
		MCPEnabled:   false, // Will be enabled after successful test
	})
	if err != nil {
		return nil, err
	}

	// Test the service and enable MCP endpoint if successful
	testResult, testErr := s.TestService(ctx, dto.ID, "", nil)
	if testErr == nil && testResult != nil && testResult.Success {
		// Test passed, enable MCP endpoint
		mcpEnabled := true
		if _, updateErr := s.UpdateService(ctx, dto.ID, UpdateServiceParams{
			MCPEnabled: &mcpEnabled,
		}); updateErr != nil {
			logrus.WithError(updateErr).Warnf("Failed to enable MCP endpoint for service %s after successful test", dto.Name)
		} else {
			dto.MCPEnabled = true
			dto.HasAccessToken = true
			logrus.WithField("service", dto.Name).Info("MCP endpoint auto-enabled after successful test")
		}
	}

	return dto, nil
}

// TestService tests if an MCP service is working correctly
// For API bridge services, it makes a test API call
// For stdio/sse/streamable_http services, it attempts to connect and list tools
func (s *Service) TestService(ctx context.Context, id uint, toolName string, arguments map[string]interface{}) (*ServiceTestResult, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	result := &ServiceTestResult{
		ServiceID:   svc.ID,
		ServiceName: svc.Name,
		ServiceType: svc.Type,
		TestedAt:    time.Now(),
	}

	// For API bridge services, test by making an API call
	if svc.Type == string(ServiceTypeAPIBridge) {
		// Get tools
		tools, err := svc.GetTools()
		if err != nil || len(tools) == 0 {
			result.Success = false
			result.Error = "No tools configured for this service"
			return result, nil
		}

		// Use first tool if not specified
		if toolName == "" {
			toolName = tools[0].Name
		}

		// Use default arguments if not provided
		if arguments == nil {
			arguments = s.getDefaultTestArguments(toolName)
		}

		// Execute test call using APIExecutor
		executor := NewAPIExecutor(s.db, s.encryptionSvc)
		execResult, err := executor.ExecuteAPIBridgeTool(ctx, svc.ID, toolName, arguments)
		if err != nil {
			result.Success = false
			result.Error = err.Error()
			return result, nil
		}

		// Check result
		if success, ok := execResult["success"].(bool); ok && success {
			result.Success = true
			result.Message = "Service is working correctly"
			result.Response = execResult
		} else {
			result.Success = false
			if errMsg, ok := execResult["error"].(string); ok {
				result.Error = errMsg
			} else {
				result.Error = "API call failed"
			}
			result.Response = execResult
		}
	} else {
		// For stdio/sse/streamable_http services, test by actually connecting
		result = s.testMCPServiceConnection(ctx, &svc)
	}

	return result, nil
}

// testMCPServiceConnection tests MCP service by attempting to connect and list tools
// If successful, it also updates the service's tools in the database
func (s *Service) testMCPServiceConnection(ctx context.Context, svc *MCPService) *ServiceTestResult {
	result := &ServiceTestResult{
		ServiceID:   svc.ID,
		ServiceName: svc.Name,
		ServiceType: svc.Type,
		TestedAt:    time.Now(),
	}

	// Check basic configuration
	switch svc.Type {
	case string(ServiceTypeStdio):
		if svc.Command == "" {
			result.Success = false
			result.Error = "No command configured"
			return result
		}
		// Check if command exists in PATH
		if !CheckCommandExists(svc.Command) {
			result.Success = false
			result.Error = fmt.Sprintf("Command '%s' not found in PATH", svc.Command)
			return result
		}
	case string(ServiceTypeSSE), string(ServiceTypeStreamableHTTP):
		endpoint := svc.APIEndpoint
		if endpoint == "" {
			endpoint = svc.Command
		}
		if endpoint == "" {
			result.Success = false
			result.Error = "No endpoint URL configured"
			return result
		}
	default:
		result.Success = false
		result.Error = fmt.Sprintf("Unsupported service type: %s", svc.Type)
		return result
	}

	// Attempt to discover tools (this tests the actual connection)
	toolDiscovery := NewMCPToolDiscovery()
	discoveryResult, err := toolDiscovery.DiscoverToolsForService(ctx, svc)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("Connection test failed: %v", err)
		return result
	}

	if !discoveryResult.Success {
		result.Success = false
		result.Error = discoveryResult.Error
		if result.Error == "" {
			result.Error = "Failed to connect to MCP service"
		}
		return result
	}

	// Connection successful - update service tools in database
	tools := ConvertDiscoveredToolsToDefinitions(discoveryResult.Tools)
	if len(tools) > 0 {
		if err := svc.SetTools(tools); err == nil {
			// Update service in database with discovered tools and metadata
			updates := map[string]interface{}{
				"tools_json": svc.ToolsJSON,
			}
			// Update display name if discovered and current is same as name
			if discoveryResult.ServerName != "" && (svc.DisplayName == svc.Name || svc.DisplayName == "") {
				updates["display_name"] = discoveryResult.ServerName
			}
			// Update description if discovered and current is empty
			if discoveryResult.Description != "" && svc.Description == "" {
				updates["description"] = discoveryResult.Description
			}
			if err := s.db.WithContext(ctx).Model(svc).Updates(updates).Error; err != nil {
				logrus.WithError(err).Warn("Failed to update service tools after successful test")
			} else {
				logrus.WithFields(logrus.Fields{
					"service":    svc.Name,
					"tool_count": len(tools),
				}).Info("Service tools updated after successful test")
				s.InvalidateServiceListCache()
			}
		}
	}

	result.Success = true
	result.Message = fmt.Sprintf("Service is working correctly. Server: %s, Tools: %d",
		discoveryResult.ServerName, len(discoveryResult.Tools))
	result.Response = map[string]interface{}{
		"server_name":    discoveryResult.ServerName,
		"server_version": discoveryResult.ServerVer,
		"tool_count":     len(discoveryResult.Tools),
		"description":    discoveryResult.Description,
	}

	return result
}

// getDefaultTestArguments returns default test arguments for common tools
func (s *Service) getDefaultTestArguments(toolName string) map[string]interface{} {
	switch toolName {
	case "search":
		return map[string]interface{}{
			"query":       "test",
			"num_results": 1,
		}
	case "find_similar":
		return map[string]interface{}{
			"url":         "https://example.com",
			"num_results": 1,
		}
	case "get_contents":
		return map[string]interface{}{
			"ids": []string{"https://example.com"},
		}
	default:
		return map[string]interface{}{}
	}
}

// isValidServiceName validates service name format
func isValidServiceName(name string) bool {
	// Allow alphanumeric, hyphens, and underscores
	matched, _ := regexp.MatchString(`^[a-zA-Z][a-zA-Z0-9_-]*$`, name)
	return matched && len(name) <= 255
}


// ExportServices exports all MCP services
// plainMode: if true, decrypt sensitive data for plain export; if false, keep encrypted
func (s *Service) ExportServices(ctx context.Context, plainMode bool) ([]MCPServiceExportInfo, error) {
	var services []MCPService
	if err := s.db.WithContext(ctx).Order("sort ASC, id ASC").Find(&services).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	exportData := make([]MCPServiceExportInfo, 0, len(services))
	for _, svc := range services {
		info := MCPServiceExportInfo{
			Name:         svc.Name,
			DisplayName:  svc.DisplayName,
			Description:  svc.Description,
			Category:     svc.Category,
			Icon:         svc.Icon,
			Sort:         svc.Sort,
			Enabled:      svc.Enabled,
			Type:         svc.Type,
			Command:      svc.Command,
			APIEndpoint:  svc.APIEndpoint,
			APIKeyName:   svc.APIKeyName,
			APIKeyHeader: svc.APIKeyHeader,
			APIKeyPrefix: svc.APIKeyPrefix,
			RPDLimit:     svc.RPDLimit,
		}

		// Parse args
		if args, err := svc.GetArgs(); err == nil {
			info.Args = args
		}

		// Parse required env vars
		if envVars, err := svc.GetRequiredEnvVars(); err == nil {
			info.RequiredEnvVars = envVars
		}

		// Parse default envs
		if envs, err := svc.GetDefaultEnvs(); err == nil {
			info.DefaultEnvs = envs
		}

		// Parse headers
		if headers, err := svc.GetHeaders(); err == nil {
			info.Headers = headers
		}

		// Parse tools
		if tools, err := svc.GetTools(); err == nil {
			info.Tools = tools
		}

		// Handle API key based on export mode
		if svc.APIKeyValue != "" {
			if plainMode {
				// Decrypt for plain export
				if decrypted, err := s.encryptionSvc.Decrypt(svc.APIKeyValue); err == nil {
					info.APIKeyValue = decrypted
				} else {
					logrus.WithError(err).Warnf("Failed to decrypt API key for service %s during export", svc.Name)
				}
			} else {
				// Keep encrypted for encrypted export
				info.APIKeyValue = svc.APIKeyValue
			}
		}

		exportData = append(exportData, info)
	}

	return exportData, nil
}

// ImportServices imports services from export data
// plainMode: if true, input data is plain and needs encryption; if false, input is already encrypted
// Returns (imported count, skipped count, error)
func (s *Service) ImportServices(ctx context.Context, services []MCPServiceExportInfo, plainMode bool) (int, int, error) {
	if len(services) == 0 {
		return 0, 0, nil
	}

	imported := 0
	skipped := 0

	for _, info := range services {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			skipped++
			continue
		}

		// Check if service already exists
		var count int64
		if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("name = ?", name).Count(&count).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to check service existence for %s", name)
			skipped++
			continue
		}
		if count > 0 {
			// Generate unique name
			uniqueName, err := s.generateUniqueServiceName(ctx, name)
			if err != nil {
				logrus.WithError(err).Warnf("Failed to generate unique name for service %s", name)
				skipped++
				continue
			}
			name = uniqueName
		}

		displayName := strings.TrimSpace(info.DisplayName)
		if displayName == "" {
			displayName = name
		}

		category := strings.TrimSpace(info.Category)
		if category == "" {
			category = string(CategoryCustom)
		}

		serviceType := strings.TrimSpace(info.Type)
		if serviceType == "" {
			serviceType = string(ServiceTypeAPIBridge)
		}

		svc := &MCPService{
			Name:         name,
			DisplayName:  displayName,
			Description:  strings.TrimSpace(info.Description),
			Category:     category,
			Icon:         strings.TrimSpace(info.Icon),
			Sort:         info.Sort,
			Enabled:      info.Enabled,
			Type:         serviceType,
			Command:      strings.TrimSpace(info.Command),
			APIEndpoint:  strings.TrimSpace(info.APIEndpoint),
			APIKeyName:   strings.TrimSpace(info.APIKeyName),
			APIKeyHeader: strings.TrimSpace(info.APIKeyHeader),
			APIKeyPrefix: strings.TrimSpace(info.APIKeyPrefix),
			RPDLimit:     info.RPDLimit,
		}

		// Set args
		if len(info.Args) > 0 {
			_ = svc.SetArgs(info.Args)
		}

		// Set required env vars
		if len(info.RequiredEnvVars) > 0 {
			_ = svc.SetRequiredEnvVars(info.RequiredEnvVars)
		}

		// Set default envs
		if len(info.DefaultEnvs) > 0 {
			_ = svc.SetDefaultEnvs(info.DefaultEnvs)
		}

		// Set headers
		if len(info.Headers) > 0 {
			_ = svc.SetHeaders(info.Headers)
		}

		// Set tools
		if len(info.Tools) > 0 {
			_ = svc.SetTools(info.Tools)
			// Auto-enable MCP endpoint if tools are available
			svc.MCPEnabled = true
			if svc.AccessToken == "" {
				token, err := generateAccessToken()
				if err != nil {
					logrus.WithError(err).Warnf("Failed to generate access token for service %s", name)
					skipped++
					continue
				}
				svc.AccessToken = token
			}
		}

		// Handle API key encryption
		if info.APIKeyValue != "" {
			if plainMode {
				// Input is plain, need to encrypt
				encrypted, err := s.encryptionSvc.Encrypt(info.APIKeyValue)
				if err != nil {
					logrus.WithError(err).Warnf("Failed to encrypt API key for service %s", name)
					skipped++
					continue
				}
				svc.APIKeyValue = encrypted
			} else {
				// Input is already encrypted, verify it can be decrypted
				if _, err := s.encryptionSvc.Decrypt(info.APIKeyValue); err != nil {
					logrus.WithError(err).Warnf("Failed to decrypt API key for service %s, skipping", name)
					skipped++
					continue
				}
				svc.APIKeyValue = info.APIKeyValue
			}
		}

		if err := s.db.WithContext(ctx).Create(svc).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to create service %s", name)
			skipped++
			continue
		}

		imported++
	}

	if imported > 0 {
		s.InvalidateServiceListCache()
	}

	return imported, skipped, nil
}

// generateUniqueServiceName generates a unique service name by appending a suffix
func (s *Service) generateUniqueServiceName(ctx context.Context, baseName string) (string, error) {
	name := baseName
	maxAttempts := 10
	for i := 1; i <= maxAttempts; i++ {
		var count int64
		if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("name = ?", name).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return name, nil
		}
		name = fmt.Sprintf("%s-%d", baseName, i)
	}
	return "", fmt.Errorf("failed to generate unique name after %d attempts", maxAttempts)
}

// importServiceTask represents a service to be imported with its prepared data
type importServiceTask struct {
	name         string
	displayName  string
	serverConfig MCPServerConfig
	svc          *MCPService
}

// importServiceResult represents the result of importing a single service
type importServiceResult struct {
	name       string
	success    bool
	toolCount  int
	errMsg     string
}

// ImportMCPServersFromJSON imports MCP services from standard MCP JSON configuration format
// This supports the format used by Claude Desktop, Kiro, and other MCP clients
// It uses concurrent tool discovery for better performance during batch imports
// Services that already exist are skipped (not renamed)
func (s *Service) ImportMCPServersFromJSON(ctx context.Context, config MCPServersConfig) (*MCPServersImportResult, error) {
	result := &MCPServersImportResult{
		Imported: 0,
		Skipped:  0,
		Errors:   []string{},
	}

	if len(config.MCPServers) == 0 {
		return result, nil
	}

	// Phase 1: Prepare all services (validate names, check existence, create objects)
	var tasks []importServiceTask
	for serverName, serverConfig := range config.MCPServers {
		name := strings.TrimSpace(serverName)
		if name == "" {
			result.Skipped++
			result.Errors = append(result.Errors, "Empty server name")
			continue
		}

		// Validate name format - convert invalid names
		if !isValidServiceName(name) {
			sanitized := sanitizeServiceName(name)
			if sanitized == "" || !isValidServiceName(sanitized) {
				result.Skipped++
				result.Errors = append(result.Errors, fmt.Sprintf("Invalid service name: %s", name))
				continue
			}
			name = sanitized
		}

		// Check if service already exists - skip if exists (don't rename)
		var count int64
		if err := s.db.WithContext(ctx).Model(&MCPService{}).Where("name = ?", name).Count(&count).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to check service existence for %s", name)
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("Database error for %s", name))
			continue
		}
		if count > 0 {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("Service already exists: %s", name))
			logrus.WithField("service", name).Debug("Skipping existing service during import")
			continue
		}

		// Determine service type
		serviceType := s.determineServiceType(serverConfig)

		// Build initial description
		description := "Imported from MCP JSON"
		_, packageName := GuessPackageManagerFromCommand(serverConfig.Command, serverConfig.Args)
		if packageName != "" {
			description = fmt.Sprintf("MCP server: %s", packageName)
		} else if serverConfig.Command != "" {
			description = fmt.Sprintf("MCP server: %s", serverConfig.Command)
		} else if serverConfig.URL != "" {
			description = fmt.Sprintf("Remote MCP server: %s", serverConfig.URL)
		}

		// Create service object
		svc := &MCPService{
			Name:        name,
			DisplayName: serverName,
			Description: description,
			Category:    string(CategoryCustom), // Will be updated after discovery
			Icon:        s.getIconForServiceType(serviceType),
			Sort:        0,
			Enabled:     !serverConfig.Disabled,
			Type:        serviceType,
			Command:     strings.TrimSpace(serverConfig.Command),
			Cwd:         strings.TrimSpace(serverConfig.Cwd),
		}

		// Set args
		if len(serverConfig.Args) > 0 {
			if err := svc.SetArgs(serverConfig.Args); err != nil {
				logrus.WithError(err).Warnf("Failed to set args for service %s", name)
			}
		}

		// Set environment variables
		if len(serverConfig.Env) > 0 {
			if err := svc.SetDefaultEnvs(serverConfig.Env); err != nil {
				logrus.WithError(err).Warnf("Failed to set env vars for service %s", name)
			}
		}

		// For remote servers, set URL as API endpoint
		if serverConfig.URL != "" {
			svc.APIEndpoint = serverConfig.URL
		}

		// Set headers
		if len(serverConfig.Headers) > 0 {
			if err := svc.SetHeaders(serverConfig.Headers); err != nil {
				logrus.WithError(err).Warnf("Failed to set headers for service %s", name)
			}
		}

		tasks = append(tasks, importServiceTask{
			name:         name,
			displayName:  serverName,
			serverConfig: serverConfig,
			svc:          svc,
		})
	}

	if len(tasks) == 0 {
		return result, nil
	}

	// Phase 2: Concurrent tool discovery with worker pool
	// Limit concurrency to avoid overwhelming the system
	const maxWorkers = 5
	taskChan := make(chan importServiceTask, len(tasks))
	resultChan := make(chan importServiceResult, len(tasks))

	// Start workers
	var wg sync.WaitGroup
	workerCount := maxWorkers
	if len(tasks) < maxWorkers {
		workerCount = len(tasks)
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				res := s.processImportTask(ctx, task)
				resultChan <- res
			}
		}()
	}

	// Send tasks to workers
	for _, task := range tasks {
		taskChan <- task
	}
	close(taskChan)

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	for res := range resultChan {
		if res.success {
			result.Imported++
			logrus.WithFields(logrus.Fields{
				"service":    res.name,
				"tool_count": res.toolCount,
			}).Info("Successfully imported MCP service")
		} else {
			result.Skipped++
			if res.errMsg != "" {
				result.Errors = append(result.Errors, res.errMsg)
			}
		}
	}

	if result.Imported > 0 {
		s.InvalidateServiceListCache()
	}

	return result, nil
}

// processImportTask processes a single import task (tool discovery + DB save)
// This runs in a worker goroutine for concurrent processing
func (s *Service) processImportTask(ctx context.Context, task importServiceTask) importServiceResult {
	res := importServiceResult{
		name:    task.name,
		success: false,
	}

	// Use a shorter timeout for tool discovery during import (15s instead of 30s)
	// This prevents slow services from blocking the entire import
	discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Discover tools (best effort - don't fail import if discovery fails)
	discoveredTools := s.discoverToolsForNewServiceAsync(discoveryCtx, task.svc)
	if len(discoveredTools) > 0 {
		if err := task.svc.SetTools(discoveredTools); err != nil {
			logrus.WithError(err).Warnf("Failed to set tools for service %s", task.name)
		}
		// Auto-enable MCP endpoint if tools are discovered
		task.svc.MCPEnabled = true
		if task.svc.AccessToken == "" {
			// Token generation failure during import is non-fatal, just log and continue without MCP
			token, err := generateAccessToken()
			if err != nil {
				logrus.WithError(err).Warnf("Failed to generate access token for service %s, MCP will be disabled", task.name)
				task.svc.MCPEnabled = false
			} else {
				task.svc.AccessToken = token
			}
		}
	}

	// Save to database
	if err := s.db.WithContext(ctx).Create(task.svc).Error; err != nil {
		logrus.WithError(err).Warnf("Failed to create service %s", task.name)
		res.errMsg = fmt.Sprintf("Failed to create %s: %v", task.name, err)
		return res
	}

	res.success = true
	res.toolCount = len(discoveredTools)
	return res
}

// discoverToolsForNewServiceAsync is a non-blocking version of discoverToolsForNewService
// It respects context cancellation and returns empty slice on timeout
func (s *Service) discoverToolsForNewServiceAsync(ctx context.Context, svc *MCPService) []ToolDefinition {
	// Check if context is already cancelled
	select {
	case <-ctx.Done():
		logrus.WithField("service", svc.Name).Debug("Skipping tool discovery due to context cancellation")
		return nil
	default:
	}

	// Check if we can attempt discovery
	canDiscover := false
	switch svc.Type {
	case string(ServiceTypeStdio):
		if svc.Command != "" && CheckCommandExists(svc.Command) {
			canDiscover = true
		}
	case string(ServiceTypeSSE), string(ServiceTypeStreamableHTTP):
		if svc.APIEndpoint != "" {
			canDiscover = true
		}
	}

	if !canDiscover {
		// Guess category from name if we can't discover
		if svc.Category == string(CategoryCustom) {
			svc.Category = guessCategoryFromName(svc.Name)
		}
		return nil
	}

	logrus.WithField("service", svc.Name).Debug("Attempting to discover tools for service")

	// Use shorter timeout for import to avoid blocking
	toolDiscovery := NewMCPToolDiscoveryWithTimeout(10 * time.Second)
	discoveryResult, err := toolDiscovery.DiscoverToolsForService(ctx, svc)
	if err != nil || !discoveryResult.Success {
		errMsg := "unknown error"
		if err != nil {
			errMsg = err.Error()
		} else if discoveryResult.Error != "" {
			errMsg = discoveryResult.Error
		}
		logrus.WithFields(logrus.Fields{
			"service": svc.Name,
			"error":   errMsg,
		}).Debug("Tool discovery failed during import (non-fatal)")
		// Guess category from name as fallback
		if svc.Category == string(CategoryCustom) {
			svc.Category = guessCategoryFromName(svc.Name)
		}
		return nil
	}

	// Update service display name and description if discovered
	if discoveryResult.ServerName != "" && (svc.DisplayName == svc.Name || svc.DisplayName == "") {
		svc.DisplayName = discoveryResult.ServerName
	}
	if discoveryResult.Description != "" && svc.Description == "" {
		svc.Description = discoveryResult.Description
	}

	tools := ConvertDiscoveredToolsToDefinitions(discoveryResult.Tools)

	// Guess category from discovered tools
	if svc.Category == string(CategoryCustom) {
		guessedCategory := guessCategoryFromTools(tools)
		if guessedCategory != "" && guessedCategory != string(CategoryCustom) {
			svc.Category = guessedCategory
		}
	}

	logrus.WithFields(logrus.Fields{
		"service":    svc.Name,
		"tool_count": len(tools),
	}).Debug("Successfully discovered tools for service during import")

	return tools
}

// guessCategoryFromTools guesses category based on discovered tool names
func guessCategoryFromTools(tools []ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}

	// Collect all tool names
	var toolNames []string
	for _, tool := range tools {
		toolNames = append(toolNames, strings.ToLower(tool.Name))
	}
	allNames := strings.Join(toolNames, " ")

	// Search related
	if strings.Contains(allNames, "search") || strings.Contains(allNames, "query") ||
		strings.Contains(allNames, "find") || strings.Contains(allNames, "lookup") {
		return string(CategorySearch)
	}

	// AI related
	if strings.Contains(allNames, "generate") || strings.Contains(allNames, "complete") ||
		strings.Contains(allNames, "chat") || strings.Contains(allNames, "embed") {
		return string(CategoryAI)
	}

	// Storage/Data related
	if strings.Contains(allNames, "read") || strings.Contains(allNames, "write") ||
		strings.Contains(allNames, "file") || strings.Contains(allNames, "database") ||
		strings.Contains(allNames, "fetch") || strings.Contains(allNames, "get") {
		return string(CategoryStorage)
	}

	// Utility
	if strings.Contains(allNames, "convert") || strings.Contains(allNames, "format") ||
		strings.Contains(allNames, "parse") || strings.Contains(allNames, "validate") {
		return string(CategoryUtil)
	}

	return string(CategoryCustom)
}

// determineServiceType determines the service type from MCP server config
func (s *Service) determineServiceType(config MCPServerConfig) string {
	// If type is explicitly specified
	if config.Type != "" {
		switch strings.ToLower(config.Type) {
		case "stdio":
			return string(ServiceTypeStdio)
		case "sse":
			return string(ServiceTypeSSE)
		case "streamable_http", "streamablehttp", "http":
			return string(ServiceTypeStreamableHTTP)
		}
	}

	// Infer from configuration
	if config.URL != "" {
		// Check URL path for SSE indicator
		if strings.HasSuffix(config.URL, "/sse") {
			return string(ServiceTypeSSE)
		}
		// Default to streamable HTTP for URL-based services
		return string(ServiceTypeStreamableHTTP)
	}
	if config.Command != "" {
		// Local server with command - default to stdio
		return string(ServiceTypeStdio)
	}

	// Default to stdio
	return string(ServiceTypeStdio)
}

// getIconForServiceType returns an appropriate icon for the service type
func (s *Service) getIconForServiceType(serviceType string) string {
	switch serviceType {
	case string(ServiceTypeStdio):
		return "⚡"
	case string(ServiceTypeSSE):
		return "🌐"
	case string(ServiceTypeStreamableHTTP):
		return "🔗"
	case string(ServiceTypeAPIBridge):
		return "🔌"
	default:
		return "🔧"
	}
}

// guessCategoryFromName tries to guess the service category from its name
func guessCategoryFromName(name string) string {
	nameLower := strings.ToLower(name)

	// Search related
	searchKeywords := []string{"search", "exa", "tavily", "serper", "brave", "google", "bing", "duckduckgo", "web"}
	for _, kw := range searchKeywords {
		if strings.Contains(nameLower, kw) {
			return string(CategorySearch)
		}
	}

	// AI related
	aiKeywords := []string{"ai", "llm", "gpt", "claude", "openai", "anthropic", "gemini", "model"}
	for _, kw := range aiKeywords {
		if strings.Contains(nameLower, kw) {
			return string(CategoryAI)
		}
	}

	// Storage/Data related
	storageKeywords := []string{"data", "database", "db", "sql", "postgres", "mysql", "mongo", "redis", "elastic", "file", "storage", "s3", "fetch"}
	for _, kw := range storageKeywords {
		if strings.Contains(nameLower, kw) {
			return string(CategoryStorage)
		}
	}

	// Utility related
	utilityKeywords := []string{"util", "tool", "helper", "convert", "transform", "time", "date", "math", "calc", "weather", "translate", "code", "github", "gitlab", "git"}
	for _, kw := range utilityKeywords {
		if strings.Contains(nameLower, kw) {
			return string(CategoryUtil)
		}
	}

	return string(CategoryCustom)
}

// sanitizeServiceName converts an invalid service name to a valid one
func sanitizeServiceName(name string) string {
	// Replace spaces and special characters with hyphens
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)

	// Remove leading/trailing hyphens
	result = strings.Trim(result, "-")

	// Ensure it starts with a letter
	if len(result) > 0 && !((result[0] >= 'a' && result[0] <= 'z') || (result[0] >= 'A' && result[0] <= 'Z')) {
		result = "mcp-" + result
	}

	// Collapse multiple hyphens
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}

	return result
}

// toolCacheKey generates a cache key for tool cache in KV store
func toolCacheKey(serviceID uint) string {
	return fmt.Sprintf("mcp:tools:%d", serviceID)
}

// toolCacheData represents the data stored in KV cache
type toolCacheData struct {
	Tools       []ToolDefinition `json:"tools"`
	ServerName  string           `json:"server_name"`
	ServerVer   string           `json:"server_version"`
	Description string           `json:"description"`
	CachedAt    time.Time        `json:"cached_at"`
	SoftExpiry  time.Time        `json:"soft_expiry"`
	HardExpiry  time.Time        `json:"hard_expiry"`
}

// GetServiceTools retrieves tools for a service using Stale-While-Revalidate (SWR) strategy.
// - Fresh cache: return immediately
// - Stale cache: return immediately + trigger background refresh
// - Expired/missing cache: refresh synchronously
// forceRefresh: if true, bypasses cache and forces fresh discovery
func (s *Service) GetServiceTools(ctx context.Context, serviceID uint, forceRefresh bool) (*ServiceToolsResult, error) {
	// Get service first
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, serviceID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	result := &ServiceToolsResult{
		ServiceID:   svc.ID,
		ServiceName: svc.Name,
		Tools:       []ToolDefinition{},
	}

	// For API bridge services, return stored tools directly (no discovery needed)
	if svc.Type == string(ServiceTypeAPIBridge) {
		tools, err := svc.GetTools()
		if err != nil {
			return nil, fmt.Errorf("failed to parse tools: %w", err)
		}
		result.Tools = tools
		result.ToolCount = len(tools)
		result.FromCache = false
		return result, nil
	}

	// Skip cache if force refresh
	if forceRefresh {
		return s.discoverAndCacheTools(ctx, &svc)
	}

	// Try to get from cache (KV store first, then DB)
	cacheData, _ := s.getToolCache(ctx, serviceID)

	if cacheData != nil {
		now := time.Now()

		// Check cache freshness
		if now.Before(cacheData.SoftExpiry) {
			// Fresh cache - return immediately
			result := s.buildResultFromCache(cacheData)
			result.ServiceID = svc.ID
			result.ServiceName = svc.Name
			return result, nil
		}

		if now.Before(cacheData.HardExpiry) {
			// Stale cache - return immediately but trigger background refresh
			s.triggerBackgroundRefresh(serviceID, &svc)
			result := s.buildResultFromCache(cacheData)
			result.ServiceID = svc.ID
			result.ServiceName = svc.Name
			return result, nil
		}
		// Hard expired - fall through to synchronous refresh
	}

	// No cache or hard expired - refresh synchronously
	return s.discoverAndCacheTools(ctx, &svc)
}

// getToolCache attempts to get tool cache from KV store first, then falls back to DB
// Returns cache data and source ("kv", "db", or "")
func (s *Service) getToolCache(ctx context.Context, serviceID uint) (*toolCacheData, string) {
	// Try KV store first (faster)
	if s.kvStore != nil {
		key := toolCacheKey(serviceID)
		data, err := s.kvStore.Get(key)
		if err == nil && len(data) > 0 {
			var cacheData toolCacheData
			if err := json.Unmarshal(data, &cacheData); err == nil {
				return &cacheData, "kv"
			}
		}
	}

	// Fall back to database
	var cache MCPToolCache
	err := s.db.WithContext(ctx).Where("service_id = ?", serviceID).First(&cache).Error
	if err == nil {
		tools, err := cache.GetTools()
		if err == nil {
			return &toolCacheData{
				Tools:       tools,
				ServerName:  cache.ServerName,
				ServerVer:   cache.ServerVer,
				Description: cache.Description,
				CachedAt:    cache.UpdatedAt,
				SoftExpiry:  cache.SoftExpiry,
				HardExpiry:  cache.HardExpiry,
			}, "db"
		}
	}

	return nil, ""
}

// buildResultFromCache builds ServiceToolsResult from cache data
func (s *Service) buildResultFromCache(cacheData *toolCacheData) *ServiceToolsResult {
	return &ServiceToolsResult{
		Tools:       cacheData.Tools,
		ToolCount:   len(cacheData.Tools),
		ServerName:  cacheData.ServerName,
		ServerVer:   cacheData.ServerVer,
		Description: cacheData.Description,
		FromCache:   true,
		CachedAt:    &cacheData.CachedAt,
		ExpiresAt:   &cacheData.HardExpiry,
	}
}

// triggerBackgroundRefresh triggers an async refresh if not already in progress
func (s *Service) triggerBackgroundRefresh(serviceID uint, svc *MCPService) {
	s.refreshingServicesMu.Lock()
	if s.refreshingServices[serviceID] {
		s.refreshingServicesMu.Unlock()
		return // Already refreshing
	}
	s.refreshingServices[serviceID] = true
	s.refreshingServicesMu.Unlock()

	// Run refresh in background goroutine
	go func() {
		defer func() {
			s.refreshingServicesMu.Lock()
			delete(s.refreshingServices, serviceID)
			s.refreshingServicesMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		if _, err := s.discoverAndCacheTools(ctx, svc); err != nil {
			logrus.WithFields(logrus.Fields{
				"service_id": serviceID,
				"error":      err.Error(),
			}).Warn("Background tool cache refresh failed")
		} else {
			logrus.WithField("service_id", serviceID).Debug("Background tool cache refresh completed")
		}
	}()
}

// discoverAndCacheTools discovers tools for a service and updates both KV and DB cache
func (s *Service) discoverAndCacheTools(ctx context.Context, svc *MCPService) (*ServiceToolsResult, error) {
	result := &ServiceToolsResult{
		ServiceID:   svc.ID,
		ServiceName: svc.Name,
		Tools:       []ToolDefinition{},
	}

	// Check if discovery is possible
	canDiscover := false
	switch svc.Type {
	case string(ServiceTypeStdio):
		if svc.Command != "" && CheckCommandExists(svc.Command) {
			canDiscover = true
		}
	case string(ServiceTypeSSE), string(ServiceTypeStreamableHTTP):
		endpoint := svc.APIEndpoint
		if endpoint == "" {
			endpoint = svc.Command
		}
		if endpoint != "" {
			canDiscover = true
		}
	}

	if !canDiscover {
		// Return stored tools if any
		tools, _ := svc.GetTools()
		result.Tools = tools
		result.ToolCount = len(tools)
		return result, nil
	}

	// Perform discovery
	toolDiscovery := NewMCPToolDiscovery()
	discoveryResult, err := toolDiscovery.DiscoverToolsForService(ctx, svc)
	if err != nil {
		return nil, fmt.Errorf("tool discovery failed: %w", err)
	}

	if !discoveryResult.Success {
		errMsg := discoveryResult.Error
		if errMsg == "" {
			errMsg = "unknown discovery error"
		}
		return nil, fmt.Errorf("tool discovery failed: %s", errMsg)
	}

	// Convert discovered tools
	tools := ConvertDiscoveredToolsToDefinitions(discoveryResult.Tools)
	now := time.Now()
	softExpiry := now.Add(s.toolCacheSoftTTL)
	hardExpiry := now.Add(s.toolCacheHardTTL)

	result.Tools = tools
	result.ToolCount = len(tools)
	result.ServerName = discoveryResult.ServerName
	result.ServerVer = discoveryResult.ServerVer
	result.Description = discoveryResult.Description
	result.FromCache = false
	result.CachedAt = &now
	result.ExpiresAt = &hardExpiry

	// Update KV cache (if available)
	if s.kvStore != nil {
		cacheData := toolCacheData{
			Tools:       tools,
			ServerName:  discoveryResult.ServerName,
			ServerVer:   discoveryResult.ServerVer,
			Description: discoveryResult.Description,
			CachedAt:    now,
			SoftExpiry:  softExpiry,
			HardExpiry:  hardExpiry,
		}
		if data, err := json.Marshal(cacheData); err == nil {
			key := toolCacheKey(svc.ID)
			if err := s.kvStore.Set(key, data, s.toolCacheHardTTL); err != nil {
				logrus.WithError(err).Warn("Failed to update KV tool cache")
			}
		}
	}

	// Update DB cache
	cache := MCPToolCache{
		ServiceID:   svc.ID,
		ServerName:  discoveryResult.ServerName,
		ServerVer:   discoveryResult.ServerVer,
		Description: discoveryResult.Description,
		SoftExpiry:  softExpiry,
		HardExpiry:  hardExpiry,
	}
	if err := cache.SetTools(tools); err != nil {
		logrus.WithError(err).Warn("Failed to set tools in cache")
	}

	// Upsert cache entry using FirstOrCreate + Assign pattern
	// AI Review Note: We intentionally use FirstOrCreate+Assign instead of Clauses(OnConflict)
	// because it provides better cross-database compatibility (SQLite, PostgreSQL, MySQL).
	// The potential race condition is acceptable here since this is just a cache update -
	// worst case is the cache gets written twice with the same data.
	err = s.db.WithContext(ctx).Where("service_id = ?", svc.ID).
		Assign(MCPToolCache{
			ToolsJSON:   cache.ToolsJSON,
			ServerName:  cache.ServerName,
			ServerVer:   cache.ServerVer,
			Description: cache.Description,
			ToolCount:   cache.ToolCount,
			SoftExpiry:  cache.SoftExpiry,
			HardExpiry:  cache.HardExpiry,
		}).
		FirstOrCreate(&cache).Error
	if err != nil {
		logrus.WithError(err).Warn("Failed to update DB tool cache")
	}

	// Also update the service's stored tools for persistence
	if err := svc.SetTools(tools); err == nil {
		if err := s.db.WithContext(ctx).Model(svc).Update("tools_json", svc.ToolsJSON).Error; err != nil {
			logrus.WithError(err).Warn("Failed to update service tools")
		}
	}

	logrus.WithFields(logrus.Fields{
		"service":     svc.Name,
		"tool_count":  len(tools),
		"soft_expiry": softExpiry,
		"hard_expiry": hardExpiry,
	}).Info("Tools discovered and cached")

	return result, nil
}

// RefreshServiceToolCache forces a refresh of the tool cache for a service
func (s *Service) RefreshServiceToolCache(ctx context.Context, serviceID uint) (*ServiceToolsResult, error) {
	return s.GetServiceTools(ctx, serviceID, true)
}

// InvalidateServiceToolCache removes the tool cache for a service from both KV and DB
func (s *Service) InvalidateServiceToolCache(ctx context.Context, serviceID uint) error {
	// Remove from KV store
	if s.kvStore != nil {
		key := toolCacheKey(serviceID)
		if err := s.kvStore.Delete(key); err != nil {
			logrus.WithError(err).Warn("Failed to delete KV tool cache")
		}
	}

	// Remove from DB
	return s.db.WithContext(ctx).Where("service_id = ?", serviceID).Delete(&MCPToolCache{}).Error
}

// CleanExpiredToolCache removes all hard-expired tool cache entries
// This should be called periodically (e.g., by a background job)
func (s *Service) CleanExpiredToolCache(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).Where("hard_expiry < ?", time.Now()).Delete(&MCPToolCache{})
	return result.RowsAffected, result.Error
}

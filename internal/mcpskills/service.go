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
		Remark:          svc.Remark,
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
// Note: This method is kept for backward compatibility but GetServiceByIDWithToken is preferred
// since service names can be duplicated
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

// GetServiceByIDWithToken retrieves a service by ID and validates access token
// Returns error if service not found, MCP not enabled, or token invalid
// This is the preferred method since service names can be duplicated
func (s *Service) GetServiceByIDWithToken(ctx context.Context, id uint, token string) (*MCPServiceDTO, error) {
	var svc MCPService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
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
// MCP endpoint URL uses service ID to support duplicate service names
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

	// MCP endpoint (for MCP protocol access) - uses ID to support duplicate names
	if svc.MCPEnabled {
		info.MCPEndpoint = fmt.Sprintf("%s/mcp/service/%d", serverAddress, svc.ID)
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
// Uses service ID in URL to support duplicate service names
func (s *Service) generateMCPConfigForService(svc *MCPService, serverAddress string) string {
	if !svc.MCPEnabled {
		return ""
	}

	// Use display name or name as the config key for readability
	// URL uses ID to ensure uniqueness even with duplicate names
	configKey := svc.DisplayName
	if configKey == "" {
		configKey = svc.Name
	}

	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			configKey: map[string]interface{}{
				"url": fmt.Sprintf("%s/mcp/service/%d", serverAddress, svc.ID),
			},
		},
	}

	// Add headers with actual access token
	if svc.AccessToken != "" {
		config["mcpServers"].(map[string]interface{})[configKey].(map[string]interface{})["headers"] = map[string]string{
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
// Duplicate names are auto-renamed with numeric suffix (e.g., name-2, name-3)
// and MCP endpoints use ID-based URLs (/mcp/service/:id)
func (s *Service) CreateService(ctx context.Context, params CreateServiceParams) (*MCPServiceDTO, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.name_required", nil)
	}

	// Validate name format (alphanumeric, hyphens, underscores only)
	if !isValidServiceName(name) {
		return nil, services.NewI18nError(app_errors.ErrValidation, "mcp_skills.validation.invalid_name_format", nil)
	}

	// Generate unique name if duplicate exists (auto-rename with suffix)
	name = s.generateUniqueName(ctx, name)

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
		Remark:       strings.TrimSpace(params.Remark),
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
		// Duplicate names are allowed - each service has unique ID and MCP endpoint URL
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
	if params.Remark != nil {
		svc.Remark = strings.TrimSpace(*params.Remark)
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
			// Auto-enable MCP endpoint if tools discovered and not already enabled
			if !svc.MCPEnabled {
				updates["mcp_enabled"] = true
				// Generate access token if not exists
				if svc.AccessToken == "" {
					token, tokenErr := generateAccessToken()
					if tokenErr != nil {
						logrus.WithError(tokenErr).Warn("Failed to generate access token during test")
					} else {
						updates["access_token"] = token
					}
				}
			}
			if err := s.db.WithContext(ctx).Model(svc).Updates(updates).Error; err != nil {
				logrus.WithError(err).Warn("Failed to update service tools after successful test")
			} else {
				logrus.WithFields(logrus.Fields{
					"service":     svc.Name,
					"tool_count":  len(tools),
					"mcp_enabled": true,
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
// Duplicate names are auto-renamed with numeric suffix (e.g., name-2, name-3)
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

		// Generate unique name if duplicate exists (auto-rename with suffix)
		uniqueName := s.generateUniqueName(ctx, name)

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
			Name:         uniqueName,
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
					logrus.WithError(err).Warnf("Failed to generate access token for service %s", uniqueName)
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
					logrus.WithError(err).Warnf("Failed to encrypt API key for service %s", uniqueName)
					skipped++
					continue
				}
				svc.APIKeyValue = encrypted
			} else {
				// Input is already encrypted, verify it can be decrypted
				if _, err := s.encryptionSvc.Decrypt(info.APIKeyValue); err != nil {
					logrus.WithError(err).Warnf("Failed to decrypt API key for service %s, skipping", uniqueName)
					skipped++
					continue
				}
				svc.APIKeyValue = info.APIKeyValue
			}
		}

		if err := s.db.WithContext(ctx).Create(svc).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to create service %s", uniqueName)
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
// Duplicate names are auto-renamed with numeric suffix (e.g., name-2, name-3)
func (s *Service) ImportMCPServersFromJSON(ctx context.Context, config MCPServersConfig) (*MCPServersImportResult, error) {
	result := &MCPServersImportResult{
		Imported: 0,
		Skipped:  0,
		Errors:   []string{},
	}

	if len(config.MCPServers) == 0 {
		return result, nil
	}

	// Phase 1: Prepare all services (validate names, create objects)
	// Duplicate names are auto-renamed with numeric suffix
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

		// Generate unique name if duplicate exists (auto-rename with suffix)
		uniqueName := s.generateUniqueName(ctx, name)

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

		// Create service object with unique name
		svc := &MCPService{
			Name:        uniqueName,
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
				logrus.WithError(err).Warnf("Failed to set args for service %s", uniqueName)
			}
		}

		// Set environment variables
		if len(serverConfig.Env) > 0 {
			if err := svc.SetDefaultEnvs(serverConfig.Env); err != nil {
				logrus.WithError(err).Warnf("Failed to set env vars for service %s", uniqueName)
			}
		}

		// For remote servers, set URL as API endpoint
		if serverConfig.URL != "" {
			svc.APIEndpoint = serverConfig.URL
		}

		// Set headers
		if len(serverConfig.Headers) > 0 {
			if err := svc.SetHeaders(serverConfig.Headers); err != nil {
				logrus.WithError(err).Warnf("Failed to set headers for service %s", uniqueName)
			}
		}

		tasks = append(tasks, importServiceTask{
			name:         uniqueName,
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

// guessCategoryFromTools guesses category based on discovered tool names and descriptions
// This function analyzes tool names to automatically categorize MCP services
// Keywords are based on popular MCP servers from GitHub modelcontextprotocol/servers,
// punkpeye/awesome-mcp-servers, glama.ai, mcpserve.com, and community lists
// Coverage: 500+ MCP servers across 14 categories
func guessCategoryFromTools(tools []ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}

	// Collect all tool names and descriptions
	var toolNames []string
	for _, tool := range tools {
		toolNames = append(toolNames, strings.ToLower(tool.Name))
		toolNames = append(toolNames, strings.ToLower(tool.Description))
	}
	allNames := strings.Join(toolNames, " ")

	// Helper function to check if any keyword matches
	containsAny := func(text string, keywords []string) bool {
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				return true
			}
		}
		return false
	}

	// Browser automation related (Puppeteer, Playwright, Selenium, BrowserBase, Stagehand, etc.)
	// Popular MCP servers: @anthropic/mcp-server-puppeteer, @anthropic/mcp-server-playwright,
	// browserbase, stagehand, hyperbrowser, steel, browserless, agentql, browser-use
	browserKeywords := []string{
		"browser", "playwright", "puppeteer", "selenium", "click", "navigate",
		"screenshot", "page", "webdriver", "chromium", "headless", "dom",
		"element", "selector", "browserbase", "browserless", "stagehand",
		"hyperbrowser", "steel", "agentql", "browser-use", "browseract",
		"webkit", "firefox", "edge", "safari", "tab", "window", "scroll",
		"hover", "drag", "drop", "input", "form", "submit", "cookie",
		"session", "viewport", "devtools", "network", "console", "pdf",
		"automation", "web-automation", "e2e", "testing", "crawlee",
	}
	if containsAny(allNames, browserKeywords) {
		return string(CategoryBrowser)
	}

	// Database related (SQL, NoSQL, Vector DB, Graph DB, Time Series, Data Warehouses)
	// Popular MCP servers: @anthropic/mcp-server-postgres, @anthropic/mcp-server-sqlite,
	// supabase, neon, planetscale, turso, qdrant, pinecone, weaviate, milvus, chroma,
	// mongodb, redis, elasticsearch, clickhouse, snowflake, bigquery, duckdb, neo4j
	dbKeywords := []string{
		"sql", "query", "database", "table", "postgres", "postgresql", "mysql",
		"mariadb", "mongo", "mongodb", "redis", "memcached", "vector", "qdrant",
		"pinecone", "weaviate", "milvus", "chroma", "sqlite", "bigquery",
		"snowflake", "clickhouse", "cassandra", "dynamodb", "firestore",
		"supabase", "neon", "planetscale", "turso", "libsql", "drizzle",
		"prisma", "schema", "migration", "upstash", "fauna", "xata", "convex",
		// Extended: More databases from MCP ecosystem
		"cockroach", "cockroachdb", "tidb", "yugabyte", "yugabytedb", "singlestore",
		"timescale", "timescaledb", "questdb", "influxdb", "influx", "duckdb",
		"motherduck", "databricks", "athena", "redshift", "edgedb", "surrealdb",
		"arangodb", "couchdb", "couchbase", "rethinkdb", "scylladb", "foundationdb",
		"vitess", "oceanbase", "polardb", "gaussdb", "opengauss", "doris",
		"starrocks", "apache-doris", "greenplum", "vertica", "teradata",
		// Vector databases
		"pgvector", "vespa", "vald", "marqo", "zilliz", "lancedb", "chromadb",
		// Graph databases
		"neo4j", "neptune", "janusgraph", "dgraph", "tigergraph", "memgraph",
		"agensgraph", "orientdb", "arcadedb", "nebula", "hugegraph",
		// ORMs and query builders
		"typeorm", "sequelize", "knex", "kysely", "objection", "bookshelf",
		"mikro-orm", "sqlalchemy", "gorm", "ent", "bun", "sqlx", "diesel",
		// Database operations
		"insert", "update", "delete", "select", "join", "index", "transaction",
		"backup", "restore", "replicate", "shard", "partition",
	}
	if containsAny(allNames, dbKeywords) {
		return string(CategoryDatabase)
	}

	// Filesystem related
	// Popular MCP servers: @anthropic/mcp-server-filesystem, desktop-commander,
	// secure-filesystem, file-context, everything-search
	fsKeywords := []string{
		"file", "directory", "folder", "path", "read_file", "write_file",
		"list_dir", "mkdir", "filesystem", "rename", "copy", "move",
		"delete_file", "create_file", "desktop-commander", "secure-filesystem",
		"file-context", "everything-search", "glob", "watch", "inotify",
		"fswatch", "chokidar", "recursive", "symlink", "hardlink", "permission",
		"chmod", "chown", "stat", "exists", "isfile", "isdir", "basename",
		"dirname", "extension", "mime", "filetype", "encoding", "line",
		"append", "truncate", "seek", "stream", "buffer", "temp", "tmp",
	}
	if containsAny(allNames, fsKeywords) {
		return string(CategoryFilesystem)
	}

	// Search related (Web search, information retrieval, documentation, knowledge)
	// Popular MCP servers: @anthropic/mcp-server-brave-search, exa, tavily, serper,
	// context7, kagi, you.com, perplexity, algolia, elasticsearch, meilisearch
	searchKeywords := []string{
		"search", "find", "lookup", "web_search", "google", "bing",
		"duckduckgo", "brave", "serper", "tavily", "perplexity", "algolia",
		"elasticsearch", "meilisearch", "typesense", "index", "resolve-library",
		"library-docs", "query-docs", "get-library",
		// Extended: More search services from MCP ecosystem
		"exa", "kagi", "you.com", "searxng", "searx", "serpapi", "searchapi",
		"search1api", "metaphor", "phind", "devdocs", "dash", "zeal",
		"context7", "api-docs", "documentation", "docs-search", "knowledge",
		"semantic-search", "hybrid-search", "full-text", "fuzzy", "autocomplete",
		"suggest", "ranking", "relevance", "facet", "filter", "aggregation",
		"wikipedia", "arxiv", "pubmed", "scholar", "academic", "research",
		"wolfram", "alpha", "answer", "qa", "question", "retrieval", "rag",
	}
	if containsAny(allNames, searchKeywords) {
		return string(CategorySearch)
	}

	// Fetch/Scraping related (Web scraping, content extraction, RSS, APIs)
	// Popular MCP servers: @anthropic/mcp-server-fetch, firecrawl, jina-reader,
	// apify, brightdata, scrapingbee, diffbot, readability
	fetchKeywords := []string{
		"fetch", "scrape", "crawl", "extract", "download", "http",
		"firecrawl", "jina", "reader", "parse", "html", "markdown",
		"content", "webpage",
		// Extended: More scraping/fetch services from MCP ecosystem
		"apify", "brightdata", "bright-data", "scrapingbee", "scrapingant",
		"zenrows", "oxylabs", "smartproxy", "webshare", "diffbot", "import.io",
		"parsehub", "octoparse", "webscraper", "scrapy", "colly", "goquery",
		"cheerio", "beautifulsoup", "lxml", "readability", "mercury",
		"rss", "atom", "feed", "syndication", "opml", "podcast",
		"sitemap", "robots", "seo", "meta", "og", "twitter-card",
		"structured-data", "schema.org", "microdata", "rdfa", "json-ld",
		"headless", "render", "javascript", "spa", "ajax", "xhr",
		"proxy", "rotate", "captcha", "antibot", "fingerprint",
		"url", "link", "href", "anchor", "redirect", "canonical",
	}
	if containsAny(allNames, fetchKeywords) {
		return string(CategoryFetch)
	}

	// Communication related (Email, messaging, notifications, CRM, support)
	// Popular MCP servers: @anthropic/mcp-server-slack, discord, telegram, twilio,
	// sendgrid, resend, mailgun, postmark, intercom, zendesk, hubspot
	commKeywords := []string{
		"email", "mail", "slack", "discord", "telegram", "message",
		"notification", "sms", "twilio", "sendgrid", "resend", "mailgun",
		"postmark", "teams", "whatsapp", "webhook", "push", "chat",
		// Extended: More communication services from MCP ecosystem
		"mailchimp", "constantcontact", "sendinblue", "brevo", "klaviyo",
		"drip", "convertkit", "activecampaign", "aweber", "getresponse",
		"zoom", "meet", "webex", "gotomeeting", "whereby", "jitsi",
		"livekit", "daily", "twitch", "youtube-live", "stream",
		"intercom", "zendesk", "freshdesk", "helpscout", "crisp", "drift",
		"tawk", "livechat", "olark", "tidio", "chatwoot", "rocket.chat",
		"mattermost", "zulip", "element", "matrix", "signal", "viber",
		"line", "wechat", "kakaotalk", "messenger", "facebook",
		"hubspot", "salesforce", "pipedrive", "zoho", "copper", "close",
		"apollo", "outreach", "salesloft", "lemlist", "woodpecker",
		"voice", "call", "phone", "voip", "pbx", "asterisk", "twilio-voice",
		"plivo", "vonage", "nexmo", "bandwidth", "telnyx", "sinch",
		"inbox", "imap", "smtp", "pop3", "mime", "attachment",
	}
	if containsAny(allNames, commKeywords) {
		return string(CategoryCommunication)
	}

	// Development tools related (Git, CI/CD, code tools, code context, IDE, testing)
	// Popular MCP servers: @anthropic/mcp-server-github, @anthropic/mcp-server-gitlab,
	// @anthropic/mcp-server-git, linear, jira, sentry, sourcegraph, codeium,
	// sequential-thinking, memory-bank, context7, ace-tool
	devKeywords := []string{
		"git", "commit", "branch", "pull_request", "issue", "code",
		"lint", "test", "github", "gitlab", "bitbucket", "jira",
		"linear", "sentry", "raygun", "npm", "pypi", "package",
		"dependency", "build", "deploy", "ci", "pipeline", "workflow",
		"action", "runner", "search_context", "codebase", "thinking", "sequential",
		// Extended: More dev tools from MCP ecosystem
		"sourcegraph", "codeium", "tabnine", "copilot", "cursor", "continue",
		"aider", "gpt-engineer", "gpt-pilot", "smol-developer", "devika",
		"devin", "opendevin", "swe-agent", "auto-gpt", "babyagi",
		// CI/CD platforms
		"jenkins", "circleci", "travisci", "travis", "buildkite", "drone",
		"argocd", "argo", "flux", "fluxcd", "tekton", "spinnaker",
		"harness", "codefresh", "semaphore", "buddy", "woodpecker-ci",
		"concourse", "gocd", "teamcity", "bamboo", "azure-devops",
		// Code quality and security
		"sonarqube", "sonar", "codecov", "coveralls", "codeclimate",
		"snyk", "dependabot", "renovate", "whitesource", "mend",
		"checkmarx", "veracode", "fortify", "bandit", "semgrep", "codeql",
		// Package managers and registries
		"cargo", "crates", "maven", "gradle", "nuget", "composer",
		"packagist", "rubygems", "gems", "hex", "pub", "cocoapods",
		"carthage", "spm", "vcpkg", "conan", "homebrew", "brew",
		"apt", "yum", "dnf", "pacman", "snap", "flatpak", "appimage",
		// IDE and editor integrations
		"vscode", "vim", "neovim", "emacs", "jetbrains", "intellij",
		"pycharm", "webstorm", "goland", "rider", "clion", "datagrip",
		"sublime", "atom", "brackets", "notepad++", "textmate",
		// Code context and analysis
		"ast", "treesitter", "tree-sitter", "lsp", "language-server",
		"semantic", "symbol", "reference", "definition", "hover",
		"completion", "diagnostic", "refactor", "rename", "format",
		"memory-bank", "knowledge-graph", "context7", "ace-tool", "acemcp",
		"augment", "code-context", "serena", "modelcontextprotocol",
		// Testing frameworks
		"jest", "mocha", "vitest", "pytest", "unittest", "rspec",
		"minitest", "phpunit", "junit", "testng", "nunit", "xunit",
		"cypress", "playwright-test", "selenium-test", "webdriverio",
		"puppeteer-test", "testcafe", "nightwatch", "protractor",
		// Version control
		"svn", "subversion", "mercurial", "hg", "perforce", "p4",
		"fossil", "darcs", "bazaar", "cvs", "tfs",
		"merge", "rebase", "cherry-pick", "stash", "tag", "release",
		"changelog", "semver", "version", "bump",
	}
	if containsAny(allNames, devKeywords) {
		return string(CategoryDevelopment)
	}

	// Cloud services related (AWS, GCP, Azure, Cloudflare, PaaS, IaC, containers)
	// Popular MCP servers: @anthropic/mcp-server-cloudflare, aws-kb-retrieval,
	// terraform, kubernetes, docker, vercel, netlify, railway, fly.io
	cloudKeywords := []string{
		"aws", "s3", "lambda", "cloudflare", "azure", "gcp",
		"kubernetes", "k8s", "docker", "container", "pod", "helm",
		"terraform", "pulumi", "vercel", "netlify", "railway", "render",
		"fly", "digitalocean", "linode", "vultr", "ec2", "ecs",
		"fargate", "cloudrun", "function", "serverless",
		// Extended: More cloud services from MCP ecosystem
		"amazon", "sqs", "sns", "kinesis", "dynamodb-streams", "eventbridge",
		"step-functions", "appsync", "cognito", "iam", "kms", "secrets-manager",
		"parameter-store", "ssm", "cloudwatch", "xray", "cloudtrail",
		"route53", "cloudfront", "elb", "alb", "nlb", "api-gateway",
		"vpc", "subnet", "security-group", "nacl", "nat", "igw",
		"rds", "aurora", "elasticache", "documentdb", "neptune", "timestream",
		"glue", "emr", "sagemaker", "bedrock", "comprehend", "rekognition",
		"textract", "polly", "transcribe", "translate", "lex",
		// Google Cloud
		"google-cloud", "gke", "cloud-functions", "cloud-run", "app-engine",
		"compute-engine", "cloud-storage", "gcs", "bigtable", "spanner",
		"pubsub", "dataflow", "dataproc", "vertex-ai", "automl",
		"cloud-sql", "memorystore", "filestore", "cloud-cdn", "cloud-armor",
		// Azure
		"azure-functions", "azure-devops", "azure-pipelines", "aks",
		"azure-storage", "cosmos-db", "azure-sql", "service-bus",
		"event-hubs", "event-grid", "logic-apps", "azure-ml",
		"cognitive-services", "azure-openai", "azure-search",
		// Other cloud providers
		"hetzner", "ovh", "scaleway", "upcloud", "oracle-cloud", "oci",
		"ibm-cloud", "alibaba-cloud", "aliyun", "tencent-cloud",
		// PaaS and hosting
		"heroku", "deno-deploy", "cloudflare-workers", "cloudflare-pages",
		"workers", "pages", "edge", "cdn", "wrangler", "miniflare",
		"apprunner", "beanstalk", "lightsail", "amplify", "firebase",
		"supabase-hosting", "planetscale-hosting", "neon-hosting",
		// Container orchestration
		"k3s", "k0s", "microk8s", "minikube", "kind", "rancher",
		"openshift", "nomad", "swarm", "compose", "podman", "containerd",
		"cri-o", "buildah", "skopeo", "kaniko", "buildkit",
		// Infrastructure as Code
		"crossplane", "ansible", "chef", "puppet", "saltstack",
		"cloudformation", "cdk", "sam", "serverless-framework",
		"sst", "arc", "winglang", "nitric", "encore",
		// Service mesh and networking
		"istio", "linkerd", "consul", "envoy", "nginx", "traefik",
		"haproxy", "caddy", "kong", "ambassador", "gloo",
	}
	if containsAny(allNames, cloudKeywords) {
		return string(CategoryCloud)
	}

	// Monitoring related (Logging, metrics, observability, APM, analytics)
	// Popular MCP servers: datadog, grafana, prometheus, sentry, axiom,
	// newrelic, splunk, honeycomb, opentelemetry, posthog, mixpanel
	monitorKeywords := []string{
		"log", "metric", "trace", "monitor", "alert", "health",
		"datadog", "grafana", "prometheus", "axiom", "logstash",
		"kibana", "newrelic", "splunk", "honeycomb", "lightstep",
		"jaeger", "zipkin", "opentelemetry", "otel", "apm", "observ",
		"digma", "uptime",
		// Extended: More monitoring services from MCP ecosystem
		"dynatrace", "appdynamics", "instana", "elastic-apm", "scout",
		"skywalking", "signoz", "uptrace", "highlight", "logrocket",
		"fullstory", "hotjar", "clarity", "heap", "pendo", "amplitude",
		"mixpanel", "segment", "rudderstack", "jitsu", "snowplow",
		"posthog", "plausible", "umami", "matomo", "fathom", "simple-analytics",
		"countly", "pirsch", "goatcounter", "ackee", "shynet",
		// Alerting and incident management
		"pagerduty", "opsgenie", "victorops", "splunk-oncall", "incident.io",
		"firehydrant", "rootly", "statuspage", "atlassian-statuspage",
		"betteruptime", "uptime-kuma", "pingdom", "uptimerobot", "statuscake",
		"checkly", "synthetic", "rum", "real-user-monitoring",
		// Log management
		"loki", "fluentd", "fluent-bit", "vector", "logdna", "papertrail",
		"loggly", "sumo-logic", "graylog", "seq", "serilog", "bunyan",
		"winston", "pino", "morgan", "log4j", "logback", "slf4j",
		// Metrics and time series
		"influx", "telegraf", "statsd", "collectd", "netdata", "zabbix",
		"nagios", "icinga", "checkmk", "sensu", "riemann", "bosun",
		"thanos", "cortex", "mimir", "victoria-metrics", "m3db",
		// Distributed tracing
		"tempo", "x-ray", "cloud-trace", "application-insights",
		"span", "baggage", "context-propagation", "w3c-trace",
	}
	if containsAny(allNames, monitorKeywords) {
		return string(CategoryMonitoring)
	}

	// Productivity related (Notion, calendar, task management, notes, collaboration)
	// Popular MCP servers: notion, airtable, todoist, asana, trello, obsidian,
	// google-drive, google-docs, google-sheets, confluence, linear
	prodKeywords := []string{
		"notion", "calendar", "task", "todo", "note", "document",
		"airtable", "todoist", "asana", "trello", "monday", "clickup",
		"obsidian", "roam", "logseq", "coda", "confluence", "wiki",
		"docs", "sheet", "spreadsheet", "drive", "dropbox", "onedrive",
		"sharepoint", "box",
		// Extended: More productivity tools from MCP ecosystem
		"basecamp", "wrike", "smartsheet", "teamwork", "podio", "workfront",
		"monday.com", "height", "shortcut", "clubhouse", "pivotal",
		"youtrack", "plane", "taiga", "openproject", "redmine",
		// Note-taking and PKM
		"evernote", "onenote", "bear", "craft", "mem", "reflect",
		"tana", "capacities", "anytype", "fibery", "heptabase",
		"remnote", "supernotes", "amplenote", "notesnook", "joplin",
		"standard-notes", "simplenote", "apple-notes", "google-keep",
		// Document collaboration
		"google-docs", "google-sheets", "google-slides", "google-forms",
		"microsoft-365", "office-365", "word", "excel", "powerpoint",
		"quip", "paper", "slite", "gitbook", "readme", "docusaurus",
		"mintlify", "nextra", "vitepress", "docsify", "mkdocs",
		// Time management
		"toggl", "clockify", "harvest", "timely", "rescuetime",
		"wakatime", "activitywatch", "timing", "hours", "everhour",
		// Bookmarking and reading
		"pocket", "instapaper", "raindrop", "pinboard", "wallabag",
		"omnivore", "readwise", "matter", "feedbin", "feedly", "inoreader",
		// Whiteboard and diagramming
		"miro", "figma", "figjam", "whimsical", "lucidchart", "draw.io",
		"excalidraw", "tldraw", "mermaid", "plantuml", "diagrams.net",
		// Password and secrets
		"1password", "bitwarden", "lastpass", "dashlane", "keeper",
		"nordpass", "enpass", "keychain", "vault", "doppler",
	}
	if containsAny(allNames, prodKeywords) {
		return string(CategoryProductivity)
	}

	// AI related (LLM, ML, model inference, agents, image/audio/video generation)
	// Popular MCP servers: openai, anthropic, gemini, mistral, groq, replicate,
	// huggingface, together, cohere, stability, midjourney, elevenlabs
	aiKeywords := []string{
		"generate", "complete", "embed", "llm", "model", "inference",
		"openai", "anthropic", "claude", "gpt", "gemini", "mistral",
		"llama", "huggingface", "replicate", "together", "groq", "cohere",
		"ai21", "stability", "midjourney", "dalle", "whisper", "transcribe",
		"tts", "speech",
		// Extended: More AI services from MCP ecosystem
		"perplexity-ai", "you-ai", "phind-ai", "kagi-ai", "poe",
		"character-ai", "pi", "inflection", "adept", "aleph-alpha",
		"writer", "jasper", "copy.ai", "anyword", "writesonic",
		// Open source models
		"llama2", "llama3", "codellama", "vicuna", "alpaca", "falcon",
		"mpt", "dolly", "pythia", "gpt-j", "gpt-neo", "gpt-neox",
		"bloom", "opt", "cerebras", "starcoder", "codegen", "santacoder",
		"phi", "phi-2", "phi-3", "orca", "zephyr", "neural-chat",
		"mixtral", "qwen", "yi", "deepseek", "internlm", "baichuan",
		"chatglm", "glm", "aquila", "moss", "tigerbot", "skywork",
		// Image generation
		"stable-diffusion", "sdxl", "sd3", "flux", "imagen", "parti",
		"muse", "kandinsky", "deepfloyd", "playground", "leonardo",
		"ideogram", "firefly", "bing-image", "copilot-image",
		"controlnet", "lora", "dreambooth", "textual-inversion",
		"img2img", "inpaint", "outpaint", "upscale", "enhance",
		// Video generation
		"runway", "pika", "luma", "gen-2", "gen-3", "sora", "kling",
		"haiper", "minimax", "morph", "kaiber", "synthesia", "heygen",
		"d-id", "colossyan", "elai", "rephrase", "tavus", "vidnoz",
		// Audio generation
		"suno", "udio", "musicgen", "audiogen", "bark", "tortoise",
		"coqui", "elevenlabs", "play.ht", "murf", "resemble", "descript",
		"wellsaid", "lovo", "speechify", "naturalreader", "voicemod",
		// Speech recognition
		"deepgram", "assembly", "assemblyai", "speechmatics", "rev",
		"otter", "fireflies", "grain", "trint", "sonix", "happy-scribe",
		// AI frameworks and tools
		"langchain", "llamaindex", "llama-index", "haystack", "semantic-kernel",
		"autogen", "crewai", "agentgpt", "babyagi", "superagi", "camel",
		"flowise", "langflow", "dify", "openagents", "chatdev",
		"metagpt", "gpt-researcher", "storm", "khoj", "quivr",
		// Vector and embedding
		"embedding", "vectorize", "chunk", "split", "tokenize",
		"sentence-transformer", "instructor", "bge", "e5", "gte",
		"nomic", "voyage", "jina-embedding", "cohere-embed",
		// Fine-tuning and training
		"finetune", "fine-tune", "lora", "qlora", "peft", "adapter",
		"train", "dataset", "annotation", "label", "doccano", "prodigy",
	}
	if containsAny(allNames, aiKeywords) {
		return string(CategoryAI)
	}

	// Storage related (Object storage, CDN, media management)
	// Popular MCP servers: cloudflare-r2, minio, backblaze, cloudinary,
	// uploadthing, imagekit, bunny, fastly
	storageKeywords := []string{
		"bucket", "object", "upload", "storage", "blob", "r2",
		"minio", "backblaze", "cloudinary", "imgix", "uploadthing", "cdn",
		// Extended: More storage services from MCP ecosystem
		"wasabi", "filebase", "storj", "sia", "filecoin", "ipfs",
		"arweave", "ceramic", "textile", "web3.storage", "nft.storage",
		"uploadcare", "filestack", "transloadit", "imagekit", "sirv",
		"bunny-cdn", "keycdn", "fastly", "akamai", "cloudfront",
		"stackpath", "limelight", "verizon-media", "edgecast",
		// Media processing
		"ffmpeg", "imagemagick", "sharp", "jimp", "pillow", "opencv",
		"mediaconvert", "elastic-transcoder", "mux", "cloudflare-stream",
		"api.video", "vimeo", "wistia", "brightcove", "jwplayer",
		"video.js", "hls", "dash", "webrtc", "mediasoup", "janus",
		// File sharing
		"wetransfer", "sendanywhere", "filemail", "smash", "gofile",
		"mega", "mediafire", "zippyshare", "rapidshare", "4shared",
		// Backup and sync
		"restic", "borg", "duplicati", "rclone", "syncthing", "rsync",
		"time-machine", "backblaze-backup", "crashplan", "carbonite",
	}
	if containsAny(allNames, storageKeywords) {
		return string(CategoryStorage)
	}

	// Utility (General tools, converters, formatters, data processing)
	// Popular MCP servers: everything, time, weather, currency, qr, pdf,
	// pandoc, ffmpeg, imagemagick, translate
	utilKeywords := []string{
		"convert", "format", "validate", "transform", "calculate", "encode",
		"decode", "compress", "hash", "encrypt", "decrypt", "uuid",
		"random", "time", "date", "timezone", "currency", "unit",
		"weather", "translate", "qr", "barcode", "pdf", "image",
		"resize", "crop",
		// Extended: More utility tools from MCP ecosystem
		"pandoc", "latex", "tex", "markdown", "asciidoc", "rst",
		"docx", "xlsx", "pptx", "odt", "rtf", "epub", "mobi",
		"json", "yaml", "toml", "xml", "csv", "tsv", "parquet",
		"avro", "protobuf", "msgpack", "bson", "cbor", "ion",
		// Text processing
		"regex", "regexp", "pattern", "match", "replace", "split",
		"join", "trim", "pad", "case", "slug", "sanitize", "escape",
		"diff", "patch", "merge", "compare", "dedupe", "unique",
		// Data validation
		"schema", "jsonschema", "ajv", "zod", "yup", "joi", "valibot",
		"typebox", "io-ts", "runtypes", "superstruct",
		// Compression
		"gzip", "bzip2", "xz", "lzma", "zstd", "lz4", "snappy",
		"brotli", "deflate", "zip", "tar", "rar", "7z", "archive",
		// Cryptography
		"aes", "rsa", "ecdsa", "ed25519", "sha256", "sha512", "md5",
		"bcrypt", "argon2", "scrypt", "pbkdf2", "hmac", "jwt", "jwe",
		"pgp", "gpg", "ssl", "tls", "certificate", "x509",
		// Math and science
		"math", "calc", "calculator", "formula", "equation", "algebra",
		"statistics", "probability", "matrix", "vector", "tensor",
		"numpy", "scipy", "pandas", "matplotlib", "plotly", "chart",
		// Location and maps
		"geo", "geocode", "reverse-geocode", "coordinates", "latitude",
		"longitude", "distance", "route", "directions", "maps",
		"openstreetmap", "osm", "mapbox", "here", "tomtom",
		// Weather and environment
		"openweather", "weatherapi", "accuweather", "darksky",
		"forecast", "temperature", "humidity", "wind", "precipitation",
		"air-quality", "aqi", "pollen", "uv-index",
		// Translation and language
		"deepl", "google-translate", "azure-translate", "amazon-translate",
		"libretranslate", "argos", "language-detect", "spell-check",
		"grammar", "grammarly", "languagetool", "prowritingaid",
		// Finance and currency
		"exchange-rate", "forex", "stock", "crypto", "bitcoin", "ethereum",
		"coinbase", "binance", "kraken", "coingecko", "coinmarketcap",
		"stripe", "paypal", "square", "adyen", "braintree", "mollie",
		// Misc utilities
		"clipboard", "screenshot", "screen-capture", "ocr", "tesseract",
		"barcode-reader", "qr-reader", "color", "palette", "gradient",
		"lorem", "faker", "mock", "placeholder", "avatar", "gravatar",
		"shorturl", "bitly", "tinyurl", "rebrandly", "dub",
		"cron", "schedule", "timer", "countdown", "stopwatch",
		"notification", "toast", "alert", "modal", "dialog",
	}
	if containsAny(allNames, utilKeywords) {
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
// This function is used when tool discovery fails or for quick categorization
// Keywords are based on popular MCP servers from GitHub modelcontextprotocol/servers,
// punkpeye/awesome-mcp-servers, glama.ai, mcpserve.com, and community lists
// Coverage: 500+ MCP servers across 14 categories
func guessCategoryFromName(name string) string {
	nameLower := strings.ToLower(name)

	// Helper function to check if any keyword matches
	containsAny := func(text string, keywords []string) bool {
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				return true
			}
		}
		return false
	}

	// Browser automation related (Puppeteer, Playwright, Selenium, BrowserBase, Stagehand, etc.)
	browserKeywords := []string{
		"browser", "playwright", "puppeteer", "selenium", "chrome", "firefox",
		"webdriver", "browseract", "browserbase", "headless", "chromium", "webkit",
		"stagehand", "hyperbrowser", "steel", "agentql", "browser-use", "browserless",
		"crawlee", "e2e", "web-automation",
	}
	if containsAny(nameLower, browserKeywords) {
		return string(CategoryBrowser)
	}

	// Database related (SQL, NoSQL, Vector DB, Graph DB, Time Series)
	dbKeywords := []string{
		"postgres", "postgresql", "mysql", "mariadb", "sqlite", "mongo", "mongodb",
		"redis", "memcached", "elastic", "elasticsearch", "qdrant", "pinecone",
		"weaviate", "milvus", "chroma", "supabase", "neon", "planetscale", "turso",
		"libsql", "drizzle", "prisma", "typeorm", "sequelize", "knex", "kysely",
		"bigquery", "snowflake", "clickhouse", "cassandra", "dynamodb", "firestore",
		"cockroach", "cockroachdb", "tidb", "yugabyte", "yugabytedb", "singlestore",
		"timescale", "timescaledb", "questdb", "influxdb", "influx", "duckdb",
		"motherduck", "databricks", "athena", "redshift", "edgedb", "surrealdb",
		"arangodb", "couchdb", "couchbase", "rethinkdb", "scylladb", "foundationdb",
		"vitess", "oceanbase", "polardb", "gaussdb", "opengauss", "doris", "starrocks",
		"greenplum", "vertica", "teradata", "upstash", "fauna", "xata", "convex",
		"pgvector", "vespa", "vald", "marqo", "zilliz", "lancedb", "chromadb",
		"neo4j", "neptune", "janusgraph", "dgraph", "tigergraph", "memgraph",
		"agensgraph", "orientdb", "arcadedb", "nebula", "hugegraph",
		"mikro-orm", "sqlalchemy", "gorm", "ent", "bun", "sqlx", "diesel",
	}
	if containsAny(nameLower, dbKeywords) {
		return string(CategoryDatabase)
	}

	// Filesystem related
	fsKeywords := []string{
		"filesystem", "file-system", "fs-", "directory", "folder", "desktop-commander",
		"secure-filesystem", "file-context", "everything-search",
	}
	if containsAny(nameLower, fsKeywords) {
		return string(CategoryFilesystem)
	}

	// Search related (Web search, information retrieval, documentation)
	searchKeywords := []string{
		"search", "exa", "tavily", "serper", "brave-search", "google-search",
		"bing", "duckduckgo", "perplexity", "you.com", "algolia", "meilisearch",
		"typesense", "search1api", "serpapi", "searchapi", "kagi", "searxng", "searx",
		"context7", "devdocs", "dash", "zeal", "library-docs", "api-docs",
		"metaphor", "phind", "wikipedia", "arxiv", "pubmed", "scholar", "wolfram",
	}
	if containsAny(nameLower, searchKeywords) {
		return string(CategorySearch)
	}

	// Fetch/Scraping related (Web scraping, content extraction)
	fetchKeywords := []string{
		"fetch", "scrape", "crawl", "firecrawl", "jina", "reader", "extract",
		"apify", "brightdata", "bright-data", "scrapingbee", "scrapingant",
		"zenrows", "oxylabs", "smartproxy", "diffbot", "import.io", "parsehub",
		"octoparse", "webscraper", "scrapy", "readability", "mercury",
		"rss", "feed", "sitemap",
	}
	if containsAny(nameLower, fetchKeywords) {
		return string(CategoryFetch)
	}

	// Communication related (Email, messaging, notifications, CRM)
	commKeywords := []string{
		"email", "mail", "slack", "discord", "telegram", "twilio", "sendgrid",
		"resend", "mailgun", "postmark", "mailchimp", "notification", "sms",
		"whatsapp", "teams", "zoom", "meet", "webex", "intercom", "zendesk",
		"freshdesk", "crisp", "drift", "hubspot", "salesforce", "pipedrive",
		"mattermost", "zulip", "element", "matrix", "signal", "viber", "line",
		"wechat", "messenger", "rocket.chat", "chatwoot", "livekit", "daily",
		"apollo", "outreach", "salesloft", "lemlist", "plivo", "vonage", "nexmo",
		"bandwidth", "telnyx", "sinch",
	}
	if containsAny(nameLower, commKeywords) {
		return string(CategoryCommunication)
	}

	// Development tools related (Git, CI/CD, code tools, code context, IDE)
	devKeywords := []string{
		"github", "gitlab", "bitbucket", "git", "linear", "jira", "sentry",
		"raygun", "vercel", "netlify", "railway", "render", "docker", "npm",
		"pypi", "cargo", "crates", "maven", "gradle", "nuget", "composer",
		"jenkins", "circleci", "travisci", "travis", "buildkite", "drone",
		"argocd", "argo", "flux", "fluxcd", "tekton", "spinnaker", "harness",
		"codefresh", "semaphore", "concourse", "gocd", "teamcity", "bamboo",
		"sonarqube", "sonar", "codecov", "coveralls", "codeclimate",
		"snyk", "dependabot", "renovate", "whitesource", "mend", "checkmarx",
		"veracode", "semgrep", "codeql", "bandit",
		"copilot", "codeium", "tabnine", "sourcegraph", "codesandbox", "stackblitz",
		"replit", "gitpod", "codespaces", "devcontainer", "continue", "cursor",
		"aider", "gpt-engineer", "gpt-pilot", "smol-developer", "devika", "devin",
		"opendevin", "swe-agent",
		"sequential-thinking", "memory-bank", "knowledge-graph", "serena",
		"ace-tool", "acemcp", "augment", "codebase", "code-context", "ast",
		"treesitter", "tree-sitter", "lsp", "language-server", "modelcontextprotocol",
		"vscode", "vim", "neovim", "emacs", "jetbrains", "intellij",
		"jest", "mocha", "vitest", "pytest", "cypress", "playwright-test",
	}
	if containsAny(nameLower, devKeywords) {
		return string(CategoryDevelopment)
	}

	// Cloud services related (AWS, GCP, Azure, Cloudflare, PaaS, IaC)
	cloudKeywords := []string{
		"aws", "amazon", "s3", "lambda", "ec2", "ecs", "fargate", "cloudflare",
		"azure", "gcp", "google-cloud", "digitalocean", "linode", "vultr",
		"hetzner", "ovh", "scaleway", "upcloud", "oracle-cloud", "oci",
		"ibm-cloud", "alibaba-cloud", "aliyun", "tencent-cloud",
		"kubernetes", "k8s", "k3s", "k0s", "microk8s", "minikube", "kind",
		"rancher", "openshift", "nomad", "swarm", "podman", "containerd",
		"terraform", "pulumi", "crossplane", "ansible", "chef", "puppet", "saltstack",
		"cloudformation", "cdk", "sam", "serverless", "sst", "arc", "winglang", "nitric",
		"cloudrun", "apprunner", "beanstalk", "heroku", "fly", "deno-deploy",
		"cloudflare-workers", "cloudflare-pages", "workers", "pages", "wrangler",
		"istio", "linkerd", "consul", "envoy", "nginx", "traefik", "kong",
		"sqs", "sns", "kinesis", "eventbridge", "step-functions", "appsync",
		"cognito", "iam", "kms", "secrets-manager", "route53", "cloudfront",
		"gke", "cloud-functions", "cloud-run", "app-engine", "pubsub",
		"azure-functions", "aks", "cosmos-db", "service-bus", "event-hubs",
	}
	if containsAny(nameLower, cloudKeywords) {
		return string(CategoryCloud)
	}

	// Monitoring related (Logging, metrics, observability, analytics)
	monitorKeywords := []string{
		"datadog", "grafana", "prometheus", "sentry", "axiom", "logstash",
		"kibana", "newrelic", "splunk", "honeycomb", "lightstep", "jaeger",
		"zipkin", "opentelemetry", "otel", "dynatrace", "appdynamics", "instana",
		"elastic-apm", "scout", "skywalking", "signoz", "uptrace", "highlight",
		"logrocket", "fullstory", "hotjar", "clarity", "heap", "pendo",
		"mixpanel", "amplitude", "segment", "rudderstack", "jitsu", "snowplow",
		"posthog", "plausible", "umami", "matomo", "fathom", "simple-analytics",
		"pagerduty", "opsgenie", "victorops", "splunk-oncall", "incident.io",
		"firehydrant", "rootly", "statuspage", "betteruptime", "uptime-kuma",
		"uptime", "pingdom", "uptimerobot", "statuscake", "checkly", "digma",
		"loki", "fluentd", "fluent-bit", "vector", "logdna", "papertrail", "loggly",
		"tempo", "thanos", "cortex", "mimir", "victoria-metrics",
	}
	if containsAny(nameLower, monitorKeywords) {
		return string(CategoryMonitoring)
	}

	// Productivity related (Notion, calendar, task management, notes)
	prodKeywords := []string{
		"notion", "airtable", "todoist", "asana", "trello", "monday", "clickup",
		"basecamp", "wrike", "smartsheet", "teamwork", "podio", "workfront",
		"height", "shortcut", "clubhouse", "pivotal", "youtrack", "plane", "taiga",
		"calendar", "google-docs", "google-sheets", "google-drive", "google-slides",
		"obsidian", "roam", "logseq", "coda", "confluence", "wiki",
		"dropbox", "onedrive", "sharepoint", "box",
		"evernote", "onenote", "bear", "craft", "mem", "reflect", "tana",
		"capacities", "anytype", "fibery", "heptabase", "remnote", "supernotes",
		"amplenote", "notesnook", "joplin", "standard-notes",
		"toggl", "clockify", "harvest", "timely", "rescuetime", "wakatime",
		"pocket", "instapaper", "raindrop", "pinboard", "omnivore", "readwise",
		"miro", "figma", "figjam", "whimsical", "lucidchart", "draw.io", "excalidraw",
		"1password", "bitwarden", "lastpass", "dashlane", "keeper",
	}
	if containsAny(nameLower, prodKeywords) {
		return string(CategoryProductivity)
	}

	// AI related (LLM, ML, model inference, agents, generation)
	aiKeywords := []string{
		"ai", "llm", "gpt", "claude", "openai", "anthropic", "gemini", "mistral",
		"llama", "llama2", "llama3", "codellama", "vicuna", "alpaca", "falcon",
		"huggingface", "replicate", "together", "groq", "cohere", "ai21",
		"stability", "midjourney", "dalle", "whisper", "deepgram", "assembly",
		"assemblyai", "speechmatics", "elevenlabs", "play.ht", "murf", "descript",
		"runway", "pika", "luma", "gen-2", "gen-3", "sora", "kling", "haiper",
		"suno", "udio", "musicgen", "audiogen", "bark", "tortoise", "coqui",
		"langchain", "llamaindex", "llama-index", "haystack", "semantic-kernel",
		"autogen", "crewai", "agentgpt", "babyagi", "superagi", "camel",
		"flowise", "langflow", "dify", "openagents", "chatdev", "metagpt",
		"gpt-researcher", "storm", "khoj", "quivr",
		"stable-diffusion", "sdxl", "sd3", "flux", "imagen", "controlnet",
		"perplexity-ai", "you-ai", "phind-ai", "kagi-ai", "poe", "character-ai",
		"mixtral", "qwen", "yi", "deepseek", "internlm", "baichuan", "chatglm",
		"phi", "phi-2", "phi-3", "orca", "zephyr", "neural-chat",
		"synthesia", "heygen", "d-id", "colossyan", "elai", "rephrase",
	}
	if containsAny(nameLower, aiKeywords) {
		return string(CategoryAI)
	}

	// Storage related (Object storage, CDN, media)
	storageKeywords := []string{
		"storage", "bucket", "blob", "r2", "minio", "backblaze", "wasabi",
		"cloudinary", "imgix", "uploadthing", "uploadcare", "filestack",
		"transloadit", "imagekit", "bunny", "bunny-cdn", "keycdn", "fastly", "akamai",
		"filebase", "storj", "sia", "filecoin", "ipfs", "arweave", "ceramic",
		"web3.storage", "nft.storage", "sirv", "stackpath", "cloudfront",
		"mux", "cloudflare-stream", "api.video", "vimeo", "wistia", "brightcove",
		"restic", "borg", "duplicati", "rclone", "syncthing",
	}
	if containsAny(nameLower, storageKeywords) {
		return string(CategoryStorage)
	}

	// Utility related (General tools, converters, formatters)
	utilityKeywords := []string{
		"util", "tool", "helper", "convert", "transform", "time", "date",
		"math", "calc", "calculator", "weather", "translate", "currency", "unit", "qr",
		"barcode", "pdf", "image", "video", "audio", "ffmpeg", "imagemagick",
		"pandoc", "latex", "tex", "markdown", "asciidoc",
		"json", "yaml", "toml", "xml", "csv", "tsv", "parquet", "excel",
		"compass", "mcp-compass",
		"regex", "diff", "patch", "merge", "dedupe",
		"gzip", "bzip2", "xz", "zstd", "lz4", "zip", "tar", "archive",
		"aes", "rsa", "sha256", "md5", "bcrypt", "argon2", "jwt", "pgp",
		"geo", "geocode", "coordinates", "maps", "openstreetmap", "mapbox",
		"openweather", "weatherapi", "accuweather", "forecast",
		"deepl", "google-translate", "libretranslate", "spell-check", "grammar",
		"exchange-rate", "forex", "stock", "crypto", "bitcoin", "ethereum",
		"coinbase", "binance", "coingecko", "coinmarketcap",
		"stripe", "paypal", "square", "adyen", "braintree", "mollie",
		"clipboard", "screenshot", "ocr", "tesseract", "color", "palette",
		"lorem", "faker", "mock", "placeholder", "shorturl", "bitly",
		"cron", "schedule", "timer",
	}
	if containsAny(nameLower, utilityKeywords) {
		return string(CategoryUtil)
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

// generateUniqueName generates a unique service name by appending a numeric suffix if needed
// It checks the database for existing names and returns a unique one
func (s *Service) generateUniqueName(ctx context.Context, baseName string) string {
	// First check if base name is available
	var count int64
	s.db.WithContext(ctx).Model(&MCPService{}).Where("name = ?", baseName).Count(&count)
	if count == 0 {
		return baseName
	}

	// Try with numeric suffix: name-2, name-3, etc.
	for i := 2; i <= 100; i++ {
		candidateName := fmt.Sprintf("%s-%d", baseName, i)
		s.db.WithContext(ctx).Model(&MCPService{}).Where("name = ?", candidateName).Count(&count)
		if count == 0 {
			return candidateName
		}
	}

	// Fallback: append timestamp
	return fmt.Sprintf("%s-%d", baseName, time.Now().UnixNano()%1000000)
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
		} else {
			// Invalidate service list cache so tool_count is refreshed in list view
			s.InvalidateServiceListCache()
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

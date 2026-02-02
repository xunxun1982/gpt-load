package centralizedmgmt

import (
	"time"

	"gorm.io/datatypes"
)

// HubAccessKey represents a Hub access key in the database.
// Key values are encrypted using encryption.Service before storage.
type HubAccessKey struct {
	ID            uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	Name          string         `gorm:"type:varchar(255);not null" json:"name"`
	KeyHash       string         `gorm:"type:varchar(64);not null;uniqueIndex:idx_hub_access_keys_key_hash" json:"-"` // SHA256 hash for lookup
	KeyValue      string         `gorm:"type:text;not null" json:"-"`                                                 // Encrypted, never exposed in JSON
	AllowedModels datatypes.JSON `gorm:"type:json;not null" json:"allowed_models"`                                    // JSON array, empty array means all models
	Enabled       bool           `gorm:"not null;default:true;index:idx_hub_access_keys_enabled" json:"enabled"`
	// Usage statistics
	UsageCount int64      `gorm:"not null;default:0" json:"usage_count"`                   // Total number of API calls
	LastUsedAt *time.Time `gorm:"index:idx_hub_access_keys_last_used" json:"last_used_at"` // Last usage timestamp
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TableName specifies the table name for HubAccessKey
func (HubAccessKey) TableName() string {
	return "hub_access_keys"
}

// HubAccessKeyDTO is the API response format with masked key value.
// Used for listing and displaying access keys without exposing actual key values.
type HubAccessKeyDTO struct {
	ID                uint       `json:"id"`
	Name              string     `json:"name"`
	MaskedKey         string     `json:"masked_key"`          // e.g., "sk-xxx...xxx"
	AllowedModels     []string   `json:"allowed_models"`      // Parsed from JSON
	AllowedModelsMode string     `json:"allowed_models_mode"` // "all" or "specific"
	Enabled           bool       `json:"enabled"`
	UsageCount        int64      `json:"usage_count"`  // Total API calls
	LastUsedAt        *time.Time `json:"last_used_at"` // Last usage timestamp
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// CreateAccessKeyParams defines parameters for creating a new Hub access key
type CreateAccessKeyParams struct {
	Name          string   `json:"name" binding:"required"`
	KeyValue      string   `json:"key_value,omitempty"`      // Optional, auto-generated if empty
	AllowedModels []string `json:"allowed_models,omitempty"` // Empty means all models
	Enabled       bool     `json:"enabled"`
}

// UpdateAccessKeyParams defines parameters for updating a Hub access key
type UpdateAccessKeyParams struct {
	Name          *string  `json:"name,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	Enabled       *bool    `json:"enabled,omitempty"`
}

// MaskKeyValue masks a key value for display, showing only first and last few characters.
// Example: "sk-abc123xyz789" -> "sk-abc...789"
func MaskKeyValue(keyValue string) string {
	if len(keyValue) <= 8 {
		return "***"
	}
	// Show first 6 chars and last 3 chars
	return keyValue[:6] + "..." + keyValue[len(keyValue)-3:]
}

// HubAccessKeyExportInfo represents exported Hub access key data.
// Key values remain encrypted (same as database storage) for security.
type HubAccessKeyExportInfo struct {
	Name          string   `json:"name"`
	KeyValue      string   `json:"key_value"`      // Encrypted value (same as storage)
	AllowedModels []string `json:"allowed_models"` // Parsed from JSON for readability
	Enabled       bool     `json:"enabled"`
}

// BatchAccessKeyOperationParams defines parameters for batch operations on access keys.
type BatchAccessKeyOperationParams struct {
	IDs []uint `json:"ids" binding:"required,min=1"`
}

// BatchEnableDisableParams defines parameters for batch enable/disable operations.
type BatchEnableDisableParams struct {
	IDs     []uint `json:"ids" binding:"required,min=1"`
	Enabled bool   `json:"enabled"`
}

// HubModelGroupPriority stores priority configuration for a model in a specific group.
// Priority semantics: Lower value = Higher priority (1 is highest priority).
// Priority 1000 means disabled (skip this group for this model).
type HubModelGroupPriority struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelName string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_hub_model_group_priority" json:"model_name"`
	GroupID   uint      `gorm:"not null;uniqueIndex:idx_hub_model_group_priority" json:"group_id"`
	Priority  int       `gorm:"not null;default:100" json:"priority"` // 1-999=priority (lower=higher), 1000=disabled
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName specifies the table name for HubModelGroupPriority
func (HubModelGroupPriority) TableName() string {
	return "hub_model_group_priorities"
}

// HubSettings stores global Hub configuration.
type HubSettings struct {
	ID                  uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	MaxRetries          int       `gorm:"not null;default:3" json:"max_retries"`              // Max retries per priority level
	RetryDelay          int       `gorm:"not null;default:100" json:"retry_delay"`            // Delay between retries in ms
	HealthThreshold     float64   `gorm:"not null;default:0.5" json:"health_threshold"`       // Min health score for group selection
	EnablePriority      bool      `gorm:"not null;default:true" json:"enable_priority"`       // Enable priority-based routing
	OnlyAggregateGroups bool      `gorm:"not null;default:true" json:"only_aggregate_groups"` // Only accept aggregate groups for routing
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// TableName specifies the table name for HubSettings
func (HubSettings) TableName() string {
	return "hub_settings"
}

// HubSettingsDTO is the API response format for Hub settings.
type HubSettingsDTO struct {
	MaxRetries          int     `json:"max_retries"`
	RetryDelay          int     `json:"retry_delay"`
	HealthThreshold     float64 `json:"health_threshold"`
	EnablePriority      bool    `json:"enable_priority"`
	OnlyAggregateGroups bool    `json:"only_aggregate_groups"`
}

// ModelGroupPriorityDTO is the API response format for model-group priority.
type ModelGroupPriorityDTO struct {
	GroupID      uint    `json:"group_id"`
	GroupName    string  `json:"group_name"`
	GroupType    string  `json:"group_type"`     // "standard" or "aggregate"
	IsChildGroup bool    `json:"is_child_group"` // True if this is a child group of a standard group
	ChannelType  string  `json:"channel_type"`   // Channel type (e.g., "openai", "claude", etc.)
	Priority     int     `json:"priority"`
	HealthScore  float64 `json:"health_score"`
	Enabled      bool    `json:"enabled"` // Group enabled status
}

// ModelPoolEntryV2 represents a model with detailed group priority information.
type ModelPoolEntryV2 struct {
	ModelName string                  `json:"model_name"`
	Groups    []ModelGroupPriorityDTO `json:"groups"`
	IsCustom  bool                    `json:"is_custom"` // True if this is a custom model defined by user
}

// UpdateModelGroupPriorityParams defines parameters for updating model-group priority.
type UpdateModelGroupPriorityParams struct {
	ModelName string `json:"model_name" binding:"required"`
	GroupID   uint   `json:"group_id" binding:"required"`
	Priority  int    `json:"priority"` // 1-999=priority (lower=higher), 1000=disabled
}

// AggregateGroupCustomModels represents custom model names for an aggregate group.
// This is used for managing custom models in the Hub centralized management UI.
type AggregateGroupCustomModels struct {
	GroupID          uint     `json:"group_id"`
	GroupName        string   `json:"group_name"`
	CustomModelNames []string `json:"custom_model_names"`
}

// UpdateCustomModelsParams defines parameters for updating custom model names.
type UpdateCustomModelsParams struct {
	GroupID          uint     `json:"group_id" binding:"required"`
	CustomModelNames []string `json:"custom_model_names"` // Empty array to clear
}

package i18n

// MessagesEnUS contains English translations for MCP Skills module
var MessagesEnUS = map[string]string{
	// Validation errors
	"mcp_skills.validation.name_required":        "Service name is required",
	"mcp_skills.validation.invalid_name_format":  "Service name must start with a letter and contain only letters, numbers, hyphens, and underscores",
	"mcp_skills.validation.name_duplicate":       "Service name '{{.name}}' already exists",
	"mcp_skills.validation.group_name_required":  "Group name is required",
	"mcp_skills.validation.group_name_duplicate": "Group name '{{.name}}' already exists",
	"mcp_skills.validation.invalid_service_ids":  "One or more service IDs are invalid",
	"mcp_skills.validation.service_in_use":       "Cannot delete service that is used in a group",

	// Success messages
	"mcp_skills.service_created":           "MCP service created successfully",
	"mcp_skills.service_updated":           "MCP service updated successfully",
	"mcp_skills.service_deleted":           "MCP service deleted successfully",
	"mcp_skills.services_deleted_all":      "Deleted {{.deleted}} unused services",
	"mcp_skills.service_toggled":           "Service status changed to {{.status}}",
	"mcp_skills.group_toggled":             "Group status changed to {{.status}}",
	"mcp_skills.mcp_toggled":               "MCP endpoint status changed to {{.status}}",
	"mcp_skills.group_created":             "Service group created successfully",
	"mcp_skills.group_updated":             "Service group updated successfully",
	"mcp_skills.group_deleted":             "Service group deleted successfully",
	"mcp_skills.skill_exported":            "Skill package exported successfully",
	"mcp_skills.token_regenerated":         "Access token regenerated successfully",
	"mcp_skills.import_completed":          "Import completed successfully",
	"mcp_skills.mcp_json_import_completed": "MCP JSON import completed",

	// Error messages
	"mcp_skills.service_not_found":     "MCP service not found",
	"mcp_skills.group_not_found":       "Service group not found",
	"mcp_skills.template_not_found":    "API bridge template not found",
	"mcp_skills.export_failed":         "Failed to export skill package",
	"mcp_skills.invalid_access_token":  "Invalid access token",
	"mcp_skills.missing_access_token":  "Aggregation endpoint has no access token configured",
	"mcp_skills.mcp_not_enabled":       "MCP endpoint is not enabled for this service",
	"mcp_skills.service_disabled":      "Service is disabled",

	// Status
	"mcp_skills.status.enabled":  "enabled",
	"mcp_skills.status.disabled": "disabled",
}

package i18n

// MessagesZhCN contains Chinese (Simplified) translations for MCP Skills module
var MessagesZhCN = map[string]string{
	// Validation errors
	"mcp_skills.validation.name_required":        "服务名称不能为空",
	"mcp_skills.validation.invalid_name_format":  "服务名称必须以字母开头，只能包含字母、数字、连字符和下划线",
	"mcp_skills.validation.name_duplicate":       "服务名称「{{.name}}」已存在",
	"mcp_skills.validation.group_name_required":  "分组名称不能为空",
	"mcp_skills.validation.group_name_duplicate": "分组名称「{{.name}}」已存在",
	"mcp_skills.validation.invalid_service_ids":  "一个或多个服务ID无效",
	"mcp_skills.validation.service_in_use":       "无法删除正在被分组使用的服务",

	// Success messages
	"mcp_skills.service_created":           "MCP服务创建成功",
	"mcp_skills.service_updated":           "MCP服务更新成功",
	"mcp_skills.service_deleted":           "MCP服务删除成功",
	"mcp_skills.services_deleted_all":      "已删除 {{.deleted}} 个未使用的服务",
	"mcp_skills.service_toggled":           "服务状态已更改为{{.status}}",
	"mcp_skills.group_toggled":             "分组状态已更改为{{.status}}",
	"mcp_skills.mcp_toggled":               "MCP端点状态已更改为{{.status}}",
	"mcp_skills.group_created":             "服务分组创建成功",
	"mcp_skills.group_updated":             "服务分组更新成功",
	"mcp_skills.group_deleted":             "服务分组删除成功",
	"mcp_skills.skill_exported":            "Skill包导出成功",
	"mcp_skills.token_regenerated":         "访问令牌已重新生成",
	"mcp_skills.import_completed":          "导入完成",
	"mcp_skills.mcp_json_import_completed": "MCP JSON 导入完成",

	// Error messages
	"mcp_skills.service_not_found":     "MCP服务不存在",
	"mcp_skills.group_not_found":       "服务分组不存在",
	"mcp_skills.template_not_found":    "API桥接模板不存在",
	"mcp_skills.export_failed":         "Skill包导出失败",
	"mcp_skills.invalid_access_token":  "访问令牌无效",
	"mcp_skills.missing_access_token":  "聚合端点未配置访问令牌",
	"mcp_skills.mcp_not_enabled":       "该服务未启用MCP端点",
	"mcp_skills.service_disabled":      "服务已禁用",

	// Status
	"mcp_skills.status.enabled":  "启用",
	"mcp_skills.status.disabled": "禁用",
}

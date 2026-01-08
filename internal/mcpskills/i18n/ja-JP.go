package i18n

// MessagesJaJP contains Japanese translations for MCP Skills module
var MessagesJaJP = map[string]string{
	// Validation errors
	"mcp_skills.validation.name_required":        "サービス名は必須です",
	"mcp_skills.validation.invalid_name_format":  "サービス名は英字で始まり、英数字、ハイフン、アンダースコアのみ使用できます",
	"mcp_skills.validation.name_duplicate":       "サービス名「{{.name}}」は既に存在します",
	"mcp_skills.validation.group_name_required":  "グループ名は必須です",
	"mcp_skills.validation.group_name_duplicate": "グループ名「{{.name}}」は既に存在します",
	"mcp_skills.validation.invalid_service_ids":  "1つ以上のサービスIDが無効です",
	"mcp_skills.validation.service_in_use":       "グループで使用中のサービスは削除できません",

	// Success messages
	"mcp_skills.service_created":           "MCPサービスが作成されました",
	"mcp_skills.service_updated":           "MCPサービスが更新されました",
	"mcp_skills.service_deleted":           "MCPサービスが削除されました",
	"mcp_skills.services_deleted_all":      "{{.deleted}}件の未使用サービスを削除しました",
	"mcp_skills.service_toggled":           "サービスのステータスが{{.status}}に変更されました",
	"mcp_skills.group_toggled":             "グループのステータスが{{.status}}に変更されました",
	"mcp_skills.mcp_toggled":               "MCPエンドポイントのステータスが{{.status}}に変更されました",
	"mcp_skills.group_created":             "サービスグループが作成されました",
	"mcp_skills.group_updated":             "サービスグループが更新されました",
	"mcp_skills.group_deleted":             "サービスグループが削除されました",
	"mcp_skills.skill_exported":            "Skillパッケージがエクスポートされました",
	"mcp_skills.token_regenerated":         "アクセストークンが再生成されました",
	"mcp_skills.import_completed":          "インポートが完了しました",
	"mcp_skills.mcp_json_import_completed": "MCP JSONインポートが完了しました",
	"mcp_skills.tools_refreshed":           "ツールリストが更新されました",

	// Runtime management
	"mcp_skills.runtime_installed":   "ランタイム {{.runtime}} がインストールされました",
	"mcp_skills.runtime_uninstalled": "ランタイム {{.runtime}} がアンインストールされました",
	"mcp_skills.runtime_upgraded":    "ランタイム {{.runtime}} がアップグレードされました",
	"mcp_skills.package_installed":   "パッケージ {{.package}} がインストールされました",
	"mcp_skills.package_uninstalled": "パッケージ {{.package}} がアンインストールされました",

	// Error messages
	"mcp_skills.service_not_found":     "MCPサービスが見つかりません",
	"mcp_skills.group_not_found":       "サービスグループが見つかりません",
	"mcp_skills.template_not_found":    "APIブリッジテンプレートが見つかりません",
	"mcp_skills.export_failed":         "Skillパッケージのエクスポートに失敗しました",
	"mcp_skills.invalid_access_token":  "アクセストークンが無効です",
	"mcp_skills.missing_access_token":  "集約エンドポイントにアクセストークンが設定されていません",
	"mcp_skills.mcp_not_enabled":       "このサービスのMCPエンドポイントは有効になっていません",
	"mcp_skills.service_disabled":      "サービスは無効です",

	// Status
	"mcp_skills.status.enabled":  "有効",
	"mcp_skills.status.disabled": "無効",
}

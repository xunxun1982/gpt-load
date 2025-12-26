package sitemanagementi18n

// MessagesJaJP contains site management related translations.
var MessagesJaJP = map[string]string{
	"site_management.validation.name_required":                 "名前は必須です",
	"site_management.validation.name_duplicate":                "サイト名「{{.name}}」は既に存在します",
	"site_management.validation.invalid_base_url":              "無効なベースURL: {{.error}}",
	"site_management.validation.invalid_auth_type":             "無効な認証タイプ",
	"site_management.validation.auth_value_requires_auth_type": "認証情報を設定するには認証タイプが必要です",
	"site_management.validation.time_window_required":          "時間ウィンドウが必要です",
	"site_management.validation.invalid_time":                  "無効な時間: {{.field}}",
	"site_management.validation.invalid_schedule_mode":         "無効なスケジュールモード",
	"site_management.validation.deterministic_time_required":   "確定モードでは固定時間が必要です",
}

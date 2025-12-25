package sitemanagementi18n

// MessagesZhCN contains site management related translations.
var MessagesZhCN = map[string]string{
	"site_management.validation.name_required":                 "名称为必填项",
	"site_management.validation.name_duplicate":                "站点名称「{{.name}}」已存在",
	"site_management.validation.invalid_base_url":              "无效的站点链接：{{.error}}",
	"site_management.validation.invalid_auth_type":             "无效的认证类型",
	"site_management.validation.auth_value_requires_auth_type": "填写认证信息时必须选择认证类型",
	"site_management.validation.time_window_required":          "需要设置时间窗口",
	"site_management.validation.invalid_time":                  "无效的时间字段：{{.field}}",
	"site_management.validation.invalid_schedule_mode":         "无效的调度模式",
	"site_management.validation.deterministic_time_required":   "确定性模式下需要设置固定时间",
}

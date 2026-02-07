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
	"site_management.validation.duplicate_time":                "签到时间「{{.time}}」重复",
	"site_management.validation.schedule_times_required":       "至少需要设置一个签到时间",

	// Check-in messages
	"site_management.checkin.failed":                    "签到失败",
	"site_management.checkin.disabled":                  "签到已禁用",
	"site_management.checkin.stealth_requires_cookie":   "隐身绕过需要使用Cookie认证",
	"site_management.checkin.missing_cf_cookies":        "缺少CF Cookies，需要以下之一：{{.cookies}}",
	"site_management.checkin.cloudflare_challenge":      "Cloudflare验证，请从浏览器更新Cookies",
	"site_management.checkin.anyrouter_requires_cookie": "Anyrouter需要使用Cookie认证",

	// HTTP error messages
	"site_management.checkin.http_400": "HTTP 400: 错误请求 - 请检查API端点和请求格式",
	"site_management.checkin.http_401": "HTTP 401: 未授权 - Cookie或令牌已过期/无效",
	"site_management.checkin.http_403": "HTTP 403: 禁止访问 - 访问被拒绝，请检查权限或更新Cookies",
	"site_management.checkin.http_404": "HTTP 404: 未找到 - 签到端点不存在，请验证基础URL",
	"site_management.checkin.http_429": "HTTP 429: 请求过多 - 已被限流，请稍后重试",
	"site_management.checkin.http_500": "HTTP 500: 内部服务器错误 - 站点API错误",
	"site_management.checkin.http_502": "HTTP 502: 网关错误 - 站点暂时不可用",
	"site_management.checkin.http_503": "HTTP 503: 服务不可用 - 站点维护中",
	"site_management.checkin.http_xxx": "HTTP {{.code}}: 请求失败",
}

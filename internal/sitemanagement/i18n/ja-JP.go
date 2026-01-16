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
	"site_management.validation.duplicate_time":                "スケジュール時間「{{.time}}」が重複しています",
	"site_management.validation.schedule_times_required":       "少なくとも1つのスケジュール時間が必要です",

	// Check-in messages
	"site_management.checkin.failed":                    "チェックイン失敗",
	"site_management.checkin.disabled":                  "チェックイン無効",
	"site_management.checkin.stealth_requires_cookie":   "ステルスバイパスにはCookie認証が必要です",
	"site_management.checkin.missing_cf_cookies":        "CF Cookiesが不足しています。次のいずれかが必要: {{.cookies}}",
	"site_management.checkin.cloudflare_challenge":      "Cloudflareチャレンジ、ブラウザからCookiesを更新してください",
	"site_management.checkin.anyrouter_requires_cookie": "AnyrouterにはCookie認証が必要です",
}

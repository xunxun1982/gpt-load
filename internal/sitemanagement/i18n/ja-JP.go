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

	// HTTP error messages
	"site_management.checkin.http_400": "HTTP 400: 不正なリクエスト - APIエンドポイントとリクエスト形式を確認してください",
	"site_management.checkin.http_401": "HTTP 401: 認証エラー - Cookieまたはトークンが期限切れ/無効です",
	"site_management.checkin.http_403": "HTTP 403: アクセス禁止 - アクセスが拒否されました。権限を確認するかCookiesを更新してください",
	"site_management.checkin.http_404": "HTTP 404: 見つかりません - チェックインエンドポイントが見つかりません。ベースURLを確認してください",
	"site_management.checkin.http_429": "HTTP 429: リクエスト過多 - レート制限されています。後でもう一度お試しください",
	"site_management.checkin.http_500": "HTTP 500: 内部サーバーエラー - サイトAPIエラー",
	"site_management.checkin.http_502": "HTTP 502: ゲートウェイエラー - サイトが一時的に利用できません",
	"site_management.checkin.http_503": "HTTP 503: サービス利用不可 - サイトメンテナンス中",
	"site_management.checkin.http_xxx": "HTTP {{.code}}: リクエスト失敗",
}

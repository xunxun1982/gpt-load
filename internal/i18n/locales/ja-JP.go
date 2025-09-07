package locales

// Messages 日本語翻訳
var MessagesJaJP = map[string]string{
	// 共通メッセージ
	"success":        "操作成功",
	"error":          "操作失敗",
	"unauthorized":   "未認証",
	"forbidden":      "アクセス拒否",
	"not_found":      "見つかりません",
	"bad_request":    "不正なリクエスト",
	"internal_error": "内部エラー",
	"invalid_param":  "無効なパラメータ",
	"required_field": "必須フィールド",

	// 認証関連
	"auth.invalid_key":    "無効な認証キー",
	"auth.key_required":   "認証キーが必要です",
	"auth.login_success":  "ログイン成功",
	"auth.logout_success": "ログアウト成功",

	// グループ関連
	"group.created":     "グループが作成されました",
	"group.updated":     "グループが更新されました",
	"group.deleted":     "グループが削除されました",
	"group.not_found":   "グループが存在しません",
	"group.name_exists": "グループ名が既に存在します",

	// キー関連
	"key.created":         "キーが作成されました",
	"key.updated":         "キーが更新されました",
	"key.deleted":         "キーが削除されました",
	"key.not_found":       "キーが存在しません",
	"key.invalid":         "無効なキー",
	"key.check_started":   "キーチェックが開始されました",
	"key.check_completed": "キーチェックが完了しました",

	// 設定関連
	"settings.updated": "設定が更新されました",
	"settings.reset":   "設定がリセットされました",

	// ログ関連
	"logs.cleared":  "ログがクリアされました",
	"logs.exported": "ログがエクスポートされました",

	// 検証関連
	"validation.invalid_group_name":      "無効なグループ名。小文字、数字、ハイフン、アンダースコアのみ使用可能、1-100文字",
	"validation.invalid_test_path":       "無効なテストパス。指定する場合は / で始まる有効なパスであり、完全なURLではない必要があります。",
	"validation.duplicate_header":        "重複ヘッダー: {key}",
	"validation.group_not_found":         "グループが見つかりません",
	"validation.invalid_status_filter":   "無効なステータスフィルター",
	"validation.invalid_group_id":        "無効なグループID形式",
	"validation.test_model_required":     "テストモデルが必要です",
	"validation.invalid_copy_keys_value": "無効なcopy_keys値。'none'、'valid_only'、'all'のいずれかである必要があります",
	"validation.invalid_channel_type":    "無効なチャンネルタイプ。サポートされるタイプ: {types}",
	"validation.test_model_empty":        "テストモデルは空またはスペースのみにできません",
	"validation.invalid_status_value":    "無効なステータス値",

	// タスク関連
	"task.validation_started": "キー検証タスクが開始されました",
	"task.import_started":     "キーインポートタスクが開始されました",
	"task.delete_started":     "キー削除タスクが開始されました",
	"task.already_running":    "タスクが既に実行中です",
	"task.get_status_failed":  "タスクステータスの取得に失敗しました",

	// ダッシュボード関連
	"dashboard.invalid_keys":               "無効なキー",
	"dashboard.success_requests":           "成功",
	"dashboard.failed_requests":            "失敗",
	"dashboard.auth_key_missing":           "AUTH_KEYが設定されていません。システムが正常に動作しません",
	"dashboard.auth_key_required":          "管理インターフェースを保護するためAUTH_KEYを設定する必要があります",
	"dashboard.encryption_key_missing":     "ENCRYPTION_KEYが設定されていません。機密データがプレーンテキストで保存されます",
	"dashboard.encryption_key_recommended": "APIキーなどの機密データを暗号化するため、ENCRYPTION_KEYの設定を強く推奨します",

	// データベース関連
	"database.cannot_get_groups":     "グループリストを取得できません",
	"database.rpm_stats_failed":      "RPM統計の取得に失敗しました",
	"database.current_stats_failed":  "現在の期間統計の取得に失敗しました",
	"database.previous_stats_failed": "前の期間統計の取得に失敗しました",
	"database.chart_data_failed":     "チャートデータの取得に失敗しました",
	"database.group_stats_failed":    "部分統計の取得に失敗しました",

	// 成功メッセージ
	"success.group_deleted":        "グループと関連キーが正常に削除されました",
	"success.keys_restored":        "{count}個のキーが復元されました",
	"success.invalid_keys_cleared": "{count}個の無効なキーがクリアされました",
	"success.all_keys_cleared":     "{count}個のキーがクリアされました",

	// パスワードセキュリティ関連
	"security.password_too_short":         "{keyType}が短すぎます（{length}文字）。少なくとも16文字を推奨します",
	"security.password_short":             "{keyType}が短いです（{length}文字）。32文字以上を推奨します",
	"security.password_weak_pattern":      "{keyType}に一般的な弱いパスワードパターンが含まれています: {pattern}",
	"security.password_low_complexity":    "{keyType}の複雑性が低く、大文字/小文字、数字、特殊文字の組み合わせが不足しています",
	"security.password_recommendation_16": "少なくとも16文字の強力なパスワードを使用してください。32文字以上を推奨します",
	"security.password_recommendation_32": "セキュリティ向上のため32文字以上のパスワードを推奨します",
	"security.password_avoid_common":      "一般的な単語は避け、ランダムに生成された強力なパスワードの使用を推奨します",
	"security.password_complexity":        "パスワード強度を向上させるため、大文字/小文字、数字、特殊文字を含めることを推奨します",

	// 設定関連
	"config.updated":                          "設定が正常に更新されました",
	"config.app_url":                          "アプリケーションURL",
	"config.app_url_desc":                     "アプリケーションのベースURL。グループエンドポイントアドレスの構築に使用されます。システム設定が環境変数APP_URLより優先されます。",
	"config.proxy_keys":                       "グローバルプロキシキー",
	"config.proxy_keys_desc":                  "すべてのグループプロキシエンドポイントにアクセスするためのグローバルプロキシキー。複数のキーはカンマで区切ります。",
	"config.log_retention_days":               "ログ保存期間（日）",
	"config.log_retention_days_desc":          "データベースにリクエストログを保持する日数、0でログを永久保存。",
	"config.log_write_interval":               "ログ書き込み間隔（分）",
	"config.log_write_interval_desc":          "リクエストログをキャッシュからデータベースに書き込む間隔（分）、0でリアルタイム書き込み。",
	"config.enable_request_body_logging":      "リクエストボディログを有効化",
	"config.enable_request_body_logging_desc": "完全なリクエストボディの内容をログに記録するかどうか。有効にするとメモリとストレージの使用量が増加します。",

	// リクエスト設定関連
	"config.request_timeout":              "リクエストタイムアウト（秒）",
	"config.request_timeout_desc":         "転送リクエストの完全なライフサイクルタイムアウト（秒）。",
	"config.connect_timeout":              "接続タイムアウト（秒）",
	"config.connect_timeout_desc":         "上流サービスへの新しい接続を確立するためのタイムアウト（秒）。",
	"config.idle_conn_timeout":            "アイドル接続タイムアウト（秒）",
	"config.idle_conn_timeout_desc":       "HTTPクライアントのアイドル接続のタイムアウト（秒）。",
	"config.response_header_timeout":      "レスポンスヘッダータイムアウト（秒）",
	"config.response_header_timeout_desc": "上流サービスからのレスポンスヘッダーを待つ最大時間（秒）。",
	"config.max_idle_conns":               "最大アイドル接続数",
	"config.max_idle_conns_desc":          "HTTPクライアント接続プールで許可される最大アイドル接続総数。",
	"config.max_idle_conns_per_host":      "ホストごとの最大アイドル接続数",
	"config.max_idle_conns_per_host_desc": "HTTPクライアント接続プールで各上流ホストに許可される最大アイドル接続数。",
	"config.proxy_url":                    "プロキシサーバーURL",
	"config.proxy_url_desc":               "グローバルHTTP/HTTPSプロキシサーバーURL。例：http://user:pass@host:port。空の場合は環境変数設定を使用。",

	// キー設定関連
	"config.max_retries":                     "最大リトライ数",
	"config.max_retries_desc":                "異なるキーを使用した単一リクエストの最大リトライ数、0でリトライなし。",
	"config.blacklist_threshold":             "ブラックリストしきい値",
	"config.blacklist_threshold_desc":        "キーがブラックリストに入るまでの連続失敗回数、0でブラックリスト無効。",
	"config.key_validation_interval":         "キー検証間隔（分）",
	"config.key_validation_interval_desc":    "バックグラウンドキー検証のデフォルト間隔（分）。",
	"config.key_validation_concurrency":      "キー検証並行数",
	"config.key_validation_concurrency_desc": "バックグラウンドで無効なキーを検証する際の並行数。SQLiteや低性能環境では20以下を維持し、データ不整合を回避してください。",
	"config.key_validation_timeout":          "キー検証タイムアウト（秒）",
	"config.key_validation_timeout_desc":     "バックグラウンドで単一キーを検証する際のAPIリクエストタイムアウト（秒）。",

	// カテゴリラベル
	"config.category.basic":   "基本設定",
	"config.category.request": "リクエスト設定",
	"config.category.key":     "キー設定",

	// 内部エラーメッセージ（fmt.Errorf用）
	"error.upstreams_required":       "upstreamsフィールドは必須です",
	"error.invalid_upstreams_format": "無効なupstreams形式",
	"error.at_least_one_upstream":    "少なくとも1つのupstreamが必要です",
	"error.upstream_url_empty":       "upstream URLは空にできません",
	"error.invalid_upstream_url":     "無効なupstream URL形式: {url}",
	"error.upstream_weight_positive": "upstreamの重みは正の整数である必要があります",
	"error.marshal_upstreams_failed": "クリーンアップされたupstreamsのシリアル化に失敗しました",
	"error.unknown_config_field":     "未知の設定フィールド: '{field}'",
}

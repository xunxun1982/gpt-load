// Package i18n provides internationalization support for centralized management
package i18n

// MessagesJaJP contains Japanese translations for centralized management
var MessagesJaJP = map[string]string{
	// Hub access key related
	"hub.access_key.created":      "Hubアクセスキーが正常に作成されました",
	"hub.access_key.updated":      "Hubアクセスキーが正常に更新されました",
	"hub.access_key.deleted":      "Hubアクセスキーが正常に削除されました",
	"hub.access_key.not_found":    "Hubアクセスキーが見つかりません",
	"hub.access_key.invalid":      "無効なHubアクセスキー",
	"hub.access_key.disabled":     "Hubアクセスキーは無効です",
	"hub.access_key.name_exists":  "Hubアクセスキー名は既に存在します",
	"hub.access_key.key_required": "アクセスキーが必要です",

	// Hub model pool related
	"hub.model_pool.updated":           "モデルプールが正常に更新されました",
	"hub.model_pool.priority_updated":  "モデルグループの優先度が正常に更新されました",
	"hub.model_pool.invalid_priority":  "優先度は1から999の間である必要があります（1000はシステム内部予約値）",
	"hub.model_pool.model_not_found":   "プールにモデルが見つかりません",
	"hub.model_pool.no_healthy_groups": "モデルに利用可能な正常なグループがありません",

	// Hub settings related
	"hub.settings.updated":              "Hub設定が正常に更新されました",
	"hub.settings.invalid_threshold":    "ヘルス閾値は0から1の間である必要があります",
	"hub.settings.invalid_retry_config": "無効な再試行設定",

	// Hub routing related
	"hub.routing.model_required":         "リクエストにモデルが必要です",
	"hub.routing.model_not_allowed":      "アクセスキーでモデルが許可されていません",
	"hub.routing.model_not_available":    "どのグループでもモデルが利用できません",
	"hub.routing.group_selection_failed": "モデルのグループ選択に失敗しました",
	"hub.routing.no_healthy_group":       "モデルに利用可能な正常なグループがありません",

	// Hub routing logic description
	"hub.routing.logic.title":       "Hub ルーティングロジック",
	"hub.routing.logic.description": "リクエストルーティングは次の順序で実行されます",
	"hub.routing.logic.step1":       "① パス形式検出：API 形式を識別（Chat/Claude/Gemini/Image/Audio）。不明な形式は OpenAI にフォールバックします。",
	"hub.routing.logic.step2":       "② モデル抽出：リクエストからモデル名を抽出（形式認識）",
	"hub.routing.logic.step3":       "③ アクセス制御：モデルに対するアクセスキーの権限を検証",
	"hub.routing.logic.step4":       "④ モデル可用性：有効なグループにモデルが存在するか確認",
	"hub.routing.logic.step5":       "⑤ グループ選択フィルター：ヘルス閾値 + 有効状態 + チャネル互換性 + CC サポート（Claude 形式）+ 集約グループ前提条件（リクエストサイズ制限など）",
	"hub.routing.logic.step6":       "⑥ チャネル優先度：ネイティブチャネル > 互換チャネル",
	"hub.routing.logic.step7":       "⑦ グループ選択：最小 priority 値（値が小さいほど優先度が高い）→ ヘルススコア加重ランダム選択",
	"hub.routing.logic.step8":       "⑧ パス書き換えと転送：/hub/v1/* → /proxy/{グループ名}/v1/*",
	"hub.routing.logic.note":        "注意：最初にモデル名をマッチングして利用可能なグループ範囲を決定し、その後パス形式を使用してチャネル互換性フィルタリングを行います。",

	// Channel types
	"channel.type.openai":    "OpenAI",
	"channel.type.anthropic": "Anthropic",
	"channel.type.gemini":    "Gemini",
	"channel.type.codex":     "Codex",
	"channel.type.azure":     "Azure",
	"channel.type.custom":    "カスタム",

	// Relay formats
	"relay_format.openai_chat":                "OpenAI チャット補完",
	"relay_format.openai_completion":          "OpenAI テキスト補完",
	"relay_format.claude":                     "Claude メッセージ",
	"relay_format.codex":                      "Codex レスポンス",
	"relay_format.openai_image":               "OpenAI 画像生成",
	"relay_format.openai_image_edit":          "OpenAI 画像編集",
	"relay_format.openai_audio_transcription": "OpenAI 音声文字起こし",
	"relay_format.openai_audio_translation":   "OpenAI 音声翻訳",
	"relay_format.openai_audio_speech":        "OpenAI 音声合成",
	"relay_format.openai_embedding":           "OpenAI 埋め込み",
	"relay_format.openai_moderation":          "OpenAI モデレーション",
	"relay_format.gemini":                     "Gemini",
	"relay_format.unknown":                    "不明な形式（OpenAIにフォールバック）",

	// Endpoint descriptions
	"endpoint.chat_completions":     "チャット補完",
	"endpoint.completions":          "テキスト補完",
	"endpoint.messages":             "メッセージ",
	"endpoint.responses":            "レスポンス",
	"endpoint.images_generations":   "画像生成",
	"endpoint.images_edits":         "画像編集",
	"endpoint.images_variations":    "画像バリエーション",
	"endpoint.audio_transcriptions": "音声文字起こし",
	"endpoint.audio_translations":   "音声翻訳",
	"endpoint.audio_speech":         "音声合成",
	"endpoint.embeddings":           "埋め込み",
	"endpoint.moderations":          "コンテンツモデレーション",
}

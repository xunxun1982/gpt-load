// Package i18n provides internationalization support for centralized management
package i18n

// MessagesZhCN contains Chinese (Simplified) translations for centralized management
var MessagesZhCN = map[string]string{
	// Hub access key related
	"hub.access_key.created":      "Hub 访问密钥创建成功",
	"hub.access_key.updated":      "Hub 访问密钥更新成功",
	"hub.access_key.deleted":      "Hub 访问密钥删除成功",
	"hub.access_key.not_found":    "Hub 访问密钥不存在",
	"hub.access_key.invalid":      "无效的 Hub 访问密钥",
	"hub.access_key.disabled":     "Hub 访问密钥已禁用",
	"hub.access_key.name_exists":  "Hub 访问密钥名称已存在",
	"hub.access_key.key_required": "需要访问密钥",

	// Hub model pool related
	"hub.model_pool.updated":           "模型池更新成功",
	"hub.model_pool.priority_updated":  "模型组优先级更新成功",
	"hub.model_pool.invalid_priority":  "优先级必须在 1 到 999 之间（1000 为系统内部保留值）",
	"hub.model_pool.model_not_found":   "模型池中未找到该模型",
	"hub.model_pool.no_healthy_groups": "该模型没有可用的健康组",

	// Hub settings related
	"hub.settings.updated":              "Hub 设置更新成功",
	"hub.settings.invalid_threshold":    "健康阈值必须在 0 到 1 之间",
	"hub.settings.invalid_retry_config": "无效的重试配置",

	// Hub routing related
	"hub.routing.model_required":         "请求中必须指定模型",
	"hub.routing.model_not_allowed":      "访问密钥不允许使用该模型",
	"hub.routing.model_not_available":    "该模型在任何组中都不可用",
	"hub.routing.group_selection_failed": "为模型选择组失败",
	"hub.routing.no_healthy_group":       "该模型没有可用的健康组",

	// Hub routing logic description
	"hub.routing.logic.title":       "Hub 路由逻辑",
	"hub.routing.logic.description": "请求路由按以下顺序执行",
	"hub.routing.logic.step1":       "① 路径格式识别：识别 API 格式（Chat/Claude/Gemini/Image/Audio）。未知格式默认使用 OpenAI。",
	"hub.routing.logic.step2":       "② 模型提取：从请求中提取模型名称（格式感知）",
	"hub.routing.logic.step3":       "③ 访问控制：验证访问密钥对该模型的权限",
	"hub.routing.logic.step4":       "④ 模型可用性：检查模型是否存在于任何启用的分组中",
	"hub.routing.logic.step5":       "⑤ 分组选择过滤：健康阈值 + 启用状态 + 渠道兼容性 + Claude Code 支持 + 聚合分组前置条件（请求大小限制等）",
	"hub.routing.logic.step6":       "⑥ 渠道优先级：原生渠道 > 兼容渠道",
	"hub.routing.logic.step7":       "⑦ 分组选择：最小 priority 值（数值越小优先级越高）→ 健康度加权随机选择",
	"hub.routing.logic.step8":       "⑧ 路径重写并转发：/hub/v1/* → /proxy/{分组名}/v1/*",
	"hub.routing.logic.note":        "注意：首先匹配模型名称以确定可用的分组范围，然后使用路径格式进行渠道兼容性过滤。",

	// Channel types
	"channel.type.openai":    "OpenAI",
	"channel.type.anthropic": "Anthropic",
	"channel.type.gemini":    "Gemini",
	"channel.type.codex":     "Codex",
	"channel.type.azure":     "Azure",
	"channel.type.custom":    "自定义",

	// Relay formats
	"relay_format.openai_chat":                "OpenAI 对话补全",
	"relay_format.openai_completion":          "OpenAI 文本补全",
	"relay_format.claude":                     "Claude 消息",
	"relay_format.codex":                      "Codex 响应",
	"relay_format.openai_image":               "OpenAI 图片生成",
	"relay_format.openai_image_edit":          "OpenAI 图片编辑",
	"relay_format.openai_audio_transcription": "OpenAI 音频转录",
	"relay_format.openai_audio_translation":   "OpenAI 音频翻译",
	"relay_format.openai_audio_speech":        "OpenAI 语音合成",
	"relay_format.openai_embedding":           "OpenAI 向量嵌入",
	"relay_format.openai_moderation":          "OpenAI 内容审核",
	"relay_format.gemini":                     "Gemini",
	"relay_format.unknown":                    "未知格式（默认使用 OpenAI）",

	// Endpoint descriptions
	"endpoint.chat_completions":     "对话补全",
	"endpoint.completions":          "文本补全",
	"endpoint.messages":             "消息",
	"endpoint.responses":            "响应",
	"endpoint.images_generations":   "图片生成",
	"endpoint.images_edits":         "图片编辑",
	"endpoint.images_variations":    "图片变体",
	"endpoint.audio_transcriptions": "音频转录",
	"endpoint.audio_translations":   "音频翻译",
	"endpoint.audio_speech":         "语音合成",
	"endpoint.embeddings":           "向量嵌入",
	"endpoint.moderations":          "内容审核",
}

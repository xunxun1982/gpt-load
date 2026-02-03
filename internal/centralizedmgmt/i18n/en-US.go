// Package i18n provides internationalization support for centralized management
package i18n

// MessagesEnUS contains English translations for centralized management
var MessagesEnUS = map[string]string{
	// Hub access key related
	"hub.access_key.created":      "Hub access key created successfully",
	"hub.access_key.updated":      "Hub access key updated successfully",
	"hub.access_key.deleted":      "Hub access key deleted successfully",
	"hub.access_key.not_found":    "Hub access key not found",
	"hub.access_key.invalid":      "Invalid hub access key",
	"hub.access_key.disabled":     "Hub access key is disabled",
	"hub.access_key.name_exists":  "Hub access key name already exists",
	"hub.access_key.key_required": "Access key is required",

	// Hub model pool related
	"hub.model_pool.updated":           "Model pool updated successfully",
	"hub.model_pool.priority_updated":  "Model group priority updated successfully",
	"hub.model_pool.invalid_priority":  "Priority must be between 1 and 999 (1000 is reserved for internal use)",
	"hub.model_pool.model_not_found":   "Model not found in pool",
	"hub.model_pool.no_healthy_groups": "No healthy groups available for model",

	// Hub settings related
	"hub.settings.updated":              "Hub settings updated successfully",
	"hub.settings.invalid_threshold":    "Health threshold must be between 0 and 1",
	"hub.settings.invalid_retry_config": "Invalid retry configuration",

	// Hub routing related
	"hub.routing.model_required":         "Model is required in request",
	"hub.routing.model_not_allowed":      "Model not allowed by access key",
	"hub.routing.model_not_available":    "Model not available in any group",
	"hub.routing.group_selection_failed": "Failed to select group for model",
	"hub.routing.no_healthy_group":       "No healthy groups available for model",

	// Hub routing logic description
	"hub.routing.logic.title":       "Hub Routing Logic",
	"hub.routing.logic.description": "Request routing follows these steps in order",
	"hub.routing.logic.step1":       "① Path Format Detection: Identify API format (Chat/Claude/Gemini/Image/Audio). Unknown formats fallback to OpenAI.",
	"hub.routing.logic.step2":       "② Model Extraction: Extract model name from request (format-aware)",
	"hub.routing.logic.step3":       "③ Access Control: Validate access key permissions for the model",
	"hub.routing.logic.step4":       "④ Model Availability: Check if model exists in any enabled group",
	"hub.routing.logic.step5":       "⑤ Group Selection Filters: Health threshold + Enabled status + Channel compatibility + Claude Code support + Aggregate group preconditions (request size limits, etc.)",
	"hub.routing.logic.step6":       "⑥ Channel Priority: Native channels > Compatible channels",
	"hub.routing.logic.step7":       "⑦ Group Selection: Minimum priority value (lower=higher) → Health-weighted random selection",
	"hub.routing.logic.step8":       "⑧ Path Rewrite & Forward: /hub/v1/* → /proxy/{group_name}/v1/*",
	"hub.routing.logic.note":        "Note: Model matching happens first to determine the available group range, then path format is used for channel compatibility filtering.",

	// Channel types
	"channel.type.openai":    "OpenAI",
	"channel.type.anthropic": "Anthropic",
	"channel.type.gemini":    "Gemini",
	"channel.type.codex":     "Codex",
	"channel.type.azure":     "Azure",
	"channel.type.custom":    "Custom",

	// Relay formats
	"relay_format.openai_chat":                "OpenAI Chat Completions",
	"relay_format.openai_completion":          "OpenAI Completions",
	"relay_format.claude":                     "Claude Messages",
	"relay_format.codex":                      "Codex Responses",
	"relay_format.openai_image":               "OpenAI Image Generation",
	"relay_format.openai_image_edit":          "OpenAI Image Editing",
	"relay_format.openai_audio_transcription": "OpenAI Audio Transcription",
	"relay_format.openai_audio_translation":   "OpenAI Audio Translation",
	"relay_format.openai_audio_speech":        "OpenAI Text-to-Speech",
	"relay_format.openai_embedding":           "OpenAI Embeddings",
	"relay_format.openai_moderation":          "OpenAI Moderation",
	"relay_format.gemini":                     "Gemini",
	"relay_format.unknown":                    "Unknown Format (defaults to OpenAI)",

	// Endpoint descriptions
	"endpoint.chat_completions":     "Chat Completions",
	"endpoint.completions":          "Text Completions",
	"endpoint.messages":             "Messages",
	"endpoint.responses":            "Responses",
	"endpoint.images_generations":   "Image Generation",
	"endpoint.images_edits":         "Image Editing",
	"endpoint.images_variations":    "Image Variations",
	"endpoint.audio_transcriptions": "Audio Transcription",
	"endpoint.audio_translations":   "Audio Translation",
	"endpoint.audio_speech":         "Text-to-Speech",
	"endpoint.embeddings":           "Embeddings",
	"endpoint.moderations":          "Content Moderation",
}

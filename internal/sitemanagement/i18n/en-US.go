package sitemanagementi18n

// MessagesEnUS contains site management related translations.
var MessagesEnUS = map[string]string{
	"site_management.validation.name_required":                 "Name is required",
	"site_management.validation.name_duplicate":                "Site name \"{{.name}}\" already exists",
	"site_management.validation.invalid_base_url":              "Invalid base URL: {{.error}}",
	"site_management.validation.invalid_auth_type":             "Invalid auth type",
	"site_management.validation.auth_value_requires_auth_type": "Auth value requires a non-none auth type",
	"site_management.validation.time_window_required":          "Time window is required",
	"site_management.validation.invalid_time":                  "Invalid time for {{.field}}",
	"site_management.validation.invalid_schedule_mode":         "Invalid schedule mode",
	"site_management.validation.deterministic_time_required":   "Deterministic time is required",
	"site_management.validation.duplicate_time":                "Duplicate schedule time: {{.time}}",
	"site_management.validation.schedule_times_required":       "At least one schedule time is required",
}

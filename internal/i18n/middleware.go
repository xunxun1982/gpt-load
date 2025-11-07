package i18n

import (
	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
)

const (
	// LocalizerKey 是 gin.Context 中存储 Localizer 的键
	LocalizerKey = "localizer"
	// LangKey 是 gin.Context 中存储当前语言的键
	LangKey = "lang"
)

// Middleware is the i18n middleware.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get Accept-Language header
		acceptLang := c.GetHeader("Accept-Language")

		// Get Localizer
		localizer := GetLocalizer(acceptLang)

		// Store Localizer in Context
		c.Set(LocalizerKey, localizer)

		// Store current language
		lang := normalizeLanguageCode(acceptLang)
		c.Set(LangKey, lang)

		c.Next()
	}
}

// GetLocalizerFromContext gets Localizer from gin.Context.
func GetLocalizerFromContext(c *gin.Context) *i18n.Localizer {
	if localizer, exists := c.Get(LocalizerKey); exists {
		if l, ok := localizer.(*i18n.Localizer); ok {
			return l
		}
	}
	// If not found, return default Chinese Localizer
	return GetLocalizer("zh-CN")
}

// GetLangFromContext gets current language from gin.Context.
func GetLangFromContext(c *gin.Context) string {
	if lang, exists := c.Get(LangKey); exists {
		if l, ok := lang.(string); ok {
			return l
		}
	}
	return "zh-CN"
}

// Success returns a success response (with internationalized message).
func Success(c *gin.Context, msgID string, data any) {
	localizer := GetLocalizerFromContext(c)
	message := T(localizer, msgID)

	c.JSON(200, gin.H{
		"success": true,
		"message": message,
		"data":    data,
		"lang":    GetLangFromContext(c),
	})
}

// SuccessWithData returns a success response (with template data).
func SuccessWithData(c *gin.Context, msgID string, templateData map[string]any, data any) {
	localizer := GetLocalizerFromContext(c)
	message := T(localizer, msgID, templateData)

	c.JSON(200, gin.H{
		"success": true,
		"message": message,
		"data":    data,
		"lang":    GetLangFromContext(c),
	})
}

// Error returns an error response (with internationalized message).
func Error(c *gin.Context, code int, msgID string) {
	localizer := GetLocalizerFromContext(c)
	message := T(localizer, msgID)

	c.JSON(code, gin.H{
		"success": false,
		"message": message,
		"lang":    GetLangFromContext(c),
	})
}

// ErrorWithData returns an error response (with template data).
func ErrorWithData(c *gin.Context, code int, msgID string, templateData map[string]any) {
	localizer := GetLocalizerFromContext(c)
	message := T(localizer, msgID, templateData)

	c.JSON(code, gin.H{
		"success": false,
		"message": message,
		"lang":    GetLangFromContext(c),
	})
}

// Message gets an internationalized message.
func Message(c *gin.Context, msgID string, templateData ...map[string]any) string {
	localizer := GetLocalizerFromContext(c)
	return T(localizer, msgID, templateData...)
}

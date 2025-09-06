package i18n

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

var (
	bundle *i18n.Bundle
)

// Init 初始化 i18n
func Init() error {
	bundle = i18n.NewBundle(language.Chinese)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)

	// 加载支持的语言文件
	languages := []string{"zh-CN", "en-US", "ja-JP"}
	for _, lang := range languages {
		if err := loadMessageFile(lang); err != nil {
			return fmt.Errorf("failed to load language file %s: %w", lang, err)
		}
	}

	return nil
}

// loadMessageFile 加载语言文件
func loadMessageFile(lang string) error {
	// 根据语言设置消息
	messages := getMessages(lang)
	for id, msg := range messages {
		bundle.AddMessages(language.MustParse(lang), &i18n.Message{
			ID:    id,
			Other: msg,
		})
	}
	
	return nil
}

// GetLocalizer 获取本地化器
func GetLocalizer(acceptLang string) *i18n.Localizer {
	// 解析 Accept-Language 头
	langs := parseAcceptLanguage(acceptLang)
	
	// 如果没有指定语言，默认使用中文
	if len(langs) == 0 {
		langs = []string{"zh-CN"}
	}
	
	return i18n.NewLocalizer(bundle, langs...)
}

// parseAcceptLanguage 解析 Accept-Language 头
func parseAcceptLanguage(acceptLang string) []string {
	if acceptLang == "" {
		return nil
	}
	
	// 简单解析，只取第一个语言
	parts := strings.Split(acceptLang, ",")
	if len(parts) > 0 {
		lang := strings.TrimSpace(parts[0])
		// 移除质量因子 (q=...)
		if idx := strings.Index(lang, ";"); idx > 0 {
			lang = lang[:idx]
		}
		
		// 标准化语言代码
		lang = normalizeLanguageCode(lang)
		return []string{lang}
	}
	
	return nil
}

// normalizeLanguageCode 标准化语言代码
func normalizeLanguageCode(lang string) string {
	lang = strings.TrimSpace(lang)
	
	// 映射常见的语言代码
	switch strings.ToLower(lang) {
	case "zh", "zh-cn", "zh-hans":
		return "zh-CN"
	case "en", "en-us":
		return "en-US"
	case "ja", "ja-jp":
		return "ja-JP"
	default:
		// 尝试匹配前缀
		if strings.HasPrefix(strings.ToLower(lang), "zh") {
			return "zh-CN"
		}
		if strings.HasPrefix(strings.ToLower(lang), "en") {
			return "en-US"
		}
		if strings.HasPrefix(strings.ToLower(lang), "ja") {
			return "ja-JP"
		}
		// 默认返回中文
		return "zh-CN"
	}
}

// T 翻译消息
func T(localizer *i18n.Localizer, msgID string, data ...map[string]interface{}) string {
	config := &i18n.LocalizeConfig{
		MessageID: msgID,
	}
	
	if len(data) > 0 {
		config.TemplateData = data[0]
	}
	
	msg, err := localizer.Localize(config)
	if err != nil {
		// 如果翻译失败，返回消息ID
		return msgID
	}
	
	return msg
}

// getMessages 获取内置消息（为了避免外部文件依赖）
func getMessages(lang string) map[string]string {
	switch lang {
	case "en-US":
		return messagesEnUS
	case "ja-JP":
		return messagesJaJP
	default:
		return messagesZhCN
	}
}

// 内置消息定义
var messagesZhCN = map[string]string{
	// 通用消息
	"success":          "操作成功",
	"error":            "操作失败",
	"unauthorized":     "未授权",
	"forbidden":        "禁止访问",
	"not_found":        "未找到",
	"bad_request":      "请求错误",
	"internal_error":   "内部错误",
	"invalid_param":    "参数无效",
	"required_field":   "必填字段",
	
	// 认证相关
	"auth.invalid_key": "无效的授权密钥",
	"auth.key_required": "需要授权密钥",
	"auth.login_success": "登录成功",
	"auth.logout_success": "退出成功",
	
	// 分组相关
	"group.created":     "分组创建成功",
	"group.updated":     "分组更新成功",
	"group.deleted":     "分组删除成功",
	"group.not_found":   "分组不存在",
	"group.name_exists": "分组名称已存在",
	
	// 密钥相关
	"key.created":       "密钥创建成功",
	"key.updated":       "密钥更新成功",
	"key.deleted":       "密钥删除成功",
	"key.not_found":     "密钥不存在",
	"key.invalid":       "密钥无效",
	"key.check_started": "密钥检查已开始",
	"key.check_completed": "密钥检查完成",
	
	// 设置相关
	"settings.updated":  "设置更新成功",
	"settings.reset":    "设置已重置",
	
	// 日志相关
	"logs.cleared":      "日志已清除",
	"logs.exported":     "日志导出成功",
}

var messagesEnUS = map[string]string{
	// Common messages
	"success":          "Success",
	"error":            "Error",
	"unauthorized":     "Unauthorized",
	"forbidden":        "Forbidden",
	"not_found":        "Not found",
	"bad_request":      "Bad request",
	"internal_error":   "Internal error",
	"invalid_param":    "Invalid parameter",
	"required_field":   "Required field",
	
	// Auth related
	"auth.invalid_key": "Invalid auth key",
	"auth.key_required": "Auth key required",
	"auth.login_success": "Login successful",
	"auth.logout_success": "Logout successful",
	
	// Group related
	"group.created":     "Group created successfully",
	"group.updated":     "Group updated successfully",
	"group.deleted":     "Group deleted successfully",
	"group.not_found":   "Group not found",
	"group.name_exists": "Group name already exists",
	
	// Key related
	"key.created":       "Key created successfully",
	"key.updated":       "Key updated successfully",
	"key.deleted":       "Key deleted successfully",
	"key.not_found":     "Key not found",
	"key.invalid":       "Invalid key",
	"key.check_started": "Key check started",
	"key.check_completed": "Key check completed",
	
	// Settings related
	"settings.updated":  "Settings updated successfully",
	"settings.reset":    "Settings reset successfully",
	
	// Logs related
	"logs.cleared":      "Logs cleared successfully",
	"logs.exported":     "Logs exported successfully",
}

var messagesJaJP = map[string]string{
	// 共通メッセージ
	"success":          "成功",
	"error":            "エラー",
	"unauthorized":     "認証されていません",
	"forbidden":        "アクセス禁止",
	"not_found":        "見つかりません",
	"bad_request":      "不正なリクエスト",
	"internal_error":   "内部エラー",
	"invalid_param":    "無効なパラメータ",
	"required_field":   "必須フィールド",
	
	// 認証関連
	"auth.invalid_key": "無効な認証キー",
	"auth.key_required": "認証キーが必要です",
	"auth.login_success": "ログイン成功",
	"auth.logout_success": "ログアウト成功",
	
	// グループ関連
	"group.created":     "グループ作成成功",
	"group.updated":     "グループ更新成功",
	"group.deleted":     "グループ削除成功",
	"group.not_found":   "グループが存在しません",
	"group.name_exists": "グループ名は既に存在します",
	
	// キー関連
	"key.created":       "キー作成成功",
	"key.updated":       "キー更新成功",
	"key.deleted":       "キー削除成功",
	"key.not_found":     "キーが見つかりません",
	"key.invalid":       "無効なキー",
	"key.check_started": "キーチェック開始",
	"key.check_completed": "キーチェック完了",
	
	// 設定関連
	"settings.updated":  "設定更新成功",
	"settings.reset":    "設定リセット成功",
	
	// ログ関連
	"logs.cleared":      "ログクリア成功",
	"logs.exported":     "ログエクスポート成功",
}
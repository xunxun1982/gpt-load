package locales

// Messages 中文(简体)翻译
var MessagesZhCN = map[string]string{
	// 通用消息
	"success":        "操作成功",
	"error":          "操作失败",
	"unauthorized":   "未授权",
	"forbidden":      "禁止访问",
	"not_found":      "未找到",
	"bad_request":    "请求错误",
	"internal_error": "内部错误",
	"invalid_param":  "参数无效",
	"required_field": "必填字段",

	// 认证相关
	"auth.invalid_key":    "无效的授权密钥",
	"auth.key_required":   "需要授权密钥",
	"auth.login_success":  "登录成功",
	"auth.logout_success": "退出成功",

	// 分组相关
	"group.created":     "分组创建成功",
	"group.updated":     "分组更新成功",
	"group.deleted":     "分组删除成功",
	"group.not_found":   "分组不存在",
	"group.name_exists": "分组名称已存在",

	// 密钥相关
	"key.created":         "密钥创建成功",
	"key.updated":         "密钥更新成功",
	"key.deleted":         "密钥删除成功",
	"key.not_found":       "密钥不存在",
	"key.invalid":         "密钥无效",
	"key.check_started":   "密钥检查已开始",
	"key.check_completed": "密钥检查完成",

	// 设置相关
	"settings.updated": "设置更新成功",
	"settings.reset":   "设置已重置",

	// 日志相关
	"logs.cleared":  "日志已清除",
	"logs.exported": "日志导出成功",

	// 验证相关
	"validation.invalid_group_name":      "无效的分组名称。只能包含小写字母、数字、中划线或下划线，长度1-100位",
	"validation.invalid_test_path":       "无效的测试路径。如果提供，必须是以 / 开头的有效路径，且不能是完整的URL。",
	"validation.duplicate_header":        "重复的请求头: {key}",
	"validation.group_not_found":         "分组不存在",
	"validation.invalid_status_filter":   "无效的状态过滤器",
	"validation.invalid_group_id":        "无效的分组ID格式",
	"validation.test_model_required":     "测试模型是必需的",
	"validation.invalid_copy_keys_value": "无效的copy_keys值。必须是'none'、'valid_only'或'all'",
	"validation.invalid_channel_type":    "无效的通道类型。支持的类型有: {types}",
	"validation.test_model_empty":        "测试模型不能为空或只有空格",
	"validation.invalid_status_value":    "无效的状态值",

	// 任务相关
	"task.validation_started": "密钥验证任务已开始",
	"task.import_started":     "密钥导入任务已开始",
	"task.delete_started":     "密钥删除任务已开始",
	"task.already_running":    "已有任务正在运行",
	"task.get_status_failed":  "获取任务状态失败",

	// 仪表板相关
	"dashboard.invalid_keys":               "无效密钥数量",
	"dashboard.success_requests":           "成功请求",
	"dashboard.failed_requests":            "失败请求",
	"dashboard.auth_key_missing":           "AUTH_KEY未设置，系统无法正常工作",
	"dashboard.auth_key_required":          "必须设置AUTH_KEY以保护管理界面",
	"dashboard.encryption_key_missing":     "未设置ENCRYPTION_KEY，敏感数据将明文存储",
	"dashboard.encryption_key_recommended": "强烈建议设置ENCRYPTION_KEY以加密保护API密钥等敏感数据",

	// 数据库相关
	"database.cannot_get_groups":     "无法获取分组列表",
	"database.rpm_stats_failed":      "获取RPM统计失败",
	"database.current_stats_failed":  "获取当前期间统计失败",
	"database.previous_stats_failed": "获取上一期间统计失败",
	"database.chart_data_failed":     "获取图表数据失败",
	"database.group_stats_failed":    "获取部分统计信息失败",

	// 成功消息
	"success.group_deleted":        "分组及相关密钥删除成功",
	"success.keys_restored":        "{count}个密钥已恢复",
	"success.invalid_keys_cleared": "{count}个无效密钥已清除",
	"success.all_keys_cleared":     "{count}个密钥已清除",

	// 密码安全相关
	"security.password_too_short":         "{keyType}长度不足（{length}字符），建议至少16字符",
	"security.password_short":             "{keyType}长度偏短（{length}字符），建议32字符以上",
	"security.password_weak_pattern":      "{keyType}包含常见弱密码模式：{pattern}",
	"security.password_low_complexity":    "{keyType}复杂度不足，缺少大小写字母、数字或特殊字符的组合",
	"security.password_recommendation_16": "使用至少16个字符的强密码，推荐32字符以上",
	"security.password_recommendation_32": "推荐使用32个字符以上的密码以提高安全性",
	"security.password_avoid_common":      "避免使用常见单词，建议使用随机生成的强密码",
	"security.password_complexity":        "建议包含大小写字母、数字和特殊字符以提高密码强度",

	// 配置相关
	"config.updated":                          "配置更新成功",
	"config.app_url":                          "项目地址",
	"config.app_url_desc":                     "项目的基础 URL，用于拼接分组终端节点地址。系统配置优先于环境变量 APP_URL。",
	"config.proxy_keys":                       "全局代理密钥",
	"config.proxy_keys_desc":                  "全局代理密钥，用于访问所有分组的代理端点。多个密钥请用逗号分隔。",
	"config.log_retention_days":               "日志保留时长（天）",
	"config.log_retention_days_desc":          "请求日志在数据库中的保留天数，0为不清理日志。",
	"config.log_write_interval":               "日志延迟写入周期（分钟）",
	"config.log_write_interval_desc":          "请求日志从缓存写入数据库的周期（分钟），0为实时写入数据。",
	"config.enable_request_body_logging":      "启用日志详情",
	"config.enable_request_body_logging_desc": "是否在请求日志中记录完整的请求体内容。启用此功能会增加内存以及存储空间的占用。",

	// 请求设置相关
	"config.request_timeout":              "请求超时（秒）",
	"config.request_timeout_desc":         "转发请求的完整生命周期超时（秒）等。",
	"config.connect_timeout":              "连接超时（秒）",
	"config.connect_timeout_desc":         "与上游服务建立新连接的超时时间（秒）。",
	"config.idle_conn_timeout":            "空闲连接超时（秒）",
	"config.idle_conn_timeout_desc":       "HTTP 客户端中空闲连接的超时时间（秒）。",
	"config.response_header_timeout":      "响应头超时（秒）",
	"config.response_header_timeout_desc": "等待上游服务响应头的最长时间（秒）。",
	"config.max_idle_conns":               "最大空闲连接数",
	"config.max_idle_conns_desc":          "HTTP 客户端连接池中允许的最大空闲连接总数。",
	"config.max_idle_conns_per_host":      "每主机最大空闲连接数",
	"config.max_idle_conns_per_host_desc": "HTTP 客户端连接池对每个上游主机允许的最大空闲连接数。",
	"config.proxy_url":                    "代理服务器地址",
	"config.proxy_url_desc":               "全局 HTTP/HTTPS 代理服务器地址，例如：http://user:pass@host:port。如果为空，则使用环境变量配置。",

	// 密钥配置相关
	"config.max_retries":                     "最大重试次数",
	"config.max_retries_desc":                "单个请求使用不同 Key 的最大重试次数，0为不重试。",
	"config.blacklist_threshold":             "黑名单阈值",
	"config.blacklist_threshold_desc":        "一个 Key 连续失败多少次后进入黑名单，0为不拉黑。",
	"config.key_validation_interval":         "密钥验证间隔（分钟）",
	"config.key_validation_interval_desc":    "后台验证密钥的默认间隔（分钟）。",
	"config.key_validation_concurrency":      "密钥验证并发数",
	"config.key_validation_concurrency_desc": "后台定时验证无效 Key 时的并发数，如果使用SQLite或者运行环境性能不佳，请尽量保证20以下，避免过高的并发导致数据不一致问题。",
	"config.key_validation_timeout":          "密钥验证超时（秒）",
	"config.key_validation_timeout_desc":     "后台定时验证单个 Key 时的 API 请求超时时间（秒）。",

	// 分类标签
	"config.category.basic":   "基础参数",
	"config.category.request": "请求设置",
	"config.category.key":     "密钥配置",

	// 内部错误消息（供fmt.Errorf使用）
	"error.upstreams_required":       "upstreams字段是必需的",
	"error.invalid_upstreams_format": "upstreams格式无效",
	"error.at_least_one_upstream":    "至少需要一个upstream",
	"error.upstream_url_empty":       "upstream URL不能为空",
	"error.invalid_upstream_url":     "无效的upstream URL格式: {url}",
	"error.upstream_weight_positive": "upstream权重必须是正整数",
	"error.marshal_upstreams_failed": "序列化清理后的upstreams失败",
	"error.unknown_config_field":     "未知的配置字段: '{field}'",
}

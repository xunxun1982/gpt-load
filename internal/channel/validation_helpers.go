package channel

import (
	"strings"
	"sync/atomic"
	"time"

	"gpt-load/internal/models"
)

const (
	validationDefaultPrompt         = "hi"
	validationPromptModeRandomQueue = "random_queue"
)

var validationPromptCursor = uint64(time.Now().UnixNano())

var validationPromptQueue = [...]string{
	"解释缓存",
	"说明重试",
	"为何超时",
	"什么是日志",
	"说明代理",
	"何为限流",
	"解释队列",
	"说明鉴权",
	"说明授权",
	"为何压缩",
	"解释流式",
	"非流优势",
	"说明重定向",
	"何为熔断",
	"解释回退",
	"说明指标",
	"为何分桶",
	"解释令牌",
	"说明推理",
	"缓存命中",
	"缓存未命中",
	"何为延迟",
	"吞吐含义",
	"说明负载",
	"为何均衡",
	"解释权重",
	"说明健康",
	"为何轮询",
	"解释密钥",
	"何为上游",
	"说明下游",
	"为何脱敏",
	"解释审计",
	"说明导入",
	"说明导出",
	"何为迁移",
	"为何备份",
	"解释事务",
	"说明索引",
	"何为慢查",
	"为何分页",
	"解释并发",
	"说明锁定",
	"何为幂等",
	"为何重试",
	"解释退避",
	"说明超时",
	"何为断连",
	"为何限速",
	"解释配额",
	"说明模型",
	"何为别名",
	"为何映射",
	"解释路由",
	"说明请求",
	"说明响应",
	"何为错误",
	"为何告警",
	"解释监控",
	"说明趋势",
	"何为峰值",
	"为何采样",
	"解释配置",
	"说明开关",
	"何为默认",
	"为何校验",
	"解释格式",
	"说明JSON",
	"检查状态",
	"说明成功",
	"说明失败",
	"为何无效",
	"解释可用",
	"说明启用",
	"说明禁用",
	"何为分组",
	"解释聚合",
	"说明子组",
	"为何删除",
	"解释恢复",
	"说明重置",
	"为何清理",
	"解释保留",
	"说明统计",
	"令牌统计",
	"输入令牌",
	"输出令牌",
	"缓存令牌",
	"推理令牌",
	"解释SSE",
	"说明事件",
	"何为终止",
	"为何完成",
	"解释正文",
	"说明头部",
	"何为路径",
	"为何改写",
	"解释超参",
	"说明覆盖",
	"何为温度",
}

func validationPromptForGroup(group *models.Group) string {
	if groupConfigString(group, "validation_prompt_mode") != validationPromptModeRandomQueue {
		return validationDefaultPrompt
	}
	idx := atomic.AddUint64(&validationPromptCursor, 1) % uint64(len(validationPromptQueue))
	return validationPromptQueue[idx]
}

func validationStreamEnabled(group *models.Group) bool {
	return groupConfigBool(group, "validation_stream")
}

func validationResponsesIncludeEncryptedReasoning(group *models.Group) bool {
	return groupConfigBool(group, "responses_include_encrypted_reasoning")
}

func groupConfigBool(group *models.Group, key string) bool {
	if group == nil || group.Config == nil {
		return false
	}
	value, ok := group.Config[key]
	if !ok {
		return false
	}
	boolValue, ok := value.(bool)
	return ok && boolValue
}

func groupConfigString(group *models.Group, key string) string {
	if group == nil || group.Config == nil {
		return ""
	}
	value, ok := group.Config[key]
	if !ok {
		return ""
	}
	stringValue, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(stringValue))
}

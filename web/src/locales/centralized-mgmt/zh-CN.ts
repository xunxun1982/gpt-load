/**
 * Centralized Management i18n - Chinese (Simplified)
 */
export default {
  // Tab label
  tabLabel: "集中管理",

  // Endpoint display
  unifiedEndpoint: "统一端点",
  copyBaseUrl: "复制基础地址",
  baseUrlCopied: "基础地址已复制",
  endpointCopied: "端点地址已复制",
  endpointChat: "Chat Completions",
  endpointChatDesc: "OpenAI 格式聊天接口",
  endpointModels: "Models",
  endpointModelsDesc: "获取可用模型列表",
  endpointClaude: "Messages",
  endpointClaudeDesc: "Claude 格式消息接口",
  endpointCodex: "Responses",
  endpointCodexDesc: "Codex 格式响应接口",

  // Model pool
  modelPool: "模型池",
  modelName: "模型名称",
  sourceGroups: "来源分组",
  groupType: "分组类型",
  channelType: "渠道类型",
  healthScore: "健康",
  effectiveWeight: "有效权重",
  groupCount: "分组",
  standardGroup: "标准",
  subGroup: "子分组",
  aggregateGroup: "聚合",
  aggregateGroupShort: "聚",
  subGroupShort: "子",
  noModels: "暂无可用模型",
  noEnabledGroups: "无可用分组",
  searchModelPlaceholder: "搜索模型或分组名称...",
  totalModels: "共 {total} 个模型",
  filterAll: "全部",
  editPriority: "编辑优先级",

  // Priority
  priority: "优先级",
  priorityHint: "0=禁用，1-999=优先级（数字越小优先级越高）",

  // Hub settings
  hubSettings: "Hub 设置",
  maxRetries: "最大重试次数",
  maxRetriesHint: "同一优先级内的最大重试次数",
  retryDelay: "重试延迟",
  healthThreshold: "健康阈值",
  healthThresholdHint: "低于此阈值的分组将被跳过",
  enablePriority: "启用优先级路由",

  // Access keys
  accessKeys: "访问密钥",
  accessKeyName: "名称",
  maskedKey: "密钥",
  allowedModels: "允许模型",
  allModels: "全部模型",
  specificModels: "{count} 个模型",
  createAccessKey: "创建访问密钥",
  editAccessKey: "编辑访问密钥",
  deleteAccessKey: "删除访问密钥",
  confirmDeleteAccessKey: '确定要删除访问密钥 "{name}" 吗？',
  accessKeyCreated: "访问密钥创建成功",
  accessKeyUpdated: "访问密钥更新成功",
  accessKeyDeleted: "访问密钥删除成功",
  accessKeyToggled: "访问密钥状态已更新",
  noAccessKeys: "暂无访问密钥",
  keyCreatedCopyHint: "请复制并保存密钥，此密钥仅显示一次",

  // Access key form
  keyName: "密钥名称",
  keyNamePlaceholder: "请输入密钥名称",
  keyValue: "密钥值",
  keyValuePlaceholder: "留空自动生成，或输入自定义密钥",
  keyValueHint: "留空将自动生成 hk- 前缀的密钥",
  allowedModelsMode: "模型权限",
  allowedModelsModeAll: "允许访问全部模型",
  allowedModelsModeSpecific: "仅允许访问指定模型",
  selectAllowedModels: "选择允许的模型",
  searchModelsPlaceholder: "搜索模型...",
  selectedModelsCount: "已选择 {count} 个模型",

  // Panel
  centralizedManagement: "集中管理",
  refreshModelPool: "刷新模型池",
  refreshing: "刷新中...",
  totalAccessKeys: "共 {total} 个密钥",
};

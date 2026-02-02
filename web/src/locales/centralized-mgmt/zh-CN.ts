/**
 * Centralized Management i18n - Chinese (Simplified)
 */
export default {
  // Tab label
  tabLabel: "集中管理",

  // Endpoint display
  supportedChannels: "支持渠道",
  channelHint: "透传后由分组/聚合处理各类 API（聊天、音频、图片、视频等）",
  copyBaseUrl: "复制基础地址",
  baseUrlCopied: "基础地址已复制",
  endpointCopied: "端点地址已复制",

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
  prioritySortHint:
    "相同优先级时，健康度越高被选中概率越大。例如：priority=10的分组优先于priority=100的分组",
  priorityColumnHint: "数值越小=优先级越高（1最高，999最低）",
  priorityExplanationHint:
    "💡 分组标签上的数字（如 :20、:100）是优先级，数值越小优先级越高。相同优先级时按健康度加权随机选择",

  // Hub settings
  hubSettings: "Hub 设置",
  maxRetries: "最大重试次数",
  maxRetriesHint: "同一优先级内的最大重试次数",
  retryDelay: "重试延迟",
  healthThreshold: "健康阈值",
  healthThresholdHint: "低于此阈值的分组将被跳过",
  enablePriority: "启用优先级路由",
  onlyAggregateGroups: "仅接受聚合分组",
  onlyAggregateGroupsHint: "启用后，Hub 仅路由到聚合分组，忽略标准分组",

  // Access keys
  accessKeys: "访问密钥",
  accessKeyName: "名称",
  maskedKey: "密钥",
  keyCopied: "密钥已复制",
  keyNameCopied: "名称已复制",
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
  keyOnlyShownOnce: "密钥仅在创建时显示一次，关闭后无法再次查看。请确保已复制保存。",

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
  onlyAggregateGroupsActive: "🔒 仅聚合分组模式已启用（可在 Hub 设置中修改）",
  refreshModelPool: "刷新模型池",
  refreshing: "刷新中...",
  totalAccessKeys: "共 {total} 个密钥",

  // Usage statistics
  usageCount: "使用次数",
  lastUsedAt: "最后使用",
  neverUsed: "从未使用",
  justNow: "刚刚",
  minutesAgo: "{n} 分钟前",
  hoursAgo: "{n} 小时前",
  daysAgo: "{n} 天前",
  monthsAgo: "{n} 个月前",
  yearsAgo: "{n} 年前",

  // Batch operations
  batchOperations: "批量操作",
  batchDelete: "批量删除",
  batchEnable: "批量启用",
  batchDisable: "批量禁用",
  selectedKeys: "已选择 {count} 个密钥",
  confirmBatchDelete: "确定要删除选中的 {count} 个访问密钥吗？",
  batchDeleteSuccess: "成功删除 {count} 个访问密钥",
  batchEnableSuccess: "成功启用 {count} 个访问密钥",
  batchDisableSuccess: "成功禁用 {count} 个访问密钥",
  selectAtLeastOne: "请至少选择一个访问密钥",

  // Custom models
  customModels: "自定义模型",
  customModelNames: "自定义模型名称",
  customModelNamesHint: "为聚合分组添加自定义模型名称，每行一个",
  addCustomModel: "添加模型",
  editCustomModels: "编辑自定义模型",
  noCustomModels: "暂无自定义模型",
  customModelsUpdated: "自定义模型已更新",
  aggregateGroupName: "聚合分组",
  modelCount: "{count} 个模型",
  customModelBadge: "自定义",
  customModelTooltip: "这是用户自定义的模型名称",

  // Routing logic
  routingLogic: "路由逻辑（先后顺序）",
  routingStep1: "① 路径识别格式（Chat/Claude/Gemini/Image/Audio）",
  routingStep2: "② 提取模型名称",
  routingStep3: "③ 访问控制：密钥权限验证",
  routingStep4: "④ 模型可用性：检查模型是否存在于任何启用的分组",
  routingStep5:
    "⑤ 分组选择过滤：健康阈值 + 启用状态 + 渠道兼容性 + CC支持（Claude格式）+ 聚合分组前置条件（请求大小限制等）",
  routingStep6: "⑥ 渠道优先级：原生渠道 > 兼容渠道",
  routingStep7: "⑦ 分组选择：最小priority值（数值越小优先级越高）→ 健康度加权随机",
  routingStep8: "⑧ 路径重写并转发：/hub/v1/* → /proxy/分组名/v1/*",
};

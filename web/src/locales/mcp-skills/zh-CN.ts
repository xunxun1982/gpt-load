/**
 * MCP Skills i18n - Chinese (Simplified)
 */
export default {
  title: "MCP & Skills",
  subtitle: "管理 MCP、MCP聚合和 Skills 导出",

  // Tabs
  tabServices: "MCP",
  tabGroups: "MCP聚合",

  // Section titles
  basicInfo: "基本信息",
  connectionSettings: "连接设置",
  apiSettings: "API 设置",
  toolsSettings: "工具设置",

  // Service fields
  name: "名称",
  namePlaceholder: "输入名称（小写，无空格）",
  displayName: "显示名称",
  displayNamePlaceholder: "输入显示名称",
  description: "描述",
  descriptionPlaceholder: "输入描述",
  category: "分类",
  icon: "图标",
  iconPlaceholder: "输入 emoji 图标",
  sort: "排序",
  sortTooltip: "数字越小越靠前",
  enabled: "启用",
  type: "类型",

  // Service types
  typeStdio: "Stdio",
  typeSse: "SSE",
  typeStreamableHttp: "Streamable HTTP",
  typeApiBridge: "API Bridge",

  // Categories - matching backend ServiceCategory enum in models.go
  categorySearch: "搜索",
  categoryFetch: "抓取",
  categoryAI: "AI服务",
  categoryUtility: "工具",
  categoryStorage: "对象存储",
  categoryDatabase: "数据库",
  categoryFilesystem: "文件系统",
  categoryBrowser: "浏览器",
  categoryCommunication: "通信",
  categoryDevelopment: "开发工具",
  categoryCloud: "云服务",
  categoryMonitoring: "监控",
  categoryProductivity: "生产力",
  categoryCustom: "自定义",

  // Stdio/SSE fields
  command: "命令",
  commandPlaceholder: "例如：uvx, npx, python",
  args: "参数",
  argsPlaceholder: "输入参数（每行一个）",
  cwd: "工作目录",
  cwdPlaceholder: "可选，命令执行的工作目录",
  url: "URL",
  urlPlaceholder: "https://example.com/sse-endpoint",

  // Service info display
  serviceInfo: "MCP 信息",
  argsCount: "个参数",
  envCount: "个环境变量",

  // Environment variables
  envVars: "环境变量",
  envKeyPlaceholder: "KEY",
  envValuePlaceholder: "value",
  addEnvVar: "添加环境变量",
  envVarsHint: "只有启用的环境变量会被添加到配置中",
  envVarEnabled: "已启用",
  envVarDisabled: "已禁用",

  // Status
  disabled: "已禁用",

  // API Bridge fields
  apiEndpoint: "API 端点",
  apiEndpointPlaceholder: "https://api.example.com",
  apiKeyName: "API Key 名称",
  apiKeyNamePlaceholder: "例如：EXA_API_KEY",
  apiKeyValue: "API Key",
  apiKeyValuePlaceholder: "输入 API Key",
  apiKeyValueEditHint: "留空表示不修改现有密钥",
  apiKeyHeader: "认证头",
  apiKeyHeaderPlaceholder: "例如：Authorization, x-api-key",
  apiKeyPrefix: "认证前缀",
  apiKeyPrefixPlaceholder: "例如：Bearer",
  hasApiKey: "已配置 API Key",
  noApiKey: "未配置 API Key",

  // Tools
  tools: "工具",
  toolName: "工具名称",
  toolDescription: "工具描述",
  toolCount: "{count} 个工具",
  totalTools: "总工具",
  uniqueTools: "唯一工具",
  addTool: "添加工具",
  editTool: "编辑工具",
  deleteTool: "删除工具",
  inputSchema: "输入模式",
  inputSchemaPlaceholder: "输入 JSON Schema",

  // Rate limiting
  rpdLimit: "每日限额",
  rpdLimitTooltip: "每日请求限制（0 = 无限制）",

  // Health status
  healthStatus: "健康状态",
  healthHealthy: "健康",
  healthUnhealthy: "异常",
  healthUnknown: "未知",

  // Group fields
  groupName: "聚合名称",
  groupNamePlaceholder: "输入聚合名称（小写，无空格）",
  groupDisplayName: "显示名称",
  groupDescription: "描述",
  services: "MCP服务",
  serviceCount: "{count} 个MCP服务",
  selectServices: "选择MCP服务",
  noServicesSelected: "未选择MCP服务",
  noServices: "暂无服务",

  // Service weights for smart routing
  serviceWeights: "服务权重",
  serviceWeightsHint: "权重越高，smart_execute 选择该服务的概率越大。默认权重为 100",
  weight: "权重",
  weightHint: "权重越高优先级越高，错误率高的服务会自动降低选择概率",
  weightPlaceholder: "1-1000",
  errorRate: "错误率",
  totalCalls: "总调用",

  // Tool aliases for smart routing
  toolAliases: "工具别名",
  toolAliasesHint:
    "左侧填写统一名称（可重复），右侧填写该名称对应的实际工具名（逗号分隔）。smart_execute 调用时会自动匹配所有别名对应的工具",
  canonicalName: "统一名称",
  aliasesPlaceholder: "实际工具名1, 实际工具名2, ...",
  addToolAlias: "添加工具别名",
  viewToolDescriptions: "查看工具描述",
  originalDescriptions: "原始描述",
  noMatchingTools: "未找到匹配的工具",
  unifiedDescription: "统一描述（可选，节省tokens）",
  unifiedDescriptionPlaceholder: "输入统一描述，将替代各服务的原始描述",

  // MCP聚合
  aggregationEnabled: "启用聚合端点",
  aggregationEnabledTooltip: "启用后可通过聚合端点URL访问此聚合下的所有MCP工具",
  aggregationEndpoint: "聚合端点",
  accessToken: "访问令牌",
  accessTokenPlaceholder: "留空自动生成",
  accessTokenSetPlaceholder: "已设置，留空保持不变",
  accessTokenAlreadySet: "已设置访问令牌，留空将保持原有令牌不变",
  regenerateToken: "重新生成",
  copyToken: "复制令牌",
  tokenCopied: "令牌已复制",
  tokenRegenerated: "令牌已重新生成",
  generate: "生成",
  tokenGenerated: "访问令牌已生成",

  // Skill export
  skillExport: "Skills 导出",
  skillExportEndpoint: "Skills 导出 URL",
  exportAsSkill: "导出为Skills",
  skillExported: "Skills 导出成功",

  // Endpoint info
  endpointInfo: "端点信息",
  mcpConfig: "MCP 配置",
  copyConfig: "复制配置",
  configCopied: "配置已复制",
  copyFailed: "复制失败",

  // Templates
  templates: "模板",
  useTemplate: "使用模板",
  createFromTemplate: "从模板创建",
  templateCreated: "已从模板创建MCP",

  // Actions
  createService: "创建MCP",
  editService: "编辑MCP",
  deleteService: "删除MCP",
  createGroup: "创建MCP聚合",
  editGroup: "编辑MCP聚合",
  deleteGroup: "删除MCP聚合",
  confirmDeleteService: "确定要删除MCP「{name}」吗？",
  confirmDeleteGroup: "确定要删除MCP聚合「{name}」吗？",

  // Filter & Search
  filterEnabled: "状态",
  filterEnabledAll: "全部",
  filterEnabledYes: "启用",
  filterEnabledNo: "禁用",
  filterCategory: "分类",
  filterCategoryAll: "全部分类",
  filterType: "类型",
  filterTypeAll: "全部类型",
  searchPlaceholder: "搜索名称、描述...",
  totalCount: "共 {count} 项",

  // Messages
  serviceCreated: "MCP创建成功",
  serviceUpdated: "MCP更新成功",
  serviceDeleted: "MCP删除成功",
  groupCreated: "MCP聚合创建成功",
  groupUpdated: "MCP聚合更新成功",
  groupDeleted: "MCP聚合删除成功",

  // Import/Export/Delete
  exportAll: "导出全部",
  importAll: "导入",
  deleteAll: "删除所有",
  deleteAllWarning: "确定要删除所有 {count} 个MCP吗？此操作将同时清空所有MCP聚合中的引用。请输入 ",
  deleteAllConfirmText: "确认删除",
  toConfirmDeletion: " 以确认删除。",
  deleteAllPlaceholder: "请输入「确认删除」",
  confirmDelete: "确认删除",
  incorrectConfirmText: "输入的确认文本不正确",
  deleteAllSuccess: "已删除 {count} 个MCP",
  deleteAllNone: "没有可删除的MCP",
  exportEncrypted: "加密导出",
  exportPlain: "明文导出",
  exportSuccess: "导出成功",
  importSuccess: "导入成功：{services} 个MCP，{groups} 个MCP聚合",
  importFailed: "导入失败：{error}",
  importInvalidFormat: "导入文件格式无效",
  importInvalidJSON: "JSON 格式错误",

  // Validation
  nameRequired: "请输入名称",
  nameDuplicate: "名称「{name}」已存在",
  displayNameRequired: "请输入显示名称",
  typeRequired: "请选择类型",
  commandRequired: "Stdio/SSE 类型需要填写命令",
  apiEndpointRequired: "API Bridge 类型需要填写 API 端点",
  invalidJson: "JSON 格式无效",

  // Test
  test: "测试",
  testSuccess: "MCP「{name}」工作正常",
  testFailed: "测试失败：{error}",

  // Custom endpoint
  customEndpointHint: "留空使用官方端点",

  // JSON Import
  importMcpJson: "导入 MCP JSON",
  importMcpJsonBtn: "导入",
  jsonImportFromFile: "从文件导入",
  selectFile: "选择文件",
  fileReadError: "读取文件失败",
  jsonImportLabel: "或粘贴 JSON 配置",
  jsonImportPlaceholder: "粘贴 MCP JSON 配置",
  jsonImportHint:
    "支持标准 MCP JSON 格式（Claude Desktop、Kiro 等）。autoApprove 和 disabledTools 等字段将被忽略。",
  jsonImportEmpty: "请输入 JSON 配置",
  jsonImportInvalidFormat: "格式无效：必须包含 mcpServers 对象",
  jsonImportNoServers: "配置中未找到 MCP",
  jsonImportSuccess: "导入成功：{imported} 个已导入，{skipped} 个已跳过",
  jsonImportAllSkipped: "所有MCP已跳过（共 {skipped} 个）",

  // MCP Endpoint
  mcpEnabled: "MCP 端点",
  mcpEnabledTooltip: "启用 MCP 端点供外部访问",
  mcpEndpoint: "MCP 端点",
  serviceEndpointInfo: "MCP端点信息",
  noMcpEndpoint: "MCP 端点未启用",
  mcpEndpointNotEnabled: "此MCP的端点未启用",
  enableMcpEndpoint: "启用 MCP 端点",
  loadingEndpointInfo: "加载端点信息中...",
  mcpEndpointEnabled: "MCP 端点已启用",
  mcpEndpointDisabled: "MCP 端点已禁用",

  // Tool expansion
  expandTools: "查看工具",
  collapseTools: "收起工具",
  loadingTools: "加载工具中...",
  noTools: "暂无工具",
  noEnabledServices: "没有启用的服务",
  refreshTools: "刷新",
  toolsFromCache: "来自缓存",
  toolsFresh: "最新",
  toolsCachedAt: "缓存时间",
  toolsExpiresAt: "过期时间",
  toolsRefreshed: "工具刷新成功",
  toolsRefreshFailed: "刷新工具失败：{error}",
  toolInputSchema: "输入参数",

  // Selection hints
  selectItemHint: "从左侧面板选择一个MCP或MCP聚合",
  selectServiceHint: "选择一个MCP查看详情",
  selectGroupHint: "选择一个MCP聚合查看详情",
  noMatchingItems: "未找到匹配项",
  noItems: "暂无MCP或MCP聚合",
};

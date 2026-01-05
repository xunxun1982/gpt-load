/**
 * MCP Skills i18n - Chinese (Simplified)
 */
export default {
  title: "MCP & Skills",
  subtitle: "管理 MCP 服务、服务组和技能导出",

  // Tabs
  tabServices: "服务",
  tabGroups: "服务组",

  // Section titles
  basicInfo: "基本信息",
  connectionSettings: "连接设置",
  apiSettings: "API 设置",
  toolsSettings: "工具设置",

  // Service fields
  name: "名称",
  namePlaceholder: "输入服务名称（小写，无空格）",
  displayName: "显示名称",
  displayNamePlaceholder: "输入显示名称",
  description: "描述",
  descriptionPlaceholder: "输入服务描述",
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

  // Categories
  categorySearch: "搜索",
  categoryCode: "代码",
  categoryData: "数据",
  categoryUtility: "工具",
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
  serviceInfo: "服务信息",
  argsCount: "个参数",
  envCount: "个环境变量",

  // Environment variables
  envVars: "环境变量",
  envKeyPlaceholder: "KEY",
  envValuePlaceholder: "value",
  addEnvVar: "添加环境变量",
  envVarsHint: "只有启用的环境变量会被添加到服务配置中",
  envVarEnabled: "已启用",
  envVarDisabled: "已禁用",

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
  groupName: "组名称",
  groupNamePlaceholder: "输入组名称（小写，无空格）",
  groupDisplayName: "显示名称",
  groupDescription: "描述",
  services: "服务",
  serviceCount: "{count} 个服务",
  selectServices: "选择服务",
  noServicesSelected: "未选择服务",

  // MCP聚合
  aggregationEnabled: "MCP聚合",
  aggregationEnabledTooltip: "为此服务组启用MCP聚合端点",
  aggregationEndpoint: "聚合端点",
  accessToken: "访问令牌",
  accessTokenPlaceholder: "留空自动生成",
  regenerateToken: "重新生成",
  copyToken: "复制令牌",
  tokenCopied: "令牌已复制",
  tokenRegenerated: "令牌已重新生成",

  // Skill export
  skillExport: "技能导出",
  skillExportEndpoint: "技能导出 URL",
  exportAsSkill: "导出为技能",
  skillExported: "技能导出成功",

  // Endpoint info
  endpointInfo: "端点信息",
  mcpConfig: "MCP 配置",
  copyConfig: "复制配置",
  configCopied: "配置已复制",

  // Templates
  templates: "模板",
  useTemplate: "使用模板",
  createFromTemplate: "从模板创建",
  templateCreated: "已从模板创建服务",

  // Actions
  createService: "创建服务",
  editService: "编辑服务",
  deleteService: "删除服务",
  createGroup: "创建服务组",
  editGroup: "编辑服务组",
  deleteGroup: "删除服务组",
  confirmDeleteService: "确定要删除服务「{name}」吗？",
  confirmDeleteGroup: "确定要删除服务组「{name}」吗？",

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
  serviceCreated: "服务创建成功",
  serviceUpdated: "服务更新成功",
  serviceDeleted: "服务删除成功",
  groupCreated: "服务组创建成功",
  groupUpdated: "服务组更新成功",
  groupDeleted: "服务组删除成功",

  // Import/Export/Delete
  exportAll: "导出全部",
  importAll: "导入",
  deleteAll: "删除所有",
  deleteAllWarning:
    "确定要删除所有 {count} 个服务吗？此操作将同时清空所有服务组中的服务引用。请输入 ",
  deleteAllConfirmText: "确认删除",
  toConfirmDeletion: " 以确认删除。",
  deleteAllPlaceholder: "请输入「确认删除」",
  confirmDelete: "确认删除",
  incorrectConfirmText: "输入的确认文本不正确",
  deleteAllSuccess: "已删除 {count} 个服务",
  deleteAllNone: "没有可删除的服务",
  exportEncrypted: "加密导出",
  exportPlain: "明文导出",
  exportSuccess: "导出成功",
  importSuccess: "导入成功：{services} 个服务，{groups} 个服务组",
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
  testSuccess: "服务「{name}」工作正常",
  testFailed: "测试失败：{error}",

  // Custom endpoint
  customEndpointHint: "留空使用官方端点",

  // JSON Import
  importMcpJson: "导入 MCP JSON",
  importMcpJsonBtn: "导入",
  jsonImportLabel: "MCP 配置 JSON",
  jsonImportPlaceholder: "粘贴 MCP JSON 配置",
  jsonImportHint:
    "支持标准 MCP JSON 格式（Claude Desktop、Kiro 等）。autoApprove 和 disabledTools 等字段将被忽略。",
  jsonImportEmpty: "请输入 JSON 配置",
  jsonImportInvalidFormat: "格式无效：必须包含 mcpServers 对象",
  jsonImportNoServers: "配置中未找到 MCP 服务",
  jsonImportSuccess: "导入成功：{imported} 个已导入，{skipped} 个已跳过",
  jsonImportAllSkipped: "所有服务已跳过（共 {skipped} 个）",

  // MCP Endpoint
  mcpEnabled: "MCP 端点",
  mcpEnabledTooltip: "启用 MCP 端点供外部访问",
  mcpEndpoint: "MCP 端点",
  serviceEndpointInfo: "服务端点信息",
  noMcpEndpoint: "MCP 端点未启用",

  // Tool expansion
  expandTools: "查看工具",
  collapseTools: "收起工具",
  loadingTools: "加载工具中...",
  noTools: "暂无工具",
  refreshTools: "刷新",
  toolsFromCache: "来自缓存",
  toolsFresh: "最新",
  toolsCachedAt: "缓存时间",
  toolsExpiresAt: "过期时间",
  toolsRefreshed: "工具刷新成功",
  toolsRefreshFailed: "刷新工具失败：{error}",
  toolInputSchema: "输入参数",
};

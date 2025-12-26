/**
 * Site Management i18n - Chinese (Simplified)
 */
export default {
  title: "站点列表",
  subtitle: "管理公益站点的名称、备注、介绍、链接和签到",

  // Section titles
  basicInfo: "基本信息",
  checkinSettings: "签到设置",
  authSettings: "认证设置",

  // Basic fields
  name: "名称",
  namePlaceholder: "输入站点名称",
  notes: "备注",
  notesPlaceholder: "输入备注信息",
  description: "介绍",
  descriptionPlaceholder: "输入站点介绍",
  sort: "排序",
  sortTooltip: "数字越小越靠前",
  baseUrl: "站点链接",
  baseUrlPlaceholder: "https://example.com",
  siteType: "站点类型",
  enabled: "启用",
  userId: "用户ID",
  userIdPlaceholder: "输入用户ID",
  userIdTooltip: "用于签到请求的用户标识",

  // Check-in related
  checkinPageUrl: "签到页面",
  checkinPageUrlPlaceholder: "https://example.com/checkin",
  checkinPageUrlTooltip: "签到页面的完整URL，用于快速跳转",
  customCheckinUrl: "签到接口",
  customCheckinUrlPlaceholder: "/api/user/checkin",
  customCheckinUrlTooltip: "自定义签到API路径，留空使用默认路径",
  checkinAvailable: "可签到",
  checkinAvailableTooltip: "标记此站点是否支持签到功能（系统内置或第三方）",
  checkinEnabled: "启用签到",
  checkinEnabledTooltip: "是否允许对此站点执行签到操作",

  // Auth related
  authType: "认证方式",
  authValue: "认证信息",
  authValuePlaceholder: "输入 Access Token",
  authValueEditHint: "留空表示不修改现有认证信息",
  authTypeNone: "无",
  authTypeAccessToken: "Access Token",
  hasAuth: "已配置认证",
  noAuth: "未配置认证",

  // Site types
  siteTypeOther: "其他",
  siteTypeNewApi: "New API",
  siteTypeVeloera: "Veloera",
  siteTypeOneHub: "One Hub",
  siteTypeDoneHub: "Done Hub",
  siteTypeWong: "Wong公益站",

  // Status
  lastStatus: "最近状态",
  status: "状态",
  statusSuccess: "签到成功",
  statusAlreadyChecked: "今日已签到",
  statusFailed: "签到失败",
  statusSkipped: "已跳过",
  statusNone: "未签到",
  lastCheckinAt: "最后签到时间",
  lastCheckinMessage: "签到消息",

  // Actions
  checkin: "签到",
  checkinNow: "立即签到",
  logs: "日志",
  viewLogs: "查看日志",
  openSite: "打开站点",
  openCheckinPage: "打开签到页",
  copySite: "复制站点",
  siteCopied: "站点复制成功",
  deleteSite: "删除站点",
  confirmDeleteSite: "确定要删除站点「{name}」吗？删除后相关签到日志也将被清除。",
  enterSiteNameToConfirm: "请输入站点ID和名称以确认",
  dangerousDeleteWarning: "这是一个危险的操作，将删除站点 ",
  toConfirmDeletion: " 及其所有签到日志。",
  enterSiteId: "请输入站点ID",
  enterSiteName: "请输入站点名称",
  confirmDelete: "确认删除",
  incorrectSiteId: "站点ID输入不正确",
  incorrectSiteName: "站点名称输入不正确",
  siteHasBinding: "站点「{name}」已绑定分组「{groupName}」，请先解绑后再删除。",
  mustUnbindFirst: "请先解绑",

  // Logs
  logTime: "时间",
  logStatus: "状态",
  logMessage: "消息",
  noLogs: "暂无签到日志",

  // Statistics
  statsTotal: "总计",
  statsEnabled: "启用",
  statsDisabled: "禁用",

  // Filter & Search
  filterCheckinAvailable: "只显示可签到",
  searchPlaceholder: "搜索名称、链接、备注...",

  // Messages
  checkinSuccess: "签到成功",
  checkinFailed: "签到失败",
  siteCreated: "站点创建成功",
  siteUpdated: "站点更新成功",
  siteDeleted: "站点删除成功",

  // Import/Export
  exportEncrypted: "加密导出",
  exportPlain: "明文导出",
  exportSuccess: "导出成功",
  importSuccess: "导入成功：{imported}/{total} 个站点",
  importInvalidFormat: "导入文件格式无效",
  importInvalidJSON: "JSON 格式错误",

  // Validation
  nameRequired: "请输入站点名称",
  nameDuplicate: "站点名称「{name}」已存在",
  baseUrlRequired: "请输入站点链接",
  invalidBaseUrl: "站点链接格式不正确",
};

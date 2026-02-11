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
  autoCheckinEnabled: "自动签到",

  // Proxy settings
  useProxy: "使用代理",
  proxyUrl: "代理地址",
  proxyUrlPlaceholder: "http://127.0.0.1:7890",
  proxyUrlTooltip: "签到请求使用的代理地址，支持HTTP/SOCKS5",

  // Bypass settings
  bypassMethod: "绕过方式",
  bypassMethodNone: "无",
  bypassMethodStealth: "隐身模式 (TLS指纹)",
  stealthBypassHint: "⚠️ 隐身绕过需要使用 Cookie 认证方式",
  stealthCookieHint: "💡 请在 Cookie 中包含 CF Cookies（cf_clearance、acw_tc 等）以绕过 Cloudflare",
  stealthRequiresCookieAuth: "隐身绕过需要使用 Cookie 认证方式",
  stealthRequiresCookieValue: "隐身绕过需要填写 Cookie 值",
  missingCFCookies: "缺少 Cloudflare 绕过所需的 CF Cookies，需要以下至少一个：{cookies}",

  // Auth related
  authType: "认证方式",
  authTypePlaceholder: "选择认证方式（可多选）",
  authValue: "认证信息",
  authValuePlaceholder: "输入 Access Token",
  authValueEditHint: "留空表示不修改现有认证信息",
  authTypeNone: "无",
  authTypeAccessToken: "Access Token",
  authTypeCookie: "Cookie",
  authTypeCookiePlaceholder: "session=xxx; token=xxx; cf_clearance=xxx",
  authTypeCookieHint:
    "需要从浏览器抓取 Cookie，包含 session/token 等字段。如站点启用了 Cloudflare 防护，还需包含 cf_clearance。",
  multiAuthHint:
    "已选择多个认证方式。签到时将先尝试 Access Token，失败后再尝试 Cookie，任一成功即算签到成功。",
  hasAuth: "已配置认证",
  noAuth: "未配置认证",

  // Site types
  siteTypeOther: "其他",
  siteTypeBrand: "品牌",
  siteTypeNewApi: "New API",
  siteTypeVeloera: "Veloera",
  siteTypeOneHub: "One Hub",
  siteTypeDoneHub: "Done Hub",
  siteTypeWong: "Wong公益站",
  siteTypeAnyrouter: "Anyrouter",

  // Status
  lastStatus: "最近状态",
  status: "状态",
  balance: "余额",
  balanceTooltip: "点击刷新余额",
  balanceNotSupported: "不支持",
  refreshBalance: "刷新余额",
  refreshBalanceTooltip: "刷新所有站点余额",
  refreshingBalance: "正在刷新余额...",
  balanceRefreshed: "余额刷新完成",
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
  openSiteVisited: "打开站点 (今日已访问)",
  openCheckinPage: "打开签到页",
  openCheckinPageVisited: "打开签到页 (今日已访问)",
  copySite: "复制站点",
  siteCopied: "站点复制成功",
  deleteSite: "删除站点",
  confirmDeleteSite: "确定要删除站点「{name}」吗？删除后相关签到日志也将被清除。",
  dangerousDeleteWarning: "这是一个危险的操作，将删除站点 ",
  toConfirmDeletion: " 及其所有签到日志。请输入站点名称以确认：",
  enterSiteName: "请输入站点名称",
  confirmDelete: "确认删除",
  incorrectSiteName: "站点名称输入不正确",
  siteHasBinding: "站点「{name}」已绑定分组「{groupName}」，请先解绑后再删除。",
  siteHasBindings: "站点「{name}」已绑定 {count} 个分组（{groupNames}），请先解绑后再删除。",
  unknownGroups: "未知分组",
  boundGroupsTooltip: "已绑定 {count} 个分组，点击查看",
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
  statsCheckinAvailable: "可签到",

  // Filter & Search
  filterCheckinAvailable: "可签到",
  filterEnabled: "状态",
  filterEnabledLabel: "状态:",
  filterCheckinLabel: "可签到:",
  filterEnabledAll: "全部",
  filterEnabledYes: "启用",
  filterEnabledNo: "禁用",
  filterCheckinAll: "全部",
  filterCheckinYes: "是",
  filterCheckinNo: "否",
  searchPlaceholder: "搜索名称、链接、备注...",
  totalCount: "共 {count} 个站点",
  paginationPrefix: "共 {total} 条",

  // Messages
  checkinSuccess: "签到成功",
  checkinFailed: "签到失败",
  siteCreated: "站点创建成功",
  siteUpdated: "站点更新成功",
  siteDeleted: "站点删除成功",

  // Backend check-in messages (for translation mapping)
  backendMsg_checkInFailed: "签到失败",
  backendMsg_checkInDisabled: "签到已禁用",
  backendMsg_missingCredentials: "缺少认证信息",
  backendMsg_missingUserId: "缺少用户ID",
  backendMsg_unsupportedAuthType: "不支持的认证类型",
  backendMsg_anyrouterRequiresCookie: "Anyrouter 需要 Cookie 认证",
  backendMsg_cloudflareChallenge: "Cloudflare 验证，请从浏览器更新 Cookie",
  backendMsg_alreadyCheckedIn: "今日已签到",
  backendMsg_stealthRequiresCookie: "隐身绕过需要使用 Cookie 认证",
  backendMsg_missingCfCookies:
    "缺少 CF Cookies，需要以下之一：cf_clearance、acw_tc、cdn_sec_tc、acw_sc__v2、__cf_bm、_cfuvid",

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

  // Bulk delete
  deleteAllUnbound: "删除所有",
  deleteAllUnboundTooltip: "删除所有未绑定分组的站点",
  confirmDeleteAllUnbound: "确定要删除所有未绑定分组的站点吗？",
  deleteAllUnboundWarning:
    "这是一个危险的操作，将删除所有未绑定分组的站点（共 {count} 个）及其签到日志。请输入 ",
  deleteAllUnboundConfirmText: "DELETE",
  deleteAllUnboundPlaceholder: "请输入 DELETE 以确认",
  incorrectConfirmText: "确认文本输入不正确",
  noUnboundSites: "没有未绑定分组的站点",
};

/**
 * Site Management i18n - English (US)
 */
export default {
  title: "Site List",
  subtitle: "Manage site names, notes, descriptions, URLs and check-in",

  // Section titles
  basicInfo: "Basic Info",
  checkinSettings: "Check-in Settings",
  authSettings: "Authentication",

  // Basic fields
  name: "Name",
  namePlaceholder: "Enter site name",
  notes: "Notes",
  notesPlaceholder: "Enter notes",
  description: "Description",
  descriptionPlaceholder: "Enter site description",
  sort: "Sort",
  sortTooltip: "Lower numbers appear first",
  baseUrl: "Site URL",
  baseUrlPlaceholder: "https://example.com",
  siteType: "Site Type",
  enabled: "Enabled",
  userId: "User ID",
  userIdPlaceholder: "Enter user ID",
  userIdTooltip: "User identifier for check-in requests",

  // Check-in related
  checkinPageUrl: "Check-in Page",
  checkinPageUrlPlaceholder: "https://example.com/checkin",
  checkinPageUrlTooltip: "Full URL of the check-in page for quick access",
  customCheckinUrl: "Check-in API",
  customCheckinUrlPlaceholder: "/api/user/checkin",
  customCheckinUrlTooltip: "Custom check-in API path, leave empty for default",
  checkinAvailable: "Can Check-in",
  checkinAvailableTooltip: "Whether this site supports check-in (system or third-party)",
  checkinEnabled: "Check-in",
  checkinEnabledTooltip: "Allow check-in operations for this site",
  autoCheckinEnabled: "Auto Check-in",

  // Proxy settings
  useProxy: "Use Proxy",
  proxyUrl: "Proxy URL",
  proxyUrlPlaceholder: "http://127.0.0.1:7890",
  proxyUrlTooltip: "Proxy URL for check-in requests, supports HTTP/SOCKS5",

  // Bypass settings
  bypassMethod: "Bypass Method",
  bypassMethodNone: "None",
  bypassMethodStealth: "Stealth (TLS Fingerprint)",
  stealthBypassHint: "⚠️ Stealth bypass requires Cookie auth type",
  stealthCookieHint:
    "💡 Include CF cookies (cf_clearance, acw_tc, etc.) from browser for Cloudflare bypass",
  stealthRequiresCookieAuth: "Stealth bypass requires Cookie auth type",
  stealthRequiresCookieValue: "Stealth bypass requires cookie value",
  missingCFCookies: "Missing CF cookies for Cloudflare bypass. Need at least one of: {cookies}",

  // Auth related
  authType: "Auth Type",
  authTypePlaceholder: "Select auth type(s) (multiple allowed)",
  authValue: "Auth Value",
  authValuePlaceholder: "Enter Access Token",
  authValueEditHint: "Leave empty to keep existing auth",
  authTypeNone: "None",
  authTypeAccessToken: "Access Token",
  authTypeCookie: "Cookie",
  authTypeCookiePlaceholder: "session=xxx; token=xxx; cf_clearance=xxx",
  authTypeCookieHint:
    "Capture Cookie from browser, including session/token fields. If site uses Cloudflare protection, also include cf_clearance.",
  multiAuthHint:
    "Multiple auth types selected. Check-in will try Access Token first, then Cookie if it fails. Success with either counts as successful check-in.",
  hasAuth: "Auth Configured",
  noAuth: "No Auth",

  // Site types
  siteTypeOther: "Other",
  siteTypeBrand: "Brand",
  siteTypeNewApi: "New API",
  siteTypeVeloera: "Veloera",
  siteTypeOneHub: "One Hub",
  siteTypeDoneHub: "Done Hub",
  siteTypeWong: "Wong Gongyi",
  siteTypeAnyrouter: "Anyrouter",

  // Status
  lastStatus: "Last Status",
  status: "Status",
  balance: "Balance",
  balanceTooltip: "Click to refresh balance",
  balanceNotSupported: "N/A",
  refreshBalance: "Refresh Balance",
  refreshBalanceTooltip: "Refresh balances for all sites",
  refreshingBalance: "Refreshing balances...",
  balanceRefreshed: "Balances refreshed",
  statusSuccess: "Check-in Success",
  statusAlreadyChecked: "Already Checked In",
  statusFailed: "Check-in Failed",
  statusSkipped: "Skipped",
  statusNone: "Not Checked In",
  lastCheckinAt: "Last Check-in Time",
  lastCheckinMessage: "Check-in Message",

  // Actions
  checkin: "Check In",
  checkinNow: "Check In Now",
  logs: "Logs",
  viewLogs: "View Logs",
  openSite: "Open Site",
  openSiteVisited: "Open Site (Visited Today)",
  openCheckinPage: "Open Check-in Page",
  openCheckinPageVisited: "Open Check-in Page (Visited Today)",
  copySite: "Copy Site",
  siteCopied: "Site copied successfully",
  deleteSite: "Delete Site",
  confirmDeleteSite:
    'Are you sure you want to delete site "{name}"? Related check-in logs will also be deleted.',
  dangerousDeleteWarning: "This is a dangerous operation. It will delete site ",
  toConfirmDeletion: " and all its check-in logs. Please enter the site name to confirm:",
  enterSiteName: "Enter site name",
  confirmDelete: "Confirm Delete",
  incorrectSiteName: "Incorrect site name",
  siteHasBinding:
    'Site "{name}" is bound to group "{groupName}". Please unbind first before deleting.',
  siteHasBindings:
    'Site "{name}" is bound to {count} groups ({groupNames}). Please unbind first before deleting.',
  unknownGroups: "unknown groups",
  boundGroupsTooltip: "Bound to {count} groups, click to view",
  mustUnbindFirst: "Unbind First",

  // Logs
  logTime: "Time",
  logStatus: "Status",
  logMessage: "Message",
  noLogs: "No check-in logs",

  // Statistics
  statsTotal: "Total",
  statsEnabled: "Enabled",
  statsDisabled: "Disabled",
  statsCheckinAvailable: "Check-in",

  // Filter & Search
  filterCheckinAvailable: "Check-in",
  filterEnabled: "Status",
  filterEnabledLabel: "Status:",
  filterCheckinLabel: "Check-in:",
  filterEnabledAll: "All",
  filterEnabledYes: "On",
  filterEnabledNo: "Off",
  filterCheckinAll: "All",
  filterCheckinYes: "Yes",
  filterCheckinNo: "No",
  searchPlaceholder: "Search name, URL, notes...",
  totalCount: "{count} sites total",
  paginationPrefix: "{total} items",

  // Messages
  checkinSuccess: "Check-in successful",
  checkinFailed: "Check-in failed",
  siteCreated: "Site created successfully",
  siteUpdated: "Site updated successfully",
  siteDeleted: "Site deleted successfully",

  // Backend check-in messages (for translation mapping)
  backendMsg_checkInFailed: "Check-in failed",
  backendMsg_checkInDisabled: "Check-in disabled",
  backendMsg_missingCredentials: "Missing credentials",
  backendMsg_missingUserId: "Missing user ID",
  backendMsg_unsupportedAuthType: "Unsupported auth type",
  backendMsg_anyrouterRequiresCookie: "Anyrouter requires cookie auth",
  backendMsg_cloudflareChallenge: "Cloudflare challenge, update cookies from browser",
  backendMsg_alreadyCheckedIn: "Already checked in",
  backendMsg_stealthRequiresCookie: "Stealth bypass requires cookie auth",
  backendMsg_missingCfCookies:
    "Missing CF cookies, need one of: cf_clearance, acw_tc, cdn_sec_tc, acw_sc__v2, __cf_bm, _cfuvid",

  // Import/Export
  exportEncrypted: "Export Encrypted",
  exportPlain: "Export Plain",
  exportSuccess: "Export successful",
  importSuccess: "Import successful: {imported}/{total} sites",
  importInvalidFormat: "Invalid import file format",
  importInvalidJSON: "Invalid JSON format",

  // Validation
  nameRequired: "Please enter site name",
  nameDuplicate: 'Site name "{name}" already exists',
  baseUrlRequired: "Please enter site URL",
  invalidBaseUrl: "Invalid site URL format",

  // Bulk delete
  deleteAllUnbound: "Delete All",
  deleteAllUnboundTooltip: "Delete all sites not bound to any group",
  confirmDeleteAllUnbound: "Are you sure you want to delete all unbound sites?",
  deleteAllUnboundWarning:
    "This is a dangerous operation. It will delete all sites not bound to any group ({count} sites) and their check-in logs. Please enter ",
  deleteAllUnboundConfirmText: "DELETE",
  deleteAllUnboundPlaceholder: "Enter DELETE to confirm",
  incorrectConfirmText: "Incorrect confirmation text",
  noUnboundSites: "No unbound sites to delete",
};

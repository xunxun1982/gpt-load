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
  reorderSort: "Renumber Sort",
  reorderSortTitle: "Renumber Sort",
  reorderSortTooltip: "Rewrite sort numbers for all sites using the current global order",
  reorderStart: "Start",
  reorderStep: "Step",
  reorderPreview: "This will update sort numbers for all sites using the current global order.",
  reorderInvalidInput: "Enter a valid start value and step",
  reorderSortSuccess: "Sort numbers updated",
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
  sub2ApiCustomCheckinUrlPlaceholder: "/your/checkin/path",
  customCheckinUrlTooltip: "Custom check-in API path, leave empty for default",
  sub2ApiCustomCheckinHint:
    "Sub2API has no standard built-in check-in endpoint. Enter only a compatible path documented by this deployment; do not use the balance endpoint /api/v1/auth/me.",
  checkinAvailable: "Can Check-in",
  checkinAvailableTooltip: "Whether this site supports check-in (system or third-party)",
  checkinEnabled: "Check-in",
  checkinEnabledTooltip: "Allow check-in operations for this site",
  autoCheckinEnabled: "Auto Check-in",

  // Proxy settings
  useProxy: "Use Proxy",
  proxyUrl: "Proxy Pool",
  proxyUrlPlaceholder: "No proxy",
  proxyUrlTooltip: "Manual proxy pool proxy for check-in requests",
  proxyManualProxy: "Manual Proxy",

  // Bypass settings
  bypassMethod: "Bypass Method",
  bypassMethodNone: "None",
  bypassMethodStealth: "Stealth (TLS Fingerprint)",
  stealthBypassHint: "⚠️ Stealth bypass requires Cookie auth type",
  bypassNoneHint:
    "Uses standard API requests without browser emulation. Try Stealth mode only if the site returns 403 or requires browser verification.",
  bypassStealthHint:
    "Simulates Chrome TLS and request headers. Enable only when ordinary Cookie requests are rejected; it cannot create or renew WAF cookies.",
  anyrouterStealthHint:
    "Try this for AnyRouter 403 or browser challenges. A valid Cookie is still required and may need recapturing after the egress IP changes.",
  sub2ApiStealthHint:
    "Enable only when a WAF blocks standard requests. Access Token remains the identity and must be paired with a valid protection Cookie.",
  stealthCookieHint:
    "💡 Include browser WAF cookies (cf_clearance, acw_tc, cdn_sec_tc, acw_sc__v2, etc.) from browser",
  stealthRequiresCookieAuth: "Stealth bypass requires Cookie auth type",
  stealthRequiresCookieValue: "Stealth bypass requires cookie value",
  missingCFCookies: "Missing browser WAF cookies. Need at least one of: {cookies}",
  maxTwoAuthTypes: "Maximum 2 authentication types allowed",

  // Auth related
  authType: "Auth Type",
  authTypePlaceholder: "Select auth type(s) (multiple allowed)",
  authValue: "Auth Value",
  authValuePlaceholder: "Enter Access Token",
  authValueEditHint: "Leave empty to keep existing auth",
  authTypeNone: "None",
  authTypeAccessToken: "Access Token",
  sub2ApiRefreshToken: "Refresh Token",
  sub2ApiRefreshTokenPlaceholder: "Enter refresh_token",
  sub2ApiRefreshTokenHint:
    "Read it beside auth_token. Automatic balance refresh renews two minutes early; compatible check-in requests reuse the rotated tokens.",
  authTypeCookie: "Cookie",
  authTypeCookiePlaceholder: "session=xxx; token=xxx; cf_clearance=xxx",
  authTypeCookieHint:
    "Capture Cookie from browser, including session/token fields. If site uses browser protection, also include WAF cookies such as cf_clearance and acw_tc.",
  sub2ApiAuthHint:
    "For Sub2API, select Access Token and fill auth_token plus refresh_token from Application/Local Storage for automatic balance renewal. Upstream has no built-in check-in; use it only when the deployment exposes a compatible endpoint or a custom check-in URL is set. Leave User ID empty.",
  anyrouterAuthHint:
    "For AnyRouter, select Cookie and copy the full Cookie from the browser Network /api/user/sign_in request. User ID is required when automation is enabled. WAF cookies may be bound to the browser fingerprint and egress IP; stealth mode cannot renew them, so copy them again after expiry.",
  newApiCompatibleAuthHint:
    "For New API compatible sites, prefer the login Access Token; Cookie is also supported. Some deployments also require User ID headers. These are login credentials, not a model API key.",
  sub2ApiCapabilityHint:
    "The standard balance endpoint is /api/v1/auth/me with automatic token renewal. Upstream has no built-in check-in; only compatible deployments or custom check-in endpoints can use it.",
  anyrouterCapabilityHint:
    "Supports Cookie-based automatic check-in and balance refresh; expired browser-protection cookies must be updated manually.",
  newApiCapabilityHint:
    "Supports automatic check-in and balance refresh. Use a login Access Token or Cookie, not a model API key.",
  capabilitylessHint:
    "This type has no built-in automatic check-in or balance retrieval and is stored as a site record only.",
  sub2ApiUserIDHint: "Sub2API does not use this field; leave it empty.",
  anyrouterUserIDHint:
    "Required while the site is enabled. Read it from account details or the /api/user/self response.",
  anyrouterUserIDRequired: "User ID is required for enabled AnyRouter automation",
  anyrouterCookieRequired: "Cookie is required for enabled AnyRouter automation",
  sub2ApiAccessTokenRequired:
    "Access Token authentication is required for enabled Sub2API automation",
  sub2ApiCredentialRequired:
    "An Access Token or Refresh Token is required for enabled Sub2API automation",
  sub2ApiCustomCheckinRequired:
    "Sub2API has no built-in check-in. Configure a custom check-in endpoint first.",
  genericUserIDHint:
    "Required by some New API compatible sites. Read it from /api/user/self or the account page.",
  multiAuthHint:
    "Multiple auth types selected. Check-in will try Access Token first, then Cookie if it fails. Success with either counts as successful check-in.",
  hasAuth: "Auth Configured",
  noAuth: "No Auth",

  // Site types
  siteTypeOther: "Other",
  siteTypeBrand: "Brand",
  siteTypeNewApi: "New API",
  siteTypeSub2Api: "Sub2API",
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
  backendMsg_browserChallenge: "Browser challenge, update cookies or WAF cookies from browser",
  backendMsg_alreadyCheckedIn: "Already checked in",
  backendMsg_stealthRequiresCookie: "Stealth bypass requires cookie auth",
  backendMsg_missingCfCookies:
    "Missing browser WAF cookies, need one of: cf_clearance, acw_tc, cdn_sec_tc, acw_sc__v2, __cf_bm, _cfuvid",

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

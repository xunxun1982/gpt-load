/**
 * Site Management i18n - English (US)
 */
export default {
  title: "Site List",
  subtitle: "Manage site names, notes, descriptions, URLs and auto check-in",

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
  autoCheckin: "Auto Check-in",
  autoCheckinEnabled: "Auto Check-in",
  autoCheckinEnabledTooltip: "Automatically check in within the configured time window",

  // Auth related
  authType: "Auth Type",
  authValue: "Auth Value",
  authValuePlaceholder: "Enter Access Token",
  authValueEditHint: "Leave empty to keep existing auth",
  authTypeNone: "None",
  authTypeAccessToken: "Access Token",
  hasAuth: "Auth Configured",
  noAuth: "No Auth",

  // Site types
  siteTypeOther: "Other",
  siteTypeNewApi: "New API",
  siteTypeVeloera: "Veloera",
  siteTypeOneHub: "One Hub",
  siteTypeDoneHub: "Done Hub",
  siteTypeWong: "Wong Gongyi",

  // Status
  lastStatus: "Last Status",
  status: "Status",
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
  openCheckinPage: "Open Check-in Page",
  deleteSite: "Delete Site",
  confirmDeleteSite:
    'Are you sure you want to delete site "{name}"? Related check-in logs will also be deleted.',
  enterSiteNameToConfirm: "Enter site name to confirm",
  dangerousDeleteWarning: "This is a dangerous operation. It will delete site ",
  toConfirmDeletion: " and all its check-in logs. Please enter the site name to confirm:",
  enterSiteName: "Enter site name",
  confirmDelete: "Confirm Delete",
  incorrectSiteName: "Incorrect site name",

  // Logs
  logTime: "Time",
  logStatus: "Status",
  logMessage: "Message",
  noLogs: "No check-in logs",

  // Auto check-in config
  autoCheckinConfig: "Auto Check-in Configuration",
  config: "Configuration",
  globalEnabled: "Global Enabled",
  globalEnabledTooltip: "When disabled, all auto check-ins will be paused",
  windowStart: "Window Start",
  windowEnd: "Window End",
  windowTooltip: "Auto check-in will execute randomly within this time range",
  scheduleMode: "Schedule Mode",
  scheduleModeRandom: "Random Time",
  scheduleModeDeterministic: "Fixed Time",
  scheduleModeTooltip: "Random mode picks a random time within the window",
  deterministicTime: "Fixed Execution Time",
  deterministicTimeTooltip: "Execute check-in at this time every day",

  // Retry strategy
  retryStrategy: "Retry Strategy",
  retryEnabled: "Enable Retry",
  retryEnabledTooltip: "Automatically retry on check-in failure",
  retryInterval: "Retry Interval (min)",
  retryIntervalTooltip: "Wait time between retries",
  retryMaxAttempts: "Max Daily Attempts",
  retryMaxAttemptsTooltip: "Maximum retry attempts per day",

  // Status display
  statusRunning: "Running",
  statusNext: "Next Execution",
  statusLastRun: "Last Run",
  statusLastResult: "Last Result",
  statusPendingRetry: "Pending Retry",
  statusAttempts: "Today's Attempts",

  // Summary
  summaryTotal: "Total Sites",
  summaryExecuted: "Executed",
  summarySuccess: "Success",
  summaryFailed: "Failed",
  summarySkipped: "Skipped",

  // Statistics
  statsTotal: "Total",
  statsEnabled: "Enabled",
  statsDisabled: "Disabled",
  statsAutoCheckin: "Auto Check-in",

  // Filter & Search
  filterCheckinAvailable: "Show only check-in available",
  searchPlaceholder: "Search name, URL, notes...",

  // Auto Check-in Actions
  runNow: "Run Now",
  autoCheckinTriggered: "Auto check-in task triggered",

  // Messages
  checkinSuccess: "Check-in successful",
  checkinFailed: "Check-in failed",
  siteCreated: "Site created successfully",
  siteUpdated: "Site updated successfully",
  siteDeleted: "Site deleted successfully",
  configSaved: "Configuration saved successfully",

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
  invalidTimeFormat: "Invalid time format, please use HH:mm",
};

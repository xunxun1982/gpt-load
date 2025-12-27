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
  siteTypeBrand: "Brand",
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
  filterCheckinAvailable: "Show only check-in available",
  searchPlaceholder: "Search name, URL, notes...",

  // Messages
  checkinSuccess: "Check-in successful",
  checkinFailed: "Check-in failed",
  siteCreated: "Site created successfully",
  siteUpdated: "Site updated successfully",
  siteDeleted: "Site deleted successfully",

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

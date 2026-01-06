/**
 * MCP Skills i18n - English (US)
 */
export default {
  title: "MCP & Skills",
  subtitle: "Manage MCP services, service groups, and skill exports",

  // Tabs
  tabServices: "Services",
  tabGroups: "Groups",

  // Section titles
  basicInfo: "Basic Info",
  connectionSettings: "Connection",
  apiSettings: "API Settings",
  toolsSettings: "Tools",

  // Service fields
  name: "Name",
  namePlaceholder: "Enter service name (lowercase, no spaces)",
  displayName: "Display Name",
  displayNamePlaceholder: "Enter display name",
  description: "Description",
  descriptionPlaceholder: "Enter service description",
  category: "Category",
  icon: "Icon",
  iconPlaceholder: "Enter emoji icon",
  sort: "Sort",
  sortTooltip: "Lower numbers appear first",
  enabled: "Enabled",
  type: "Type",

  // Service types
  typeStdio: "Stdio",
  typeSse: "SSE",
  typeStreamableHttp: "Streamable HTTP",
  typeApiBridge: "API Bridge",

  // Categories
  categorySearch: "Search",
  categoryCode: "Code",
  categoryData: "Data",
  categoryUtility: "Utility",
  categoryCustom: "Custom",

  // Stdio/SSE fields
  command: "Command",
  commandPlaceholder: "e.g., uvx, npx, python",
  args: "Arguments",
  argsPlaceholder: "Enter arguments (one per line)",
  cwd: "Working Directory",
  cwdPlaceholder: "Optional, working directory for command",
  url: "URL",
  urlPlaceholder: "https://example.com/sse-endpoint",

  // Service info display
  serviceInfo: "Info",
  argsCount: "args",
  envCount: "env vars",

  // Environment variables
  envVars: "Environment Variables",
  envKeyPlaceholder: "KEY",
  envValuePlaceholder: "value",
  addEnvVar: "Add Variable",
  envVarsHint: "Only enabled variables will be added to the service configuration",
  envVarEnabled: "Enabled",
  envVarDisabled: "Disabled",

  // API Bridge fields
  apiEndpoint: "API Endpoint",
  apiEndpointPlaceholder: "https://api.example.com",
  apiKeyName: "API Key Name",
  apiKeyNamePlaceholder: "e.g., EXA_API_KEY",
  apiKeyValue: "API Key",
  apiKeyValuePlaceholder: "Enter API key",
  apiKeyValueEditHint: "Leave empty to keep existing key",
  apiKeyHeader: "Auth Header",
  apiKeyHeaderPlaceholder: "e.g., Authorization, x-api-key",
  apiKeyPrefix: "Auth Prefix",
  apiKeyPrefixPlaceholder: "e.g., Bearer",
  hasApiKey: "API Key Configured",
  noApiKey: "No API Key",

  // Tools
  tools: "Tools",
  toolName: "Tool Name",
  toolDescription: "Tool Description",
  toolCount: "{count} tools",
  addTool: "Add Tool",
  editTool: "Edit Tool",
  deleteTool: "Delete Tool",
  inputSchema: "Input Schema",
  inputSchemaPlaceholder: "Enter JSON schema",

  // Rate limiting
  rpdLimit: "RPD Limit",
  rpdLimitTooltip: "Requests per day limit (0 = unlimited)",

  // Health status
  healthStatus: "Health",
  healthHealthy: "Healthy",
  healthUnhealthy: "Unhealthy",
  healthUnknown: "Unknown",

  // Group fields
  groupName: "Group Name",
  groupNamePlaceholder: "Enter group name (lowercase, no spaces)",
  groupDisplayName: "Display Name",
  groupDescription: "Description",
  services: "Services",
  serviceCount: "{count} services",
  selectServices: "Select Services",
  noServicesSelected: "No services selected",

  // MCP Aggregation
  aggregationEnabled: "MCP Aggregation",
  aggregationEnabledTooltip: "Enable MCP Aggregation endpoint for this group",
  aggregationEndpoint: "Aggregation Endpoint",
  accessToken: "Access Token",
  accessTokenPlaceholder: "Auto-generated if empty",
  regenerateToken: "Regenerate",
  copyToken: "Copy Token",
  tokenCopied: "Token copied",
  tokenRegenerated: "Token regenerated",

  // Skill export
  skillExport: "Skill Export",
  skillExportEndpoint: "Skill Export URL",
  exportAsSkill: "Export as Skill",
  skillExported: "Skill exported successfully",

  // Endpoint info
  endpointInfo: "Endpoint Info",
  mcpConfig: "MCP Config",
  copyConfig: "Copy Config",
  configCopied: "Config copied",

  // Templates
  templates: "Templates",
  useTemplate: "Use Template",
  createFromTemplate: "Create from Template",
  templateCreated: "Service created from template",

  // Actions
  createService: "Create Service",
  editService: "Edit Service",
  deleteService: "Delete Service",
  createGroup: "Create Group",
  editGroup: "Edit Group",
  deleteGroup: "Delete Group",
  confirmDeleteService: 'Are you sure you want to delete service "{name}"?',
  confirmDeleteGroup: 'Are you sure you want to delete group "{name}"?',

  // Filter & Search
  filterEnabled: "Status",
  filterEnabledAll: "All",
  filterEnabledYes: "Enabled",
  filterEnabledNo: "Disabled",
  filterCategory: "Category",
  filterCategoryAll: "All Categories",
  filterType: "Type",
  filterTypeAll: "All Types",
  searchPlaceholder: "Search name, description...",
  totalCount: "{count} items",

  // Messages
  serviceCreated: "Service created successfully",
  serviceUpdated: "Service updated successfully",
  serviceDeleted: "Service deleted successfully",
  groupCreated: "Group created successfully",
  groupUpdated: "Group updated successfully",
  groupDeleted: "Group deleted successfully",

  // Import/Export/Delete
  exportAll: "Export All",
  importAll: "Import",
  deleteAll: "Delete All",
  deleteAllWarning:
    "Are you sure you want to delete all {count} services? This will also clear service references from all groups. Please enter ",
  deleteAllConfirmText: "confirm delete",
  toConfirmDeletion: " to confirm deletion.",
  deleteAllPlaceholder: 'Enter "confirm delete"',
  confirmDelete: "Confirm Delete",
  incorrectConfirmText: "Incorrect confirmation text",
  deleteAllSuccess: "Deleted {count} services",
  deleteAllNone: "No services to delete",
  exportEncrypted: "Export Encrypted",
  exportPlain: "Export Plain",
  exportSuccess: "Export successful",
  importSuccess: "Import successful: {services} services, {groups} groups",
  importFailed: "Import failed: {error}",
  importInvalidFormat: "Invalid import file format",
  importInvalidJSON: "Invalid JSON format",

  // Validation
  nameRequired: "Please enter name",
  nameDuplicate: 'Name "{name}" already exists',
  displayNameRequired: "Please enter display name",
  typeRequired: "Please select type",
  commandRequired: "Command is required for stdio/sse type",
  apiEndpointRequired: "API endpoint is required for API bridge type",
  invalidJson: "Invalid JSON format",

  // Test
  test: "Test",
  testSuccess: 'Service "{name}" is working correctly',
  testFailed: "Test failed: {error}",

  // Custom endpoint
  customEndpointHint: "Leave empty to use official endpoint",

  // JSON Import
  importMcpJson: "Import MCP JSON",
  importMcpJsonBtn: "Import",
  jsonImportLabel: "MCP Configuration JSON",
  jsonImportPlaceholder: "Paste MCP JSON configuration",
  jsonImportHint:
    "Supports standard MCP JSON format (Claude Desktop, Kiro, etc.). Fields like autoApprove and disabledTools will be ignored.",
  jsonImportEmpty: "Please enter JSON configuration",
  jsonImportInvalidFormat: 'Invalid format: must contain "mcpServers" object',
  jsonImportNoServers: "No MCP servers found in configuration",
  jsonImportSuccess: "Import successful: {imported} imported, {skipped} skipped",
  jsonImportAllSkipped: "All servers skipped ({skipped} total)",

  // MCP Endpoint
  mcpEnabled: "MCP Endpoint",
  mcpEnabledTooltip: "Enable MCP endpoint for external access",
  mcpEndpoint: "MCP Endpoint",
  serviceEndpointInfo: "Service Endpoint Info",
  noMcpEndpoint: "MCP endpoint not enabled",
  mcpEndpointNotEnabled: "MCP endpoint is not enabled for this service",
  enableMcpEndpoint: "Enable MCP Endpoint",
  loadingEndpointInfo: "Loading endpoint info...",
  mcpEndpointEnabled: "MCP endpoint enabled",
  mcpEndpointDisabled: "MCP endpoint disabled",

  // Tool expansion
  expandTools: "View Tools",
  collapseTools: "Hide Tools",
  loadingTools: "Loading tools...",
  noTools: "No tools available",
  refreshTools: "Refresh",
  toolsFromCache: "From cache",
  toolsFresh: "Fresh",
  toolsCachedAt: "Cached at",
  toolsExpiresAt: "Expires at",
  toolsRefreshed: "Tools refreshed successfully",
  toolsRefreshFailed: "Failed to refresh tools: {error}",
  toolInputSchema: "Input Schema",

  // Selection hints
  selectItemHint: "Select a service or group from the left panel",
  selectServiceHint: "Select a service to view details",
  selectGroupHint: "Select a group to view details",
  noMatchingItems: "No matching items found",
  noItems: "No services or groups yet",
};

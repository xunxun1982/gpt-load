/**
 * MCP Skills i18n - English (US)
 */
export default {
  title: "MCP & Skills",
  subtitle: "Manage MCP, MCP Aggregation, and Skills exports",

  // Tabs
  tabServices: "MCP",
  tabGroups: "MCP Aggregation",

  // Section titles
  basicInfo: "Basic Info",
  connectionSettings: "Connection",
  apiSettings: "API Settings",
  toolsSettings: "Tools",

  // Service fields
  name: "Name",
  namePlaceholder: "Enter name (lowercase, no spaces)",
  displayName: "Display Name",
  displayNamePlaceholder: "Enter display name",
  description: "Description",
  descriptionPlaceholder: "Enter description",
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

  // Categories - matching backend ServiceCategory enum in models.go
  categorySearch: "Search",
  categoryFetch: "Fetch",
  categoryAI: "AI",
  categoryUtility: "Utility",
  categoryStorage: "Storage",
  categoryDatabase: "Database",
  categoryFilesystem: "Filesystem",
  categoryBrowser: "Browser",
  categoryCommunication: "Communication",
  categoryDevelopment: "Development",
  categoryCloud: "Cloud",
  categoryMonitoring: "Monitoring",
  categoryProductivity: "Productivity",
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
  serviceInfo: "MCP Info",
  argsCount: "args",
  envCount: "env vars",

  // Environment variables
  envVars: "Environment Variables",
  envKeyPlaceholder: "KEY",
  envValuePlaceholder: "value",
  addEnvVar: "Add Variable",
  envVarsHint: "Only enabled variables will be added to the configuration",
  envVarEnabled: "Enabled",
  envVarDisabled: "Disabled",

  // Status
  disabled: "Disabled",

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
  totalTools: "total",
  uniqueTools: "unique",
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
  groupName: "Aggregation Name",
  groupNamePlaceholder: "Enter aggregation name (lowercase, no spaces)",
  groupDisplayName: "Display Name",
  groupDescription: "Description",
  services: "MCP Services",
  serviceCount: "{count} MCP services",
  selectServices: "Select MCP Services",
  noServicesSelected: "No MCP services selected",
  noServices: "No services available",

  // Service weights for smart routing
  serviceWeights: "MCP Weights",
  serviceWeightsHint:
    "Higher weight = higher probability of being selected by smart_execute. Default weight is 100",
  weight: "Weight",
  weightHint:
    "Higher weight = higher priority. Services with high error rates are automatically deprioritized",
  weightPlaceholder: "1-1000",
  errorRate: "Error Rate",
  totalCalls: "Total Calls",

  // Tool aliases for smart routing
  toolAliases: "Tool Aliases",
  toolAliasesHint:
    "Left: unified name (can repeat). Right: actual tool names (comma-separated). After configuration, list_all_tools only shows unified names, and smart_execute auto-routes to the corresponding service",
  canonicalName: "Unified Name",
  aliasesPlaceholder: "tool_name1, tool_name2, ...",
  addToolAlias: "Add Tool Alias",
  viewToolDescriptions: "View tool descriptions",
  originalDescriptions: "Original descriptions",
  noMatchingTools: "No matching tools found",
  unifiedDescription: "Unified description (optional, saves tokens)",
  unifiedDescriptionPlaceholder: "Enter unified description to replace original descriptions",

  // MCP Aggregation
  aggregationEnabled: "Enable Aggregation Endpoint",
  aggregationEnabledTooltip:
    "Enable to access all MCP tools in this group via aggregation endpoint URL",
  aggregationEndpoint: "Aggregation Endpoint",
  accessToken: "Access Token",
  accessTokenPlaceholder: "Auto-generated if empty",
  accessTokenSetPlaceholder: "Already set, leave empty to keep",
  accessTokenAlreadySet: "Access token is set. Leave empty to keep the existing token.",
  regenerateToken: "Regenerate",
  copyToken: "Copy Token",
  tokenCopied: "Token copied",
  tokenRegenerated: "Token regenerated",
  generate: "Generate",
  tokenGenerated: "Access token generated",

  // Skill export
  skillExport: "Skills Export",
  skillExportEndpoint: "Skills Export URL",
  exportAsSkill: "Export as Skills",
  skillExported: "Skills exported successfully",

  // Endpoint info
  endpointInfo: "Endpoint Info",
  mcpConfig: "MCP Config",
  copyConfig: "Copy Config",
  configCopied: "Config copied",
  copyFailed: "Copy failed",

  // Templates
  templates: "Templates",
  useTemplate: "Use Template",
  createFromTemplate: "Create from Template",
  templateCreated: "MCP created from template",

  // Actions
  createService: "Create MCP",
  editService: "Edit MCP",
  deleteService: "Delete MCP",
  createGroup: "Create MCP Aggregation",
  editGroup: "Edit MCP Aggregation",
  deleteGroup: "Delete MCP Aggregation",
  confirmDeleteService: 'Are you sure you want to delete MCP "{name}"?',
  confirmDeleteGroup: 'Are you sure you want to delete MCP Aggregation "{name}"?',

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
  serviceCreated: "MCP created successfully",
  serviceUpdated: "MCP updated successfully",
  serviceDeleted: "MCP deleted successfully",
  groupCreated: "MCP Aggregation created successfully",
  groupUpdated: "MCP Aggregation updated successfully",
  groupDeleted: "MCP Aggregation deleted successfully",

  // Import/Export/Delete
  exportAll: "Export All",
  importAll: "Import",
  deleteAll: "Delete All",
  deleteAllWarning:
    "Are you sure you want to delete all {count} MCPs? This will also clear references from all MCP Aggregations. Please enter ",
  deleteAllConfirmText: "confirm delete",
  toConfirmDeletion: " to confirm deletion.",
  deleteAllPlaceholder: 'Enter "confirm delete"',
  confirmDelete: "Confirm Delete",
  incorrectConfirmText: "Incorrect confirmation text",
  deleteAllSuccess: "Deleted {count} MCPs",
  deleteAllNone: "No MCPs to delete",
  exportEncrypted: "Export Encrypted",
  exportPlain: "Export Plain",
  exportSuccess: "Export successful",
  importSuccess: "Import successful: {services} MCPs, {groups} MCP Aggregations",
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
  testSuccess: 'MCP "{name}" is working correctly',
  testFailed: "Test failed: {error}",

  // Custom endpoint
  customEndpointHint: "Leave empty to use official endpoint",

  // JSON Import
  importMcpJson: "Import MCP JSON",
  importMcpJsonBtn: "Import",
  jsonImportFromFile: "Import from file",
  selectFile: "Select File",
  fileReadError: "Failed to read file",
  jsonImportLabel: "Or paste JSON configuration",
  jsonImportPlaceholder: "Paste MCP JSON configuration",
  jsonImportHint:
    "Supports standard MCP JSON format (Claude Desktop, Kiro, etc.). Fields like autoApprove and disabledTools will be ignored.",
  jsonImportEmpty: "Please enter JSON configuration",
  jsonImportInvalidFormat: 'Invalid format: must contain "mcpServers" object',
  jsonImportNoServers: "No MCP found in configuration",
  jsonImportSuccess: "Import successful: {imported} imported, {skipped} skipped",
  jsonImportAllSkipped: "All MCPs skipped ({skipped} total)",

  // MCP Endpoint
  mcpEnabled: "MCP Endpoint",
  mcpEnabledTooltip: "Enable MCP endpoint for external access",
  mcpEndpoint: "MCP Endpoint",
  serviceEndpointInfo: "MCP Endpoint Info",
  noMcpEndpoint: "MCP endpoint not enabled",
  mcpEndpointNotEnabled: "MCP endpoint is not enabled for this MCP",
  enableMcpEndpoint: "Enable MCP Endpoint",
  loadingEndpointInfo: "Loading endpoint info...",
  mcpEndpointEnabled: "MCP endpoint enabled",
  mcpEndpointDisabled: "MCP endpoint disabled",

  // Tool expansion
  expandTools: "View Tools",
  collapseTools: "Hide Tools",
  loadingTools: "Loading tools...",
  noTools: "No tools available",
  noEnabledServices: "No enabled services",
  refreshTools: "Refresh",
  toolsFromCache: "From cache",
  toolsFresh: "Fresh",
  toolsCachedAt: "Cached at",
  toolsExpiresAt: "Expires at",
  toolsRefreshed: "Tools refreshed successfully",
  toolsRefreshFailed: "Failed to refresh tools: {error}",
  toolInputSchema: "Input Schema",

  // Selection hints
  selectItemHint: "Select an MCP or MCP Aggregation from the left panel",
  selectServiceHint: "Select an MCP to view details",
  selectGroupHint: "Select an MCP Aggregation to view details",
  noMatchingItems: "No matching items found",
  noItems: "No MCPs or MCP Aggregations yet",
};

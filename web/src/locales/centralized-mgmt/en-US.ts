/**
 * Centralized Management i18n - English (US)
 */
export default {
  // Tab label
  tabLabel: "Centralized Management",

  // Endpoint display
  supportedChannels: "Supported Channels",
  channelHint:
    "Requests are forwarded to groups/aggregates for processing (chat, audio, image, video, etc.)",
  copyBaseUrl: "Copy Base URL",
  baseUrlCopied: "Base URL copied",
  endpointCopied: "Endpoint URL copied",

  // Model pool
  modelPool: "Model Pool",
  modelName: "Model Name",
  sourceGroups: "Source Groups",
  groupType: "Group Type",
  channelType: "Channel Type",
  healthScore: "Health Score",
  effectiveWeight: "Effective Weight",
  groupCount: "Groups",
  standardGroup: "Standard",
  subGroup: "Sub",
  aggregateGroup: "Aggregate",
  aggregateGroupShort: "A",
  subGroupShort: "S",
  noModels: "No models available",
  noEnabledGroups: "No enabled groups",
  searchModelPlaceholder: "Search model or group name...",
  totalModels: "{total} models",
  filterAll: "All",
  editPriority: "Edit Priority",

  // Priority
  priority: "Priority",
  priorityHint: "0=disabled, 1-999=priority (lower=higher priority)",

  // Hub settings
  hubSettings: "Hub Settings",
  maxRetries: "Max Retries",
  maxRetriesHint: "Max retries within the same priority level",
  retryDelay: "Retry Delay",
  healthThreshold: "Health Threshold",
  healthThresholdHint: "Groups below this threshold will be skipped",
  enablePriority: "Enable Priority Routing",

  // Access keys
  accessKeys: "Access Keys",
  accessKeyName: "Name",
  maskedKey: "Key",
  allowedModels: "Allowed Models",
  allModels: "All Models",
  specificModels: "{count} models",
  createAccessKey: "Create Access Key",
  editAccessKey: "Edit Access Key",
  deleteAccessKey: "Delete Access Key",
  confirmDeleteAccessKey: 'Are you sure you want to delete access key "{name}"?',
  accessKeyCreated: "Access key created successfully",
  accessKeyUpdated: "Access key updated successfully",
  accessKeyDeleted: "Access key deleted successfully",
  accessKeyToggled: "Access key status updated",
  noAccessKeys: "No access keys",
  keyCreatedCopyHint: "Please copy and save the key, it will only be shown once",
  keyOnlyShownOnce:
    "The key is only shown once at creation. It cannot be viewed again after closing. Please make sure you have copied it.",

  // Access key form
  keyName: "Key Name",
  keyNamePlaceholder: "Enter key name",
  keyValue: "Key Value",
  keyValuePlaceholder: "Leave empty to auto-generate, or enter custom key",
  keyValueHint: "Leave empty to auto-generate a key with hk- prefix",
  allowedModelsMode: "Model Permissions",
  allowedModelsModeAll: "Allow access to all models",
  allowedModelsModeSpecific: "Allow access to specific models only",
  selectAllowedModels: "Select Allowed Models",
  searchModelsPlaceholder: "Search models...",
  selectedModelsCount: "{count} models selected",

  // Panel
  centralizedManagement: "Centralized Management",
  refreshModelPool: "Refresh Model Pool",
  refreshing: "Refreshing...",
  totalAccessKeys: "{total} keys",
};

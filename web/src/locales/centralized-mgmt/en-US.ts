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
  priorityHint: "1-999=priority (lower=higher priority), 1000=internal reserved value",
  prioritySortHint:
    "Within the same priority, higher health score = higher selection probability. E.g., two priority=10 groups are weighted by health",
  priorityColumnHint:
    "Lower value = Higher priority (1=highest, 999=lowest, 1000=internal reserved)",
  priorityExplanationHint:
    "💡 Numbers on group tags (e.g., :20, :100) are priorities. Lower value = higher priority. Same priority uses health-weighted random selection",

  // Hub settings
  hubSettings: "Hub Settings",
  maxRetries: "Max Retries",
  maxRetriesHint: "Max retries within the same priority level",
  retryDelay: "Retry Delay",
  healthThreshold: "Health Threshold",
  healthThresholdHint: "Groups below this threshold will be skipped",
  enablePriority: "Enable Priority Routing",
  onlyAggregateGroups: "Only Aggregate Groups",
  onlyAggregateGroupsHint:
    "When enabled, Hub only routes to aggregate groups, ignoring standard groups",

  // Access keys
  accessKeys: "Access Keys",
  accessKeyName: "Name",
  maskedKey: "Key",
  keyCopied: "Key copied",
  keyNameCopied: "Name copied",
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
  onlyAggregateGroupsActive:
    "🔒 Only Aggregate Groups Mode Enabled (Can be changed in Hub Settings)",
  refreshModelPool: "Refresh Model Pool",
  refreshing: "Refreshing...",
  totalAccessKeys: "{total} keys",

  // Usage statistics
  usageCount: "Usage Count",
  lastUsedAt: "Last Used",
  neverUsed: "Never used",
  justNow: "Just now",
  minutesAgo: "{n} minutes ago",
  hoursAgo: "{n} hours ago",
  daysAgo: "{n} days ago",
  monthsAgo: "{n} months ago",
  yearsAgo: "{n} years ago",

  // Batch operations
  batchOperations: "Batch Operations",
  batchDelete: "Batch Delete",
  batchEnable: "Batch Enable",
  batchDisable: "Batch Disable",
  selectedKeys: "{count} keys selected",
  confirmBatchDelete: "Are you sure you want to delete {count} selected access keys?",
  batchDeleteSuccess: "Successfully deleted {count} access keys",
  batchEnableSuccess: "Successfully enabled {count} access keys",
  batchDisableSuccess: "Successfully disabled {count} access keys",
  selectAtLeastOne: "Please select at least one access key",

  // Custom models
  customModels: "Custom Models",
  customModelNames: "Custom Model Names",
  customModelNamesHint: "Add custom model names for aggregate groups, one per line",
  addCustomModel: "Add Model",
  editCustomModels: "Edit Custom Models",
  noCustomModels: "No custom models",
  customModelsUpdated: "Custom models updated",
  aggregateGroupName: "Aggregate Group",
  modelCount: "{count} models",
  customModelBadge: "Custom",
  customModelTooltip: "This is a user-defined custom model name",

  // Routing logic
  routingLogic: "Routing Logic (Sequential)",
  routingStep1:
    "① Path → Format Detection (Chat/Claude/Gemini/Image/Audio). Unknown formats fallback to OpenAI",
  routingStep2: "② Extract model name from request",
  routingStep3: "③ Access Control: Validate key permissions",
  routingStep4: "④ Model Availability: Check if model exists in any enabled group",
  routingStep5:
    "⑤ Group Selection Filters: Health threshold + Enabled status + Channel compatibility + CC support (Claude) + Aggregate preconditions (request size limits, etc.)",
  routingStep6: "⑥ Channel Priority: Native channel > Compatible channel",
  routingStep7: "⑦ Group Selection: Min priority value (lower=higher) → Health-weighted random",
  routingStep8: "⑧ Path Rewrite & Forward: /hub/v1/* → /proxy/group-name/v1/*",
};

<script setup lang="ts">
/**
 * MCP Skills Panel Component
 * Manages MCP services and service groups with skill export functionality
 */
import {
  mcpSkillsApi,
  type APIBridgeTemplate,
  type CreateGroupRequest,
  type CreateServiceRequest,
  type GroupListParams,
  type GroupServicesWithToolsResult,
  type MCPServersConfig,
  type MCPServiceDTO,
  type MCPServiceGroupDTO,
  type MCPServiceType,
  type MCPSkillsExportData,
  type ServiceEndpointInfo,
  type ServiceListParams,
  type ServiceToolsResult,
} from "@/api/mcp-skills";
import { askExportMode, askImportMode } from "@/utils/export-import";
import {
  Add,
  CloudDownloadOutline,
  CloudUploadOutline,
  CopyOutline,
  Key,
  RefreshOutline,
  Search,
  TrashOutline,
} from "@vicons/ionicons5";
import { debounce } from "lodash-es";
import {
  NButton,
  NDataTable,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NPagination,
  NSelect,
  NSpace,
  NSwitch,
  NTabPane,
  NTabs,
  NTag,
  NText,
  useDialog,
  useMessage,
  type DataTableColumns,
  type SelectOption,
} from "naive-ui";
import { computed, h, onMounted, onUnmounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

// Active tab
const activeTab = ref<"services" | "groups">("services");

// Loading states
const loading = ref(false);
const importLoading = ref(false);

// Services state
const services = ref<MCPServiceDTO[]>([]);
const servicePagination = reactive({ page: 1, pageSize: 20, total: 0, totalPages: 0 });
const serviceFilters = reactive({ search: "", enabled: "all", category: "all", type: "all" });

// Groups state
const groups = ref<MCPServiceGroupDTO[]>([]);
const groupPagination = reactive({ page: 1, pageSize: 20, total: 0, totalPages: 0 });
const groupFilters = reactive({ search: "", enabled: "all" });

// Group services with tools for alias configuration
const groupServicesWithTools = ref<GroupServicesWithToolsResult | null>(null);
const loadingGroupServicesWithTools = ref(false);

// Modal states
const showServiceModal = ref(false);
const showGroupModal = ref(false);
const showTemplateModal = ref(false);
const showEndpointModal = ref(false);
const showServiceEndpointModal = ref(false);
const editingService = ref<MCPServiceDTO | null>(null);
const editingGroup = ref<MCPServiceGroupDTO | null>(null);

// Templates
const templates = ref<APIBridgeTemplate[]>([]);
const selectedTemplateId = ref<string | null>(null);
const templateApiKey = ref("");
const templateCustomEndpoint = ref("");

// Computed to get selected template object
// Note: Using 'tpl' instead of 't' to avoid shadowing the i18n t() function
const selectedTemplate = computed(
  () => templates.value.find(tpl => tpl.id === selectedTemplateId.value) || null
);

// Testing state
const testingServices = ref<Set<number>>(new Set());

// Tool expansion state - tracks which services have expanded tool lists

interface ExpandedToolsState {
  loading: boolean;
  data: ServiceToolsResult | null;
  error: string | null;
}
const expandedTools = ref<Map<number, ExpandedToolsState>>(new Map());
const refreshingTools = ref<Set<number>>(new Set());

// Track which tool description is currently expanded (only one at a time)
// Format: "service-{serviceId}-{toolName}" or "group-{groupId}-{serviceName}-{toolName}"
const expandedToolDescription = ref<string | null>(null);

// Toggle tool description expansion
function toggleToolDescriptionExpansion(key: string) {
  if (expandedToolDescription.value === key) {
    expandedToolDescription.value = null;
  } else {
    expandedToolDescription.value = key;
  }
}

// Check if a tool description is expanded
function isToolDescriptionExpanded(key: string): boolean {
  return expandedToolDescription.value === key;
}

// Check if a service's tools are expanded
function isToolsExpanded(serviceId: number): boolean {
  return expandedTools.value.has(serviceId);
}

// Toggle tool expansion for a service (only one can be expanded at a time)
async function toggleToolsExpansion(service: MCPServiceDTO) {
  const serviceId = service.id;

  if (expandedTools.value.has(serviceId)) {
    // Collapse - remove from map
    expandedTools.value.delete(serviceId);
    return;
  }

  // Collapse all other expanded services first
  expandedTools.value.clear();

  // Expand - load tools
  expandedTools.value.set(serviceId, { loading: true, data: null, error: null });

  try {
    const result = await mcpSkillsApi.getServiceTools(serviceId);
    expandedTools.value.set(serviceId, { loading: false, data: result, error: null });
  } catch (e) {
    const errorMsg = e instanceof Error ? e.message : "Unknown error";
    expandedTools.value.set(serviceId, { loading: false, data: null, error: errorMsg });
  }
}

// Refresh tools for a service
async function refreshServiceTools(serviceId: number) {
  if (refreshingTools.value.has(serviceId)) {
    return;
  }

  refreshingTools.value.add(serviceId);
  try {
    const result = await mcpSkillsApi.refreshServiceTools(serviceId);
    expandedTools.value.set(serviceId, { loading: false, data: result, error: null });
    message.success(t("mcpSkills.toolsRefreshed"));
  } catch (e) {
    const errorMsg = e instanceof Error ? e.message : "Unknown error";
    message.error(t("mcpSkills.toolsRefreshFailed", { error: errorMsg }));
  } finally {
    refreshingTools.value.delete(serviceId);
  }
}

// Format date for display
function formatDate(dateStr: string | undefined): string {
  if (!dateStr) {
    return "-";
  }
  try {
    return new Date(dateStr).toLocaleString();
  } catch {
    return dateStr;
  }
}

// Group tool expansion state - tracks which groups have expanded tool lists
interface ExpandedGroupToolsState {
  loading: boolean;
  data: GroupServicesWithToolsResult | null;
  error: string | null;
}
const expandedGroupTools = ref<Map<number, ExpandedGroupToolsState>>(new Map());

// Check if a group's tools are expanded
function isGroupToolsExpanded(groupId: number): boolean {
  return expandedGroupTools.value.has(groupId);
}

// Toggle tool expansion for a group (only one can be expanded at a time)
async function toggleGroupToolsExpansion(group: MCPServiceGroupDTO) {
  const groupId = group.id;

  if (expandedGroupTools.value.has(groupId)) {
    // Collapse - remove from map
    expandedGroupTools.value.delete(groupId);
    return;
  }

  // Collapse all other expanded groups first
  expandedGroupTools.value.clear();

  // Expand - load tools
  expandedGroupTools.value.set(groupId, { loading: true, data: null, error: null });

  try {
    const result = await mcpSkillsApi.getGroupServicesWithTools(groupId);
    expandedGroupTools.value.set(groupId, { loading: false, data: result, error: null });
  } catch (e) {
    const errorMsg = e instanceof Error ? e.message : "Unknown error";
    expandedGroupTools.value.set(groupId, { loading: false, data: null, error: errorMsg });
  }
}

// Get the display description for a tool, considering alias configurations
// Priority: 1. Custom unified description from alias config, 2. Original tool description
function getToolDisplayDescription(
  group: MCPServiceGroupDTO,
  toolName: string,
  originalDescription: string
): string {
  // Check if this tool has a custom description in alias configs
  if (group.tool_alias_configs) {
    // Check if toolName is a canonical name with custom description
    const directConfig = group.tool_alias_configs[toolName];
    if (directConfig?.description) {
      return directConfig.description;
    }
    // Check if toolName is an alias of some canonical name
    for (const [_canonical, config] of Object.entries(group.tool_alias_configs)) {
      if (config.aliases?.includes(toolName) && config.description) {
        return config.description;
      }
    }
  }
  // Fallback to original description
  return originalDescription;
}

// Get the display name for a tool (canonical name if aliased)
function getToolDisplayName(
  group: MCPServiceGroupDTO,
  toolName: string
): { displayName: string; isAliased: boolean; canonicalName?: string } {
  if (group.tool_alias_configs) {
    // Check if toolName is an alias
    for (const [canonical, config] of Object.entries(group.tool_alias_configs)) {
      if (config.aliases?.includes(toolName)) {
        return { displayName: toolName, isAliased: true, canonicalName: canonical };
      }
    }
  } else if (group.tool_aliases) {
    // Fallback to old format
    for (const [canonical, aliases] of Object.entries(group.tool_aliases)) {
      if (aliases.includes(toolName)) {
        return { displayName: toolName, isAliased: true, canonicalName: canonical };
      }
    }
  }
  return { displayName: toolName, isAliased: false };
}

// JSON import state
const showJsonImportModal = ref(false);
const jsonImportText = ref("");
const jsonImportLoading = ref(false);
const jsonFileInputRef = ref<HTMLInputElement | null>(null);
const selectedJsonFileName = ref("");

// JSON import placeholder - defined here to avoid i18n parsing issues with curly braces
const jsonImportPlaceholderText = `{
  "mcpServers": {
    "my-server": {
      "command": "npx",
      "args": ["-y", "@some/mcp-server"],
      "env": {}
    }
  }
}`;

// Group endpoint info
const endpointInfo = ref<{
  aggregation_endpoint: string;
  skill_export_url: string;
  mcp_config_json: string;
} | null>(null);
const endpointGroupName = ref("");

// Service endpoint info
const serviceEndpointInfo = ref<ServiceEndpointInfo | null>(null);
const serviceEndpointName = ref("");

// File input ref
const fileInputRef = ref<HTMLInputElement | null>(null);

// Service form
const serviceForm = reactive<CreateServiceRequest>({
  name: "",
  display_name: "",
  description: "",
  category: "custom",
  icon: "🔧",
  sort: 0,
  enabled: true,
  type: "api_bridge",
  command: "",
  args: [],
  cwd: "",
  api_endpoint: "",
  api_key_name: "",
  api_key_value: "",
  api_key_header: "",
  api_key_prefix: "",
  tools: [],
  rpd_limit: 0,
  mcp_enabled: false,
});
const apiKeyInput = ref("");
const argsInput = ref("");
const cwdInput = ref("");

// Dynamic environment variables for service form
interface EnvVarItem {
  id: string;
  key: string;
  value: string;
  enabled: boolean;
}
const envVars = ref<EnvVarItem[]>([]);

// Counter for generating unique env var IDs within component lifecycle
// Using counter instead of Date.now() + random for guaranteed uniqueness and simplicity
let envVarIdCounter = 0;

function addEnvVar() {
  envVars.value.push({
    id: `env-${++envVarIdCounter}`,
    key: "",
    value: "",
    enabled: true,
  });
}

function removeEnvVar(id: string) {
  envVars.value = envVars.value.filter(v => v.id !== id);
}

function toggleEnvVar(id: string) {
  const item = envVars.value.find(v => v.id === id);
  if (item) {
    item.enabled = !item.enabled;
  }
}

// Convert envVars to Record<string, string> for API
function getEnvVarsAsRecord(): Record<string, string> {
  const result: Record<string, string> = {};
  for (const v of envVars.value) {
    if (v.enabled && v.key.trim()) {
      result[v.key.trim()] = v.value;
    }
  }
  return result;
}

// Load envVars from Record<string, string>
// AI review: Use counter-based ID generation for consistency with addEnvVar()
function loadEnvVarsFromRecord(record: Record<string, string> | undefined) {
  envVars.value = [];
  if (record) {
    for (const [key, value] of Object.entries(record)) {
      envVars.value.push({
        id: `env-${++envVarIdCounter}`,
        key,
        value,
        enabled: true,
      });
    }
  }
}

// Group form
const groupForm = reactive<
  CreateGroupRequest & {
    tool_alias_configs?: Record<string, { aliases: string[]; description?: string }>;
  }
>({
  name: "",
  display_name: "",
  description: "",
  service_ids: [],
  service_weights: {},
  tool_aliases: {},
  tool_alias_configs: undefined,
  enabled: true,
  aggregation_enabled: false,
  access_token: "",
});

// Service access token input (separate from serviceForm for edit mode)
const serviceAccessTokenInput = ref("");

// Generate random access token for MCP endpoints
// Uses cryptographically secure random when available, falls back to Math.random
function generateRandomAccessToken(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const length = 32;
  let result = "";

  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    const randomValues = new Uint32Array(length);
    crypto.getRandomValues(randomValues);
    for (let i = 0; i < length; i++) {
      const value = randomValues[i] ?? 0;
      result += chars.charAt(value % chars.length);
    }
    return result;
  }

  for (let i = 0; i < length; i++) {
    result += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return result;
}

// Generate and set access token for group form
function generateGroupAccessToken() {
  groupForm.access_token = generateRandomAccessToken();
  message.success(t("mcpSkills.tokenGenerated"));
}

// Generate and set access token for service form
function generateServiceAccessToken() {
  serviceAccessTokenInput.value = generateRandomAccessToken();
  message.success(t("mcpSkills.tokenGenerated"));
}

// Options
const typeOptions = computed<SelectOption[]>(() => [
  { label: t("mcpSkills.typeStdio"), value: "stdio" },
  { label: t("mcpSkills.typeSse"), value: "sse" },
  { label: t("mcpSkills.typeStreamableHttp"), value: "streamable_http" },
  { label: t("mcpSkills.typeApiBridge"), value: "api_bridge" },
]);

// Category options matching backend ServiceCategory enum in models.go
const categoryOptions = computed<SelectOption[]>(() => [
  { label: t("mcpSkills.categorySearch"), value: "search" },
  { label: t("mcpSkills.categoryFetch"), value: "fetch" },
  { label: t("mcpSkills.categoryAI"), value: "ai" },
  { label: t("mcpSkills.categoryUtility"), value: "utility" },
  { label: t("mcpSkills.categoryStorage"), value: "storage" },
  { label: t("mcpSkills.categoryDatabase"), value: "database" },
  { label: t("mcpSkills.categoryFilesystem"), value: "filesystem" },
  { label: t("mcpSkills.categoryBrowser"), value: "browser" },
  { label: t("mcpSkills.categoryCommunication"), value: "communication" },
  { label: t("mcpSkills.categoryDevelopment"), value: "development" },
  { label: t("mcpSkills.categoryCloud"), value: "cloud" },
  { label: t("mcpSkills.categoryMonitoring"), value: "monitoring" },
  { label: t("mcpSkills.categoryProductivity"), value: "productivity" },
  { label: t("mcpSkills.categoryCustom"), value: "custom" },
]);

const enabledFilterOptions = computed<SelectOption[]>(() => [
  { label: t("mcpSkills.filterEnabledAll"), value: "all" },
  { label: t("mcpSkills.filterEnabledYes"), value: "true" },
  { label: t("mcpSkills.filterEnabledNo"), value: "false" },
]);

const categoryFilterOptions = computed<SelectOption[]>(() => [
  { label: t("mcpSkills.filterCategoryAll"), value: "all" },
  ...categoryOptions.value,
]);

const typeFilterOptions = computed<SelectOption[]>(() => [
  { label: t("mcpSkills.filterTypeAll"), value: "all" },
  ...typeOptions.value,
]);

// Service options with disabled status indicator
const serviceOptions = computed<SelectOption[]>(() =>
  services.value.map(s => ({
    label: s.enabled
      ? `${s.icon} ${s.display_name || s.name}`
      : `${s.icon} ${s.display_name || s.name} (${t("mcpSkills.disabled")})`,
    value: s.id,
    disabled: false, // Allow selection but show disabled status
    style: s.enabled ? undefined : { opacity: 0.5 },
  }))
);

// Helper to check if a service is enabled
function isServiceEnabled(serviceId: number): boolean {
  const service = services.value.find(s => s.id === serviceId);
  return service?.enabled ?? true;
}

// Count enabled services in group
function countEnabledServices(serviceIds: number[]): number {
  return serviceIds.filter(id => isServiceEnabled(id)).length;
}

// Load services
async function loadServices() {
  loading.value = true;
  try {
    const params: ServiceListParams = {
      page: servicePagination.page,
      page_size: servicePagination.pageSize,
      search: serviceFilters.search || undefined,
      category: serviceFilters.category === "all" ? undefined : serviceFilters.category,
      type: serviceFilters.type === "all" ? undefined : serviceFilters.type,
      enabled: serviceFilters.enabled === "all" ? null : serviceFilters.enabled === "true",
    };
    const result = await mcpSkillsApi.listServicesPaginated(params);
    services.value = result.services;
    servicePagination.total = result.total;
    servicePagination.totalPages = result.total_pages;
  } finally {
    loading.value = false;
  }
}

async function loadAllServices() {
  try {
    services.value = await mcpSkillsApi.listServices();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

async function loadGroups() {
  loading.value = true;
  try {
    // Load all services first to calculate enabled service counts
    await loadAllServices();

    const params: GroupListParams = {
      page: groupPagination.page,
      page_size: groupPagination.pageSize,
      search: groupFilters.search || undefined,
      enabled: groupFilters.enabled === "all" ? null : groupFilters.enabled === "true",
    };
    const result = await mcpSkillsApi.listGroupsPaginated(params);
    groups.value = result.groups;
    groupPagination.total = result.total;
    groupPagination.totalPages = result.total_pages;
  } finally {
    loading.value = false;
  }
}

async function loadTemplates() {
  try {
    templates.value = await mcpSkillsApi.getTemplates();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

const debouncedServiceSearch = debounce(() => {
  servicePagination.page = 1;
  loadServices();
}, 300);

const debouncedGroupSearch = debounce(() => {
  groupPagination.page = 1;
  loadGroups();
}, 300);

onUnmounted(() => {
  debouncedServiceSearch.cancel();
  debouncedGroupSearch.cancel();
});

watch(() => serviceFilters.search, debouncedServiceSearch);
watch(
  () => [serviceFilters.enabled, serviceFilters.category, serviceFilters.type],
  () => {
    servicePagination.page = 1;
    loadServices();
  }
);

watch(() => groupFilters.search, debouncedGroupSearch);
watch(
  () => groupFilters.enabled,
  () => {
    groupPagination.page = 1;
    loadGroups();
  }
);

function resetServiceForm() {
  Object.assign(serviceForm, {
    name: "",
    display_name: "",
    description: "",
    category: "custom",
    icon: "🔧",
    sort: 0,
    enabled: true,
    type: "api_bridge",
    command: "",
    args: [],
    cwd: "",
    api_endpoint: "",
    api_key_name: "",
    api_key_value: "",
    api_key_header: "",
    api_key_prefix: "",
    tools: [],
    rpd_limit: 0,
    mcp_enabled: false,
  });
  apiKeyInput.value = "";
  argsInput.value = "";
  cwdInput.value = "";
  envVars.value = [];
  serviceAccessTokenInput.value = "";
}

function openCreateService() {
  editingService.value = null;
  resetServiceForm();
  showServiceModal.value = true;
}

function openEditService(service: MCPServiceDTO) {
  editingService.value = service;
  Object.assign(serviceForm, {
    name: service.name,
    display_name: service.display_name,
    description: service.description,
    category: service.category,
    icon: service.icon,
    sort: service.sort,
    enabled: service.enabled,
    type: service.type as MCPServiceType,
    command: service.command || "",
    args: service.args || [],
    cwd: service.cwd || "",
    api_endpoint: service.api_endpoint || "",
    api_key_name: service.api_key_name || "",
    api_key_header: service.api_key_header || "",
    api_key_prefix: service.api_key_prefix || "",
    tools: service.tools || [],
    rpd_limit: service.rpd_limit,
    mcp_enabled: service.mcp_enabled,
  });
  apiKeyInput.value = "";
  argsInput.value = (service.args || []).join("\n");
  cwdInput.value = service.cwd || "";
  loadEnvVarsFromRecord(service.default_envs);
  showServiceModal.value = true;
}

async function submitService() {
  if (!serviceForm.name.trim()) {
    message.warning(t("mcpSkills.nameRequired"));
    return;
  }
  if (!serviceForm.display_name.trim()) {
    message.warning(t("mcpSkills.displayNameRequired"));
    return;
  }
  serviceForm.args = argsInput.value
    .split("\n")
    .map(s => s.trim())
    .filter(Boolean);
  serviceForm.cwd = cwdInput.value.trim();
  // Convert env vars to default_envs
  const defaultEnvs = getEnvVarsAsRecord();
  try {
    if (editingService.value) {
      const payload: Record<string, unknown> = { ...serviceForm, default_envs: defaultEnvs };
      if (apiKeyInput.value.trim()) {
        payload.api_key_value = apiKeyInput.value;
      }
      // Only include access_token if user provided one
      if (serviceAccessTokenInput.value.trim()) {
        payload.access_token = serviceAccessTokenInput.value;
      }
      await mcpSkillsApi.updateService(editingService.value.id, payload);
      message.success(t("mcpSkills.serviceUpdated"));
    } else {
      await mcpSkillsApi.createService({
        ...serviceForm,
        api_key_value: apiKeyInput.value,
        default_envs: defaultEnvs,
        access_token: serviceAccessTokenInput.value || undefined,
      });
      message.success(t("mcpSkills.serviceCreated"));
    }
    showServiceModal.value = false;
    await loadServices();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

function confirmDeleteService(service: MCPServiceDTO) {
  dialog.warning({
    title: t("mcpSkills.deleteService"),
    content: t("mcpSkills.confirmDeleteService", { name: service.display_name || service.name }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      try {
        await mcpSkillsApi.deleteService(service.id);
        message.success(t("mcpSkills.serviceDeleted"));
        await loadServices();
      } catch (_) {
        /* handled by centralized error handler */
      }
    },
  });
}

async function toggleService(service: MCPServiceDTO) {
  try {
    await mcpSkillsApi.toggleService(service.id);
    await loadServices();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

function resetGroupForm() {
  Object.assign(groupForm, {
    name: "",
    display_name: "",
    description: "",
    service_ids: [],
    service_weights: {},
    tool_aliases: {},
    tool_alias_configs: undefined, // Reset to prevent stale alias configs leaking into new group
    enabled: true,
    aggregation_enabled: false,
    access_token: "",
  });
}

function openCreateGroup() {
  editingGroup.value = null;
  groupServicesWithTools.value = null;
  resetGroupForm();
  syncToolAliasesToEntries();
  loadAllServices();
  showGroupModal.value = true;
}

async function openEditGroup(group: MCPServiceGroupDTO) {
  editingGroup.value = group;
  Object.assign(groupForm, {
    name: group.name,
    display_name: group.display_name,
    description: group.description,
    service_ids: group.service_ids || [],
    service_weights: group.service_weights || {},
    tool_aliases: group.tool_aliases || {},
    tool_alias_configs: group.tool_alias_configs || undefined,
    enabled: group.enabled,
    aggregation_enabled: group.aggregation_enabled,
    access_token: group.access_token || "",
  });
  syncToolAliasesToEntries();
  loadAllServices();
  showGroupModal.value = true;

  // Load services with tools for alias configuration
  if (group.id && group.aggregation_enabled) {
    loadingGroupServicesWithTools.value = true;
    try {
      groupServicesWithTools.value = await mcpSkillsApi.getGroupServicesWithTools(group.id);
    } catch (_err) {
      groupServicesWithTools.value = null;
    } finally {
      loadingGroupServicesWithTools.value = false;
    }
  } else {
    groupServicesWithTools.value = null;
  }
}

// Get weight for a service (default 100)
function getServiceWeight(serviceId: number): number {
  return groupForm.service_weights?.[serviceId] ?? 100;
}

// Set weight for a service
function setServiceWeight(serviceId: number, weight: number) {
  if (!groupForm.service_weights) {
    groupForm.service_weights = {};
  }
  groupForm.service_weights[serviceId] = weight;
}

// Tool alias management - use array structure to avoid key-based re-rendering issues
interface ToolAliasEntry {
  id: number;
  canonicalName: string;
  aliases: string;
  description: string; // Unified description for this alias group
  expanded: boolean; // UI state: whether to show tool descriptions
}

// Tool description info from services
interface ToolDescriptionInfo {
  serviceName: string;
  toolName: string;
  description: string;
}

const toolAliasEntries = ref<ToolAliasEntry[]>([]);
let toolAliasIdCounter = 0;

// Get tool descriptions for a given canonical name and its aliases
function getToolDescriptionsForAlias(entry: ToolAliasEntry): ToolDescriptionInfo[] {
  const result: ToolDescriptionInfo[] = [];
  const toolNames = new Set<string>();

  // Add canonical name
  if (entry.canonicalName.trim()) {
    toolNames.add(entry.canonicalName.trim());
  }

  // Add aliases
  entry.aliases.split(",").forEach(alias => {
    const trimmed = alias.trim();
    if (trimmed) {
      toolNames.add(trimmed);
    }
  });

  // Search in group services
  if (groupServicesWithTools.value) {
    for (const svc of groupServicesWithTools.value.services) {
      for (const tool of svc.tools) {
        if (toolNames.has(tool.name)) {
          result.push({
            serviceName: svc.service_name,
            toolName: tool.name,
            description: tool.description || "",
          });
        }
      }
    }
  }

  return result;
}

// Sync tool_aliases from groupForm to toolAliasEntries (when opening modal)
function syncToolAliasesToEntries() {
  toolAliasEntries.value = [];
  toolAliasIdCounter = 0;

  // Try new format first (tool_alias_configs)
  if (groupForm.tool_alias_configs && Object.keys(groupForm.tool_alias_configs).length > 0) {
    for (const [canonicalName, config] of Object.entries(groupForm.tool_alias_configs)) {
      toolAliasEntries.value.push({
        id: ++toolAliasIdCounter,
        canonicalName,
        aliases: config.aliases?.join(", ") || "",
        description: config.description || "",
        expanded: false,
      });
    }
  } else if (groupForm.tool_aliases) {
    // Fallback to old format
    for (const [canonicalName, aliases] of Object.entries(groupForm.tool_aliases)) {
      toolAliasEntries.value.push({
        id: ++toolAliasIdCounter,
        canonicalName,
        aliases: aliases.join(", "),
        description: "",
        expanded: false,
      });
    }
  }
}

// Sync toolAliasEntries back to groupForm (when saving)
function syncEntriesToToolAliases() {
  const configs: Record<string, { aliases: string[]; description?: string }> = {};
  const simpleAliases: Record<string, string[]> = {};

  for (const entry of toolAliasEntries.value) {
    const name = entry.canonicalName.trim();
    if (!name) {
      continue;
    }
    const aliases = entry.aliases
      .split(",")
      .map(s => s.trim())
      .filter(s => s.length > 0);
    if (aliases.length > 0 || entry.description.trim()) {
      configs[name] = {
        aliases,
        ...(entry.description.trim() ? { description: entry.description.trim() } : {}),
      };
      simpleAliases[name] = aliases;
    }
  }

  // Use new format if any entry has description, otherwise use old format for compatibility
  const hasDescription = Object.values(configs).some(c => c.description);
  if (hasDescription) {
    groupForm.tool_alias_configs = configs;
    groupForm.tool_aliases = simpleAliases; // Keep for backward compatibility
  } else {
    groupForm.tool_aliases = simpleAliases;
    groupForm.tool_alias_configs = undefined;
  }
}

// Add a new tool alias entry
function addToolAliasEntry() {
  toolAliasEntries.value.push({
    id: ++toolAliasIdCounter,
    canonicalName: "",
    aliases: "",
    description: "",
    expanded: false,
  });
}

// Remove a tool alias entry by id
function removeToolAliasEntry(id: number) {
  const idx = toolAliasEntries.value.findIndex(e => e.id === id);
  if (idx !== -1) {
    toolAliasEntries.value.splice(idx, 1);
  }
}

// Toggle expanded state for an entry
function toggleAliasExpanded(entry: ToolAliasEntry) {
  entry.expanded = !entry.expanded;
}

async function submitGroup() {
  if (!groupForm.name.trim()) {
    message.warning(t("mcpSkills.nameRequired"));
    return;
  }
  if (!groupForm.display_name.trim()) {
    message.warning(t("mcpSkills.displayNameRequired"));
    return;
  }
  // Sync tool alias entries back to groupForm before saving
  syncEntriesToToolAliases();
  try {
    if (editingGroup.value) {
      const payload: Record<string, unknown> = { ...groupForm };
      if (!groupForm.access_token?.trim()) {
        delete payload.access_token;
      }
      await mcpSkillsApi.updateGroup(editingGroup.value.id, payload);
      message.success(t("mcpSkills.groupUpdated"));
    } else {
      await mcpSkillsApi.createGroup(groupForm);
      message.success(t("mcpSkills.groupCreated"));
    }
    showGroupModal.value = false;
    await loadGroups();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

function confirmDeleteGroup(group: MCPServiceGroupDTO) {
  dialog.warning({
    title: t("mcpSkills.deleteGroup"),
    content: t("mcpSkills.confirmDeleteGroup", { name: group.display_name || group.name }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      try {
        await mcpSkillsApi.deleteGroup(group.id);
        message.success(t("mcpSkills.groupDeleted"));
        await loadGroups();
      } catch (_) {
        /* handled by centralized error handler */
      }
    },
  });
}

async function toggleGroup(group: MCPServiceGroupDTO) {
  try {
    await mcpSkillsApi.toggleGroup(group.id);
    await loadGroups();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

async function showGroupEndpoint(group: MCPServiceGroupDTO) {
  try {
    const info = await mcpSkillsApi.getGroupEndpointInfo(group.id);
    endpointInfo.value = info;
    endpointGroupName.value = group.display_name || group.name;
    showEndpointModal.value = true;
  } catch (_) {
    /* handled by centralized error handler */
  }
}

async function showServiceEndpoint(service: MCPServiceDTO) {
  try {
    const info = await mcpSkillsApi.getServiceEndpointInfo(service.id);
    serviceEndpointInfo.value = info;
    serviceEndpointName.value = service.display_name || service.name;
    showServiceEndpointModal.value = true;
  } catch (_) {
    /* handled by centralized error handler */
  }
}

async function copyToClipboard(text: string, msgKey: string) {
  try {
    await navigator.clipboard.writeText(text);
    message.success(t(msgKey));
  } catch (_) {
    message.error(t("mcpSkills.copyFailed"));
  }
}

async function exportGroupAsSkill(group: MCPServiceGroupDTO) {
  try {
    const blob = await mcpSkillsApi.exportGroupAsSkill(group.id);
    const filename = `gpt-load-${group.name.replace(/_/g, "-")}.zip`;

    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);

    // Delay URL revocation for download manager compatibility
    setTimeout(() => URL.revokeObjectURL(url), 60000);

    message.success(t("mcpSkills.skillExported"));
  } catch (e) {
    if (e instanceof Error) {
      message.error(e.message);
    }
  }
}

function openTemplateModal() {
  selectedTemplateId.value = null;
  templateApiKey.value = "";
  templateCustomEndpoint.value = "";
  loadTemplates();
  showTemplateModal.value = true;
}

async function createFromTemplate() {
  if (!selectedTemplate.value) {
    message.warning(t("mcpSkills.templates"));
    return;
  }
  try {
    // Use custom endpoint if provided, otherwise undefined to use template default
    const customEndpoint = templateCustomEndpoint.value.trim() || undefined;
    await mcpSkillsApi.createFromTemplate(
      selectedTemplate.value.id,
      templateApiKey.value,
      customEndpoint
    );
    message.success(t("mcpSkills.templateCreated"));
    showTemplateModal.value = false;
    await loadServices();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

async function testService(service: MCPServiceDTO) {
  if (testingServices.value.has(service.id)) {
    return;
  }
  testingServices.value.add(service.id);
  try {
    const result = await mcpSkillsApi.testService(service.id);
    if (result.success) {
      message.success(t("mcpSkills.testSuccess", { name: service.display_name || service.name }));
      // Refresh service list to update tool_count and mcp_enabled status
      await loadServices();
    } else {
      message.error(t("mcpSkills.testFailed", { error: result.error || "Unknown error" }));
    }
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    testingServices.value.delete(service.id);
  }
}

function openJsonImportModal() {
  jsonImportText.value = "";
  selectedJsonFileName.value = "";
  showJsonImportModal.value = true;
}

// Trigger file input click
function triggerJsonFileInput() {
  jsonFileInputRef.value?.click();
}

// Handle JSON file selection
async function handleJsonFileSelect(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) {
    return;
  }

  selectedJsonFileName.value = file.name;

  try {
    const content = await file.text();
    jsonImportText.value = content;
  } catch (_err) {
    message.error(t("mcpSkills.fileReadError"));
    selectedJsonFileName.value = "";
  }

  // Reset input to allow selecting the same file again
  input.value = "";
}

async function importFromJson() {
  const text = jsonImportText.value.trim();
  if (!text) {
    message.warning(t("mcpSkills.jsonImportEmpty"));
    return;
  }

  let config: MCPServersConfig;
  try {
    config = JSON.parse(text);
  } catch (_) {
    message.error(t("mcpSkills.importInvalidJSON"));
    return;
  }

  // Validate structure
  if (!config.mcpServers || typeof config.mcpServers !== "object") {
    message.error(t("mcpSkills.jsonImportInvalidFormat"));
    return;
  }

  const serverCount = Object.keys(config.mcpServers).length;
  if (serverCount === 0) {
    message.warning(t("mcpSkills.jsonImportNoServers"));
    return;
  }

  jsonImportLoading.value = true;
  try {
    const result = await mcpSkillsApi.importMCPServers(config);
    if (result.imported > 0) {
      message.success(
        t("mcpSkills.jsonImportSuccess", {
          imported: result.imported,
          skipped: result.skipped,
        })
      );
      showJsonImportModal.value = false;
      await loadServices();
    } else {
      message.warning(
        t("mcpSkills.jsonImportAllSkipped", {
          skipped: result.skipped,
        })
      );
    }
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    jsonImportLoading.value = false;
  }
}

async function handleExport() {
  try {
    const mode = await askExportMode(dialog, t);
    const data = await mcpSkillsApi.exportAll(mode);
    const jsonStr = JSON.stringify(data, null, 2);
    const blob = new Blob([jsonStr], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    const timestamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
    const suffix = mode === "plain" ? "plain" : "enc";
    a.download = `mcp-skills_${timestamp}-${suffix}.json`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    message.success(t("mcpSkills.exportSuccess"));
  } catch (_) {
    /* handled by centralized error handler */
  }
}

// Delete all confirmation input
const deleteAllConfirmInput = ref("");
const deleteAllLoading = ref(false);

// Confirm delete all services with text input confirmation
async function confirmDeleteAll() {
  // First get the count of all services
  let serviceCount = 0;
  try {
    serviceCount = await mcpSkillsApi.countAllServices();
  } catch (_) {
    return;
  }

  if (serviceCount === 0) {
    message.info(t("mcpSkills.deleteAllNone"));
    return;
  }

  // Show confirmation dialog with input
  deleteAllConfirmInput.value = "";
  dialog.create({
    title: t("mcpSkills.deleteAll"),
    content: () =>
      h("div", null, [
        h("p", null, [
          t("mcpSkills.deleteAllWarning", { count: serviceCount }),
          h(
            "strong",
            { style: { color: "#d03050", userSelect: "all", cursor: "pointer" } },
            t("mcpSkills.deleteAllConfirmText")
          ),
          h("span", null, " "),
          t("mcpSkills.toConfirmDeletion"),
        ]),
        h(
          "div",
          { style: "margin-top: 12px;" },
          h(NInput, {
            value: deleteAllConfirmInput.value,
            "onUpdate:value": (v: string) => {
              deleteAllConfirmInput.value = v;
            },
            placeholder: t("mcpSkills.deleteAllPlaceholder"),
          })
        ),
      ]),
    positiveText: t("mcpSkills.confirmDelete"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      if (deleteAllConfirmInput.value !== t("mcpSkills.deleteAllConfirmText")) {
        message.error(t("mcpSkills.incorrectConfirmText"));
        return false;
      }
      deleteAllLoading.value = true;
      try {
        const result = await mcpSkillsApi.deleteAllServices();
        if (result.deleted > 0) {
          message.success(t("mcpSkills.deleteAllSuccess", { count: result.deleted }));
          await loadServices();
          await loadGroups();
        } else {
          message.info(t("mcpSkills.deleteAllNone"));
        }
      } catch (_) {
        /* handled by centralized error handler */
        return false;
      } finally {
        deleteAllLoading.value = false;
      }
    },
  });
}

function triggerImport() {
  fileInputRef.value?.click();
}

async function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) {
    return;
  }
  try {
    const text = await file.text();
    const data = JSON.parse(text) as MCPSkillsExportData;
    if (!data.services && !data.groups) {
      message.error(t("mcpSkills.importInvalidFormat"));
      input.value = "";
      return;
    }
    const mode = await askImportMode(dialog, t);
    importLoading.value = true;
    const result = await mcpSkillsApi.importAll(data, mode === "auto" ? undefined : mode);
    message.success(
      t("mcpSkills.importSuccess", {
        services: result.services_imported,
        groups: result.groups_imported,
      })
    );
    await loadServices();
    await loadGroups();
  } catch (e) {
    if (e instanceof SyntaxError) {
      message.error(t("mcpSkills.importInvalidJSON"));
    } else if (e instanceof Error) {
      // Handle non-JSON errors (network issues, server errors, etc.)
      // AI review suggestion: show generic error message for import failures
      message.error(t("mcpSkills.importFailed", { error: e.message }));
    }
  } finally {
    importLoading.value = false;
    input.value = "";
  }
}

const serviceColumns = computed<DataTableColumns<MCPServiceDTO>>(() => [
  {
    title: "#",
    key: "index",
    width: 50,
    align: "center",
    render: (_row, rowIndex) => {
      // Calculate actual row number based on pagination
      const rowNum = (servicePagination.page - 1) * servicePagination.pageSize + rowIndex + 1;
      return h(NText, { depth: 3 }, () => rowNum);
    },
  },
  {
    title: t("mcpSkills.name"),
    key: "name",
    width: 200,
    render: row =>
      h("div", { style: "display: flex; flex-direction: column; gap: 2px;" }, [
        h("div", { style: "display: flex; align-items: center; gap: 6px;" }, [
          h("span", null, row.icon),
          h("span", { style: "font-weight: 500;" }, row.display_name || row.name),
        ]),
        // Show command/url info below name
        row.command
          ? h(
              "code",
              {
                style:
                  "font-size: 11px; color: var(--n-text-color-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 180px;",
                title: row.command,
              },
              row.command
            )
          : row.api_endpoint
            ? h(
                "code",
                {
                  style:
                    "font-size: 11px; color: var(--n-text-color-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 180px;",
                  title: row.api_endpoint,
                },
                row.api_endpoint
              )
            : null,
      ]),
  },
  {
    title: t("mcpSkills.type"),
    key: "type",
    width: 90,
    align: "center",
    render: row => {
      const typeColors: Record<string, "info" | "success" | "warning" | "error" | "default"> = {
        stdio: "warning",
        sse: "success",
        streamable_http: "info",
        api_bridge: "default",
      };
      return h(
        NTag,
        { size: "small", bordered: false, type: typeColors[row.type] || "default" },
        () => row.type
      );
    },
  },
  {
    title: t("mcpSkills.serviceInfo"),
    key: "info",
    width: 180,
    render: row => {
      const items: ReturnType<typeof h>[] = [];
      // Show args count (for stdio type)
      const argsLen = row.args?.length ?? 0;
      if (argsLen > 0) {
        items.push(
          h(NTag, { size: "tiny", bordered: false }, () => `${argsLen} ${t("mcpSkills.argsCount")}`)
        );
      }
      // Show env count (for stdio type)
      const envLen = row.default_envs ? Object.keys(row.default_envs).length : 0;
      if (envLen > 0) {
        items.push(
          h(NTag, { size: "tiny", bordered: false }, () => `${envLen} ${t("mcpSkills.envCount")}`)
        );
      }

      // Show tool count with expand/collapse button
      const toolCount = row.tool_count || 0;
      const isExpanded = isToolsExpanded(row.id);
      const toolState = expandedTools.value.get(row.id);
      const isLoading = toolState?.loading ?? false;

      items.push(
        h(
          NButton,
          {
            size: "tiny",
            text: true,
            type: isExpanded ? "primary" : "default",
            loading: isLoading,
            onClick: (e: Event) => {
              e.stopPropagation();
              toggleToolsExpansion(row);
            },
          },
          () =>
            h("span", { style: "display: flex; align-items: center; gap: 2px;" }, [
              h("span", null, isExpanded ? "▼" : "▶"),
              h("span", null, t("mcpSkills.toolCount", { count: toolCount })),
            ])
        )
      );
      // Show API key status
      if (row.has_api_key) {
        items.push(h(NTag, { size: "tiny", bordered: false, type: "success" }, () => "API Key ✓"));
      }
      return items.length > 0
        ? h(NSpace, { size: 4, wrap: true }, () => items)
        : h(NText, { depth: 3 }, () => "-");
    },
  },
  {
    title: t("mcpSkills.enabled"),
    key: "enabled",
    width: 70,
    align: "center",
    render: row =>
      h(NSwitch, { size: "small", value: row.enabled, onUpdateValue: () => toggleService(row) }),
  },
  {
    title: t("common.actions"),
    key: "actions",
    width: 220,
    fixed: "right",
    align: "center",
    render: row =>
      h(NSpace, { size: 4, justify: "center" }, () => [
        h(
          NButton,
          {
            size: "tiny",
            secondary: true,
            loading: testingServices.value.has(row.id),
            onClick: () => testService(row),
          },
          () => t("mcpSkills.test")
        ),
        h(NButton, { size: "tiny", secondary: true, onClick: () => openEditService(row) }, () =>
          t("common.edit")
        ),
        h(NButton, { size: "tiny", secondary: true, onClick: () => showServiceEndpoint(row) }, () =>
          t("mcpSkills.endpointInfo")
        ),
        h(
          NButton,
          {
            size: "tiny",
            secondary: true,
            type: "error",
            onClick: () => confirmDeleteService(row),
          },
          () => t("common.delete")
        ),
      ]),
  },
]);

const groupColumns = computed<DataTableColumns<MCPServiceGroupDTO>>(() => [
  {
    title: t("mcpSkills.name"),
    key: "name",
    width: 180,
    render: row => h("span", null, row.display_name || row.name),
  },
  {
    title: t("mcpSkills.services"),
    key: "service_count",
    width: 100,
    align: "center",
    render: row => {
      // Calculate enabled service count from loaded services
      const enabledCount = countEnabledServices(row.service_ids || []);
      const totalCount = row.service_count;
      const hasDisabled = enabledCount < totalCount;

      return h(NText, { type: hasDisabled ? "warning" : undefined }, () =>
        hasDisabled
          ? `${enabledCount}/${totalCount}`
          : t("mcpSkills.serviceCount", { count: totalCount })
      );
    },
  },
  {
    title: t("mcpSkills.tools"),
    key: "total_tool_count",
    width: 120,
    align: "center",
    render: row => {
      const isExpanded = isGroupToolsExpanded(row.id);
      const toolState = expandedGroupTools.value.get(row.id);
      const isLoading = toolState?.loading ?? false;

      // Show unique count if there are duplicates (same tool name across services) or aliases
      const uniqueCount = row.unique_tool_count ?? row.total_tool_count;
      const totalCount = row.total_tool_count ?? 0;
      const hasDuplicates = uniqueCount < totalCount;
      const displayCount = hasDuplicates ? uniqueCount : totalCount;
      const tooltipText = hasDuplicates
        ? `${totalCount} ${t("mcpSkills.totalTools")} → ${uniqueCount} ${t("mcpSkills.uniqueTools")}`
        : "";

      return h(
        NButton,
        {
          size: "tiny",
          text: true,
          type: isExpanded ? "primary" : "default",
          loading: isLoading,
          title: tooltipText,
          onClick: (e: Event) => {
            e.stopPropagation();
            toggleGroupToolsExpansion(row);
          },
        },
        () =>
          h("span", { style: "display: flex; align-items: center; gap: 2px;" }, [
            h("span", null, isExpanded ? "▼" : "▶"),
            h(
              "span",
              null,
              hasDuplicates
                ? `${displayCount}/${totalCount}`
                : t("mcpSkills.toolCount", { count: totalCount })
            ),
          ])
      );
    },
  },
  {
    title: t("mcpSkills.aggregationEnabled"),
    key: "aggregation_enabled",
    width: 100,
    align: "center",
    render: row =>
      h(NTag, { size: "small", type: row.aggregation_enabled ? "success" : "default" }, () =>
        row.aggregation_enabled ? t("common.yes") : t("common.no")
      ),
  },
  {
    title: t("mcpSkills.enabled"),
    key: "enabled",
    width: 70,
    align: "center",
    render: row =>
      h(NSwitch, { size: "small", value: row.enabled, onUpdateValue: () => toggleGroup(row) }),
  },
  {
    title: t("common.actions"),
    key: "actions",
    width: 220,
    fixed: "right",
    align: "center",
    render: row =>
      h(NSpace, { size: 4, justify: "center" }, () => [
        h(NButton, { size: "tiny", secondary: true, onClick: () => openEditGroup(row) }, () =>
          t("common.edit")
        ),
        h(NButton, { size: "tiny", secondary: true, onClick: () => showGroupEndpoint(row) }, () =>
          t("mcpSkills.endpointInfo")
        ),
        h(NButton, { size: "tiny", secondary: true, onClick: () => exportGroupAsSkill(row) }, () =>
          t("mcpSkills.exportAsSkill")
        ),
        h(
          NButton,
          { size: "tiny", secondary: true, type: "error", onClick: () => confirmDeleteGroup(row) },
          () => t("common.delete")
        ),
      ]),
  },
]);

function handleServicePageChange(page: number) {
  servicePagination.page = page;
  loadServices();
}

function handleGroupPageChange(page: number) {
  groupPagination.page = page;
  loadGroups();
}

function handleTabChange(tab: "services" | "groups") {
  activeTab.value = tab;
  if (tab === "services") {
    loadServices();
  } else {
    loadGroups();
  }
}

onMounted(() => {
  loadServices();
});
</script>

<template>
  <div class="mcp-skills-panel">
    <input
      ref="fileInputRef"
      type="file"
      accept=".json"
      style="display: none"
      @change="handleFileChange"
    />
    <div class="panel-header">
      <n-space>
        <n-button size="small" type="error" :loading="deleteAllLoading" @click="confirmDeleteAll">
          <template #icon><n-icon :component="TrashOutline" /></template>
          {{ t("mcpSkills.deleteAll") }}
        </n-button>
        <n-button size="small" @click="handleExport">
          <template #icon><n-icon :component="CloudDownloadOutline" /></template>
          {{ t("mcpSkills.exportAll") }}
        </n-button>
        <n-button size="small" :loading="importLoading" @click="triggerImport">
          <template #icon><n-icon :component="CloudUploadOutline" /></template>
          {{ t("mcpSkills.importAll") }}
        </n-button>
      </n-space>
    </div>
    <n-tabs :value="activeTab" type="line" animated @update:value="handleTabChange">
      <n-tab-pane name="services" :tab="t('mcpSkills.tabServices')">
        <div class="filter-row">
          <n-input
            v-model:value="serviceFilters.search"
            size="small"
            :placeholder="t('mcpSkills.searchPlaceholder')"
            clearable
            style="width: 200px"
          >
            <template #prefix><n-icon :component="Search" /></template>
          </n-input>
          <n-select
            v-model:value="serviceFilters.enabled"
            size="small"
            :options="enabledFilterOptions"
            style="width: 100px"
          />
          <n-select
            v-model:value="serviceFilters.category"
            size="small"
            :options="categoryFilterOptions"
            style="width: 120px"
          />
          <n-select
            v-model:value="serviceFilters.type"
            size="small"
            :options="typeFilterOptions"
            style="width: 120px"
          />
          <n-button size="small" @click="loadServices">
            <template #icon><n-icon :component="RefreshOutline" /></template>
          </n-button>
          <div style="flex: 1" />
          <n-button size="small" @click="openJsonImportModal">
            {{ t("mcpSkills.importMcpJson") }}
          </n-button>
          <n-button size="small" type="primary" @click="openTemplateModal">
            {{ t("mcpSkills.useTemplate") }}
          </n-button>
          <n-button size="small" type="primary" @click="openCreateService">
            <template #icon><n-icon :component="Add" /></template>
            {{ t("mcpSkills.createService") }}
          </n-button>
        </div>
        <n-data-table
          :columns="serviceColumns"
          :data="services"
          :loading="loading"
          :bordered="false"
          size="small"
          :row-key="(row: MCPServiceDTO) => row.id"
        />
        <!-- Expanded tools panel -->
        <template v-for="service in services" :key="`tools-${service.id}`">
          <div v-if="isToolsExpanded(service.id)" class="tools-expansion-panel">
            <div class="tools-expansion-header">
              <n-space align="center">
                <n-text strong>
                  {{ service.icon }} {{ service.display_name || service.name }}
                </n-text>
                <n-tag
                  v-if="expandedTools.get(service.id)?.data?.from_cache"
                  size="tiny"
                  type="info"
                >
                  {{ t("mcpSkills.toolsFromCache") }}
                </n-tag>
                <n-tag v-else size="tiny" type="success">
                  {{ t("mcpSkills.toolsFresh") }}
                </n-tag>
              </n-space>
              <n-space>
                <n-text
                  v-if="expandedTools.get(service.id)?.data?.cached_at"
                  depth="3"
                  style="font-size: 12px"
                >
                  {{ t("mcpSkills.toolsCachedAt") }}:
                  {{ formatDate(expandedTools.get(service.id)?.data?.cached_at) }}
                </n-text>
                <n-button
                  size="tiny"
                  :loading="refreshingTools.has(service.id)"
                  @click="refreshServiceTools(service.id)"
                >
                  <template #icon><n-icon :component="RefreshOutline" /></template>
                  {{ t("mcpSkills.refreshTools") }}
                </n-button>
                <n-button size="tiny" @click="toggleToolsExpansion(service)">
                  {{ t("mcpSkills.collapseTools") }}
                </n-button>
              </n-space>
            </div>
            <div v-if="expandedTools.get(service.id)?.loading" class="tools-loading">
              <n-text depth="3">{{ t("mcpSkills.loadingTools") }}</n-text>
            </div>
            <div v-else-if="expandedTools.get(service.id)?.error" class="tools-error">
              <n-text type="error">{{ expandedTools.get(service.id)?.error }}</n-text>
            </div>
            <div
              v-else-if="!expandedTools.get(service.id)?.data?.tools?.length"
              class="tools-empty"
            >
              <n-text depth="3">{{ t("mcpSkills.noTools") }}</n-text>
            </div>
            <div v-else class="tools-list">
              <div
                v-for="tool in expandedTools.get(service.id)?.data?.tools"
                :key="tool.name"
                class="tool-item-clickable"
              >
                <div
                  class="tool-name-row"
                  @click="toggleToolDescriptionExpansion(`service-${service.id}-${tool.name}`)"
                >
                  <n-text strong>{{ tool.name }}</n-text>
                  <span class="tool-expand-icon">
                    {{
                      isToolDescriptionExpanded(`service-${service.id}-${tool.name}`) ? "▼" : "▶"
                    }}
                  </span>
                </div>
                <div
                  v-if="isToolDescriptionExpanded(`service-${service.id}-${tool.name}`)"
                  class="tool-detail-panel"
                >
                  <div v-if="tool.description" class="tool-description">
                    <n-text depth="2">{{ tool.description }}</n-text>
                  </div>
                  <div
                    v-if="tool.input_schema && Object.keys(tool.input_schema).length > 0"
                    class="tool-schema"
                  >
                    <n-text depth="3" style="font-size: 11px">
                      {{ t("mcpSkills.toolInputSchema") }}:
                    </n-text>
                    <pre class="schema-code">{{ JSON.stringify(tool.input_schema, null, 2) }}</pre>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </template>
        <div class="pagination-row">
          <n-text depth="3">
            {{ t("mcpSkills.totalCount", { count: servicePagination.total }) }}
          </n-text>
          <n-pagination
            v-model:page="servicePagination.page"
            :page-count="servicePagination.totalPages"
            size="small"
            @update:page="handleServicePageChange"
          />
        </div>
      </n-tab-pane>
      <n-tab-pane name="groups" :tab="t('mcpSkills.tabGroups')">
        <div class="filter-row">
          <n-input
            v-model:value="groupFilters.search"
            size="small"
            :placeholder="t('mcpSkills.searchPlaceholder')"
            clearable
            style="width: 200px"
          >
            <template #prefix><n-icon :component="Search" /></template>
          </n-input>
          <n-select
            v-model:value="groupFilters.enabled"
            size="small"
            :options="enabledFilterOptions"
            style="width: 100px"
          />
          <n-button size="small" @click="loadGroups">
            <template #icon><n-icon :component="RefreshOutline" /></template>
          </n-button>
          <div style="flex: 1" />
          <n-button size="small" type="primary" @click="openCreateGroup">
            <template #icon><n-icon :component="Add" /></template>
            {{ t("mcpSkills.createGroup") }}
          </n-button>
        </div>
        <n-data-table
          :columns="groupColumns"
          :data="groups"
          :loading="loading"
          :bordered="false"
          size="small"
        />
        <!-- Expanded group tools panel -->
        <template v-for="group in groups" :key="`group-tools-${group.id}`">
          <div v-if="isGroupToolsExpanded(group.id)" class="tools-expansion-panel">
            <div class="tools-expansion-header">
              <n-space align="center">
                <n-text strong>
                  {{ group.display_name || group.name }}
                </n-text>
                <n-tag size="tiny" type="info">
                  {{
                    t("mcpSkills.serviceCount", {
                      count: expandedGroupTools.get(group.id)?.data?.service_count || 0,
                    })
                  }}
                </n-tag>
              </n-space>
              <n-button size="tiny" @click="toggleGroupToolsExpansion(group)">
                {{ t("mcpSkills.collapseTools") }}
              </n-button>
            </div>
            <div v-if="expandedGroupTools.get(group.id)?.loading" class="tools-loading">
              <n-text depth="3">{{ t("mcpSkills.loadingTools") }}</n-text>
            </div>
            <div v-else-if="expandedGroupTools.get(group.id)?.error" class="tools-error">
              <n-text type="error">{{ expandedGroupTools.get(group.id)?.error }}</n-text>
            </div>
            <div
              v-else-if="
                !expandedGroupTools.get(group.id)?.data?.services?.filter(s => s.enabled)?.length
              "
              class="tools-empty"
            >
              <n-text depth="3">{{ t("mcpSkills.noEnabledServices") }}</n-text>
            </div>
            <div v-else class="group-services-list">
              <template
                v-for="svc in expandedGroupTools.get(group.id)?.data?.services"
                :key="svc.service_id"
              >
                <!-- Only show enabled services -->
                <div v-if="svc.enabled" class="group-service-item">
                  <div class="group-service-header">
                    <n-space align="center">
                      <span>{{ svc.icon }}</span>
                      <n-text strong>{{ svc.display_name || svc.service_name }}</n-text>
                      <n-tag size="tiny" type="success">{{ svc.type }}</n-tag>
                      <n-tag size="tiny">
                        {{ t("mcpSkills.toolCount", { count: svc.tool_count }) }}
                      </n-tag>
                    </n-space>
                  </div>
                  <div v-if="svc.tools?.length" class="tools-list">
                    <div v-for="tool in svc.tools" :key="tool.name" class="tool-item-clickable">
                      <div
                        class="tool-name-row"
                        @click="
                          toggleToolDescriptionExpansion(
                            `group-${group.id}-${svc.service_name}-${tool.name}`
                          )
                        "
                      >
                        <n-text strong>{{ tool.name }}</n-text>
                        <n-tag
                          v-if="getToolDisplayName(group, tool.name).isAliased"
                          size="tiny"
                          type="info"
                          style="margin-left: 4px"
                        >
                          → {{ getToolDisplayName(group, tool.name).canonicalName }}
                        </n-tag>
                        <span class="tool-expand-icon">
                          {{
                            isToolDescriptionExpanded(
                              `group-${group.id}-${svc.service_name}-${tool.name}`
                            )
                              ? "▼"
                              : "▶"
                          }}
                        </span>
                      </div>
                      <div
                        v-if="
                          isToolDescriptionExpanded(
                            `group-${group.id}-${svc.service_name}-${tool.name}`
                          )
                        "
                        class="tool-detail-panel"
                      >
                        <div
                          v-if="getToolDisplayDescription(group, tool.name, tool.description || '')"
                          class="tool-description"
                        >
                          <n-text depth="2">
                            {{
                              getToolDisplayDescription(group, tool.name, tool.description || "")
                            }}
                          </n-text>
                        </div>
                        <div
                          v-if="tool.input_schema && Object.keys(tool.input_schema).length > 0"
                          class="tool-schema"
                        >
                          <n-text depth="3" style="font-size: 11px">
                            {{ t("mcpSkills.toolInputSchema") }}:
                          </n-text>
                          <pre class="schema-code">{{
                            JSON.stringify(tool.input_schema, null, 2)
                          }}</pre>
                        </div>
                      </div>
                    </div>
                  </div>
                  <div v-else class="tools-empty" style="padding: 8px">
                    <n-text depth="3" style="font-size: 12px">{{ t("mcpSkills.noTools") }}</n-text>
                  </div>
                </div>
              </template>
            </div>
          </div>
        </template>
        <div class="pagination-row">
          <n-text depth="3">
            {{ t("mcpSkills.totalCount", { count: groupPagination.total }) }}
          </n-text>
          <n-pagination
            v-model:page="groupPagination.page"
            :page-count="groupPagination.totalPages"
            size="small"
            @update:page="handleGroupPageChange"
          />
        </div>
      </n-tab-pane>
    </n-tabs>

    <!-- Service Modal -->
    <n-modal
      v-model:show="showServiceModal"
      preset="card"
      :title="editingService ? t('mcpSkills.editService') : t('mcpSkills.createService')"
      style="width: 560px"
    >
      <n-form label-placement="left" label-width="70" size="small" class="compact-form">
        <div class="form-row">
          <n-form-item :label="t('mcpSkills.name')" style="flex: 1">
            <n-input
              v-model:value="serviceForm.name"
              :placeholder="t('mcpSkills.namePlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.displayName')" style="flex: 1">
            <n-input
              v-model:value="serviceForm.display_name"
              :placeholder="t('mcpSkills.displayNamePlaceholder')"
            />
          </n-form-item>
        </div>
        <n-form-item :label="t('mcpSkills.description')">
          <n-input
            v-model:value="serviceForm.description"
            type="textarea"
            :rows="2"
            :placeholder="t('mcpSkills.descriptionPlaceholder')"
          />
        </n-form-item>
        <div class="form-row">
          <n-form-item :label="t('mcpSkills.type')" style="flex: 1">
            <n-select v-model:value="serviceForm.type" :options="typeOptions" />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.category')" style="flex: 1">
            <n-select v-model:value="serviceForm.category" :options="categoryOptions" />
          </n-form-item>
        </div>
        <div class="form-row">
          <n-form-item :label="t('mcpSkills.icon')" style="flex: 1">
            <n-space align="center" :size="8">
              <n-input v-model:value="serviceForm.icon" style="width: 50px" />
              <n-text depth="3">|</n-text>
              <n-text depth="3" style="font-size: 12px">{{ t("mcpSkills.sort") }}:</n-text>
              <n-input-number
                v-model:value="serviceForm.sort"
                :min="0"
                style="width: 60px"
                :show-button="false"
              />
            </n-space>
          </n-form-item>
          <n-form-item :label="t('mcpSkills.enabled')" style="flex: 1">
            <n-space align="center" :size="8">
              <n-switch v-model:value="serviceForm.enabled" size="small" />
              <n-text depth="3">|</n-text>
              <n-text depth="3" style="font-size: 12px">{{ t("mcpSkills.mcpEnabled") }}:</n-text>
              <n-switch v-model:value="serviceForm.mcp_enabled" size="small" />
              <n-tooltip trigger="hover">
                <template #trigger>
                  <n-icon size="14" style="color: var(--n-text-color-3); cursor: help">
                    <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor">
                      <path
                        d="M12 22C6.477 22 2 17.523 2 12S6.477 2 12 2s10 4.477 10 10-4.477 10-10 10zm-1-7v2h2v-2h-2zm0-8v6h2V7h-2z"
                      />
                    </svg>
                  </n-icon>
                </template>
                {{ t("mcpSkills.mcpEnabledTooltip") }}
              </n-tooltip>
            </n-space>
          </n-form-item>
        </div>
        <template
          v-if="
            serviceForm.type === 'stdio' ||
            serviceForm.type === 'sse' ||
            serviceForm.type === 'streamable_http'
          "
        >
          <n-form-item :label="t('mcpSkills.command')" v-if="serviceForm.type === 'stdio'">
            <n-input
              v-model:value="serviceForm.command"
              :placeholder="t('mcpSkills.commandPlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.args')" v-if="serviceForm.type === 'stdio'">
            <n-input
              v-model:value="argsInput"
              type="textarea"
              :rows="3"
              :placeholder="t('mcpSkills.argsPlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.cwd')" v-if="serviceForm.type === 'stdio'">
            <n-input v-model:value="cwdInput" :placeholder="t('mcpSkills.cwdPlaceholder')" />
          </n-form-item>
          <!-- Dynamic Environment Variables for stdio -->
          <n-form-item :label="t('mcpSkills.envVars')" v-if="serviceForm.type === 'stdio'">
            <div class="env-vars-container">
              <div v-for="envVar in envVars" :key="envVar.id" class="env-var-row">
                <n-button
                  quaternary
                  size="small"
                  :type="envVar.enabled ? 'success' : 'default'"
                  @click="toggleEnvVar(envVar.id)"
                  :title="
                    envVar.enabled ? t('mcpSkills.envVarEnabled') : t('mcpSkills.envVarDisabled')
                  "
                >
                  {{ envVar.enabled ? "✓" : "○" }}
                </n-button>
                <n-input
                  v-model:value="envVar.key"
                  :placeholder="t('mcpSkills.envKeyPlaceholder')"
                  :disabled="!envVar.enabled"
                  style="width: 140px"
                />
                <span class="env-var-eq">=</span>
                <n-input
                  v-model:value="envVar.value"
                  :placeholder="t('mcpSkills.envValuePlaceholder')"
                  :disabled="!envVar.enabled"
                  style="flex: 1"
                />
                <n-button quaternary size="small" type="error" @click="removeEnvVar(envVar.id)">
                  ✕
                </n-button>
              </div>
              <n-button size="small" dashed @click="addEnvVar" style="width: 100%">
                + {{ t("mcpSkills.addEnvVar") }}
              </n-button>
              <n-text v-if="envVars.length > 0" depth="3" style="font-size: 12px; margin-top: 4px">
                {{ t("mcpSkills.envVarsHint") }}
              </n-text>
            </div>
          </n-form-item>
          <n-form-item
            :label="t('mcpSkills.url')"
            v-if="serviceForm.type === 'sse' || serviceForm.type === 'streamable_http'"
          >
            <n-input
              v-model:value="serviceForm.api_endpoint"
              :placeholder="t('mcpSkills.urlPlaceholder')"
            />
          </n-form-item>
        </template>
        <template v-if="serviceForm.type === 'api_bridge'">
          <n-form-item :label="t('mcpSkills.apiEndpoint')">
            <n-input
              v-model:value="serviceForm.api_endpoint"
              :placeholder="t('mcpSkills.apiEndpointPlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.apiKeyName')">
            <n-input
              v-model:value="serviceForm.api_key_name"
              :placeholder="t('mcpSkills.apiKeyNamePlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.apiKeyValue')">
            <n-input
              v-model:value="apiKeyInput"
              type="password"
              show-password-on="click"
              :placeholder="
                editingService
                  ? t('mcpSkills.apiKeyValueEditHint')
                  : t('mcpSkills.apiKeyValuePlaceholder')
              "
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.apiKeyHeader')">
            <n-input
              v-model:value="serviceForm.api_key_header"
              :placeholder="t('mcpSkills.apiKeyHeaderPlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.apiKeyPrefix')">
            <n-input
              v-model:value="serviceForm.api_key_prefix"
              :placeholder="t('mcpSkills.apiKeyPrefixPlaceholder')"
            />
          </n-form-item>
        </template>
        <div class="form-row">
          <n-form-item :label="t('mcpSkills.rpdLimit')" style="width: 200px">
            <n-input-number
              v-model:value="serviceForm.rpd_limit"
              :min="0"
              style="width: 100px"
              :show-button="false"
            />
            <n-text depth="3" style="margin-left: 4px; font-size: 10px">
              (0={{ t("common.unlimited") }})
            </n-text>
          </n-form-item>
          <n-form-item :label="t('mcpSkills.accessToken')" style="flex: 1">
            <n-input
              v-model:value="serviceAccessTokenInput"
              type="password"
              show-password-on="click"
              :placeholder="t('mcpSkills.accessTokenPlaceholder')"
            >
              <template #suffix>
                <n-button text type="primary" size="small" @click="generateServiceAccessToken">
                  <template #icon>
                    <n-icon :component="Key" />
                  </template>
                  {{ t("mcpSkills.generate") }}
                </n-button>
              </template>
            </n-input>
          </n-form-item>
        </div>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showServiceModal = false">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" @click="submitService">{{ t("common.save") }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- Group Modal -->
    <n-modal
      v-model:show="showGroupModal"
      preset="card"
      :title="editingGroup ? t('mcpSkills.editGroup') : t('mcpSkills.createGroup')"
      style="width: 560px"
    >
      <n-form label-placement="left" label-width="70" size="small" class="compact-form">
        <div class="form-row">
          <n-form-item :label="t('mcpSkills.groupName')" style="flex: 1">
            <n-input
              v-model:value="groupForm.name"
              :placeholder="t('mcpSkills.groupNamePlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.groupDisplayName')" style="flex: 1">
            <n-input
              v-model:value="groupForm.display_name"
              :placeholder="t('mcpSkills.displayNamePlaceholder')"
            />
          </n-form-item>
        </div>
        <n-form-item :label="t('mcpSkills.groupDescription')">
          <n-input
            v-model:value="groupForm.description"
            type="textarea"
            :rows="2"
            :placeholder="t('mcpSkills.descriptionPlaceholder')"
          />
        </n-form-item>
        <n-form-item :label="t('mcpSkills.services')">
          <n-select
            v-model:value="groupForm.service_ids"
            multiple
            :options="serviceOptions"
            :placeholder="t('mcpSkills.selectServices')"
          />
        </n-form-item>
        <!-- Service Weights Configuration -->
        <n-form-item v-if="groupForm.service_ids.length > 0" :label="t('mcpSkills.serviceWeights')">
          <div style="width: 100%">
            <n-space vertical :size="4">
              <div
                v-for="serviceId in groupForm.service_ids"
                :key="serviceId"
                style="display: flex; align-items: center; gap: 8px"
                :style="{ opacity: isServiceEnabled(serviceId) ? 1 : 0.5 }"
              >
                <span
                  style="
                    min-width: 120px;
                    font-size: 12px;
                    overflow: hidden;
                    text-overflow: ellipsis;
                    white-space: nowrap;
                  "
                >
                  {{ serviceOptions.find(s => s.value === serviceId)?.label || serviceId }}
                  <n-tag
                    v-if="!isServiceEnabled(serviceId)"
                    size="tiny"
                    type="warning"
                    style="margin-left: 4px"
                  >
                    {{ t("mcpSkills.disabled") }}
                  </n-tag>
                </span>
                <n-input-number
                  :value="getServiceWeight(serviceId)"
                  :min="1"
                  :max="1000"
                  size="small"
                  style="width: 100px"
                  :show-button="false"
                  @update:value="(v: number | null) => setServiceWeight(serviceId, v ?? 100)"
                />
              </div>
            </n-space>
            <n-text depth="3" style="font-size: 11px; margin-top: 4px; display: block">
              {{ t("mcpSkills.weightHint") }}
            </n-text>
          </div>
        </n-form-item>
        <div class="form-row">
          <n-form-item :label="t('mcpSkills.enabled')" style="flex: 1">
            <n-space align="center" :size="8">
              <n-switch v-model:value="groupForm.enabled" size="small" />
              <n-text depth="3">|</n-text>
              <n-text depth="3" style="font-size: 12px">
                {{ t("mcpSkills.aggregationEnabled") }}:
              </n-text>
              <n-switch v-model:value="groupForm.aggregation_enabled" size="small" />
            </n-space>
          </n-form-item>
        </div>
        <!-- Tool Aliases Configuration -->
        <n-form-item v-if="groupForm.aggregation_enabled" :label="t('mcpSkills.toolAliases')">
          <div style="width: 100%">
            <n-space vertical :size="8">
              <div
                v-for="entry in toolAliasEntries"
                :key="entry.id"
                style="border: 1px solid var(--n-border-color); border-radius: 6px; padding: 8px"
              >
                <!-- Main row: canonical name, aliases, expand/delete buttons -->
                <div style="display: flex; align-items: center; gap: 6px">
                  <n-input
                    v-model:value="entry.canonicalName"
                    size="small"
                    style="width: 120px"
                    :placeholder="t('mcpSkills.canonicalName')"
                  />
                  <span style="color: var(--n-text-color-3); font-size: 12px">→</span>
                  <n-input
                    v-model:value="entry.aliases"
                    size="small"
                    style="flex: 1"
                    :placeholder="t('mcpSkills.aliasesPlaceholder')"
                  />
                  <n-button
                    quaternary
                    size="small"
                    :type="entry.expanded ? 'primary' : 'default'"
                    @click="toggleAliasExpanded(entry)"
                    :title="t('mcpSkills.viewToolDescriptions')"
                  >
                    {{ entry.expanded ? "▼" : "▶" }}
                  </n-button>
                  <n-button
                    quaternary
                    size="small"
                    type="error"
                    @click="removeToolAliasEntry(entry.id)"
                  >
                    ✕
                  </n-button>
                </div>
                <!-- Expanded section: show tool descriptions and unified description input -->
                <div
                  v-if="entry.expanded"
                  style="
                    margin-top: 8px;
                    padding-top: 8px;
                    border-top: 1px dashed var(--n-border-color);
                  "
                >
                  <!-- Original tool descriptions from services -->
                  <div style="margin-bottom: 8px">
                    <n-text depth="3" style="font-size: 11px; display: block; margin-bottom: 4px">
                      {{ t("mcpSkills.originalDescriptions") }}:
                    </n-text>
                    <div v-if="getToolDescriptionsForAlias(entry).length > 0">
                      <div
                        v-for="(desc, idx) in getToolDescriptionsForAlias(entry)"
                        :key="idx"
                        style="
                          font-size: 11px;
                          padding: 4px 8px;
                          margin-bottom: 2px;
                          background: var(--n-color-embedded);
                          border-radius: 4px;
                        "
                      >
                        <n-text type="info" style="font-weight: 500">{{ desc.serviceName }}</n-text>
                        <span style="color: var(--n-text-color-3)">/ {{ desc.toolName }}:</span>
                        <br />
                        <n-text depth="2" style="font-size: 11px; white-space: pre-wrap">
                          {{ desc.description || "(no description)" }}
                        </n-text>
                      </div>
                    </div>
                    <n-text v-else depth="3" style="font-size: 11px; font-style: italic">
                      {{ t("mcpSkills.noMatchingTools") }}
                    </n-text>
                  </div>
                  <!-- Unified description input -->
                  <div>
                    <n-text depth="3" style="font-size: 11px; display: block; margin-bottom: 4px">
                      {{ t("mcpSkills.unifiedDescription") }}:
                    </n-text>
                    <n-input
                      v-model:value="entry.description"
                      type="textarea"
                      size="small"
                      :rows="2"
                      :placeholder="t('mcpSkills.unifiedDescriptionPlaceholder')"
                    />
                  </div>
                </div>
              </div>
            </n-space>
            <n-button
              size="small"
              dashed
              style="width: 100%; margin-top: 6px"
              @click="addToolAliasEntry"
            >
              + {{ t("mcpSkills.addToolAlias") }}
            </n-button>
            <n-text
              depth="3"
              style="font-size: 11px; margin-top: 6px; display: block; line-height: 1.4"
            >
              {{ t("mcpSkills.toolAliasesHint") }}
            </n-text>
          </div>
        </n-form-item>
        <n-form-item :label="t('mcpSkills.accessToken')">
          <n-input
            v-model:value="groupForm.access_token"
            type="password"
            show-password-on="click"
            :placeholder="t('mcpSkills.accessTokenPlaceholder')"
          >
            <template #suffix>
              <n-button text type="primary" size="small" @click="generateGroupAccessToken">
                <template #icon>
                  <n-icon :component="Key" />
                </template>
                {{ t("mcpSkills.generate") }}
              </n-button>
            </template>
          </n-input>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showGroupModal = false">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" @click="submitGroup">{{ t("common.save") }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- Template Modal -->
    <n-modal
      v-model:show="showTemplateModal"
      preset="card"
      :title="t('mcpSkills.createFromTemplate')"
      style="width: 500px"
    >
      <n-form label-placement="left" label-width="100">
        <n-form-item :label="t('mcpSkills.templates')">
          <n-select
            v-model:value="selectedTemplateId"
            :options="
              templates.map(tpl => ({ label: `${tpl.icon} ${tpl.display_name}`, value: tpl.id }))
            "
          />
        </n-form-item>
        <n-form-item v-if="selectedTemplate" :label="t('mcpSkills.description')">
          <n-text depth="3">{{ selectedTemplate.description }}</n-text>
        </n-form-item>
        <n-form-item v-if="selectedTemplate" :label="t('mcpSkills.apiEndpoint')">
          <n-input
            v-model:value="templateCustomEndpoint"
            :placeholder="selectedTemplate.api_endpoint"
          />
          <template #feedback>
            <n-text depth="3" style="font-size: 12px">
              {{ t("mcpSkills.customEndpointHint") }}
            </n-text>
          </template>
        </n-form-item>
        <n-form-item v-if="selectedTemplate" :label="t('mcpSkills.apiKeyValue')">
          <n-input
            v-model:value="templateApiKey"
            type="password"
            show-password-on="click"
            :placeholder="t('mcpSkills.apiKeyValuePlaceholder')"
          />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showTemplateModal = false">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" :disabled="!selectedTemplate" @click="createFromTemplate">
            {{ t("common.create") }}
          </n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- Endpoint Info Modal -->
    <n-modal
      v-model:show="showEndpointModal"
      preset="card"
      :title="`${t('mcpSkills.endpointInfo')} - ${endpointGroupName}`"
      style="width: 650px"
    >
      <div v-if="endpointInfo" class="endpoint-info">
        <div class="endpoint-item">
          <n-text strong>{{ t("mcpSkills.aggregationEndpoint") }}</n-text>
          <div class="endpoint-value">
            <code>{{ endpointInfo.aggregation_endpoint }}</code>
            <n-button
              size="tiny"
              quaternary
              @click="copyToClipboard(endpointInfo.aggregation_endpoint, 'mcpSkills.configCopied')"
            >
              <template #icon><n-icon :component="CopyOutline" /></template>
            </n-button>
          </div>
        </div>
        <div class="endpoint-item">
          <n-text strong>{{ t("mcpSkills.skillExportEndpoint") }}</n-text>
          <div class="endpoint-value">
            <code>{{ endpointInfo.skill_export_url }}</code>
            <n-button
              size="tiny"
              quaternary
              @click="copyToClipboard(endpointInfo.skill_export_url, 'mcpSkills.configCopied')"
            >
              <template #icon><n-icon :component="CopyOutline" /></template>
            </n-button>
          </div>
        </div>
        <div class="endpoint-item">
          <n-text strong>{{ t("mcpSkills.mcpConfig") }}</n-text>
          <div class="endpoint-value config-block">
            <pre>{{ endpointInfo.mcp_config_json }}</pre>
            <n-button
              size="tiny"
              quaternary
              @click="copyToClipboard(endpointInfo.mcp_config_json, 'mcpSkills.configCopied')"
            >
              <template #icon><n-icon :component="CopyOutline" /></template>
            </n-button>
          </div>
        </div>
      </div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showEndpointModal = false">{{ t("common.close") }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- Service Endpoint Info Modal -->
    <n-modal
      v-model:show="showServiceEndpointModal"
      preset="card"
      :title="`${t('mcpSkills.serviceEndpointInfo')} - ${serviceEndpointName}`"
      style="width: 650px"
    >
      <div v-if="serviceEndpointInfo" class="endpoint-info">
        <div v-if="serviceEndpointInfo.mcp_endpoint" class="endpoint-item">
          <n-text strong>{{ t("mcpSkills.mcpEndpoint") }}</n-text>
          <div class="endpoint-value">
            <code>{{ serviceEndpointInfo.mcp_endpoint }}</code>
            <n-button
              size="tiny"
              quaternary
              @click="copyToClipboard(serviceEndpointInfo.mcp_endpoint, 'mcpSkills.configCopied')"
            >
              <template #icon><n-icon :component="CopyOutline" /></template>
            </n-button>
          </div>
        </div>
        <div v-else class="endpoint-item">
          <n-text depth="3">{{ t("mcpSkills.noMcpEndpoint") }}</n-text>
        </div>
        <div v-if="serviceEndpointInfo.api_endpoint" class="endpoint-item">
          <n-text strong>{{ t("mcpSkills.apiEndpoint") }}</n-text>
          <div class="endpoint-value">
            <code>{{ serviceEndpointInfo.api_endpoint }}</code>
            <n-button
              size="tiny"
              quaternary
              @click="copyToClipboard(serviceEndpointInfo.api_endpoint!, 'mcpSkills.configCopied')"
            >
              <template #icon><n-icon :component="CopyOutline" /></template>
            </n-button>
          </div>
        </div>
        <div v-if="serviceEndpointInfo.mcp_config_json" class="endpoint-item">
          <n-text strong>{{ t("mcpSkills.mcpConfig") }}</n-text>
          <div class="endpoint-value config-block">
            <pre>{{ serviceEndpointInfo.mcp_config_json }}</pre>
            <n-button
              size="tiny"
              quaternary
              @click="
                copyToClipboard(serviceEndpointInfo.mcp_config_json, 'mcpSkills.configCopied')
              "
            >
              <template #icon><n-icon :component="CopyOutline" /></template>
            </n-button>
          </div>
        </div>
      </div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showServiceEndpointModal = false">{{ t("common.close") }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- JSON Import Modal -->
    <n-modal
      v-model:show="showJsonImportModal"
      preset="card"
      :title="t('mcpSkills.importMcpJson')"
      style="width: 700px"
    >
      <div style="display: flex; flex-direction: column; gap: 12px">
        <!-- File upload section -->
        <div style="display: flex; align-items: center; gap: 8px">
          <n-text strong>{{ t("mcpSkills.jsonImportFromFile") }}:</n-text>
          <input
            ref="jsonFileInputRef"
            type="file"
            accept=".json"
            style="display: none"
            @change="handleJsonFileSelect"
          />
          <n-button size="small" @click="triggerJsonFileInput">
            {{ t("mcpSkills.selectFile") }}
          </n-button>
          <n-text v-if="selectedJsonFileName" depth="3">{{ selectedJsonFileName }}</n-text>
        </div>
        <n-divider style="margin: 4px 0">{{ t("common.or") }}</n-divider>
        <!-- Text input section -->
        <n-text strong>{{ t("mcpSkills.jsonImportLabel") }}</n-text>
        <n-input
          v-model:value="jsonImportText"
          type="textarea"
          :rows="10"
          :placeholder="jsonImportPlaceholderText"
          style="font-family: monospace; font-size: 12px"
        />
        <n-text depth="3" style="font-size: 12px">
          {{ t("mcpSkills.jsonImportHint") }}
        </n-text>
      </div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showJsonImportModal = false">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" :loading="jsonImportLoading" @click="importFromJson">
            {{ t("mcpSkills.importMcpJsonBtn") }}
          </n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.mcp-skills-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.panel-header {
  display: flex;
  justify-content: flex-end;
  margin-bottom: 8px;
}
.filter-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
.pagination-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 12px;
}
/* Compact form styles */
.compact-form :deep(.n-form-item) {
  margin-bottom: 6px;
}
.compact-form :deep(.n-form-item-label) {
  font-size: 12px;
  padding-right: 4px;
}
.compact-form :deep(.n-form-item-feedback-wrapper) {
  min-height: 0;
}
.form-row {
  display: flex;
  gap: 8px;
  align-items: flex-start;
}
.form-row > * {
  margin-bottom: 6px;
}
.endpoint-info {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.endpoint-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.endpoint-value {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}
.endpoint-value code {
  flex: 1;
  padding: 8px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  word-break: break-all;
  font-size: 12px;
}
.config-block pre {
  flex: 1;
  padding: 8px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
  margin: 0;
  max-height: 200px;
  overflow-y: auto;
}
.env-vars-container {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}
.env-var-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.env-var-eq {
  color: var(--n-text-color-3);
  font-weight: 500;
}

/* Tool expansion panel styles */
.tools-expansion-panel {
  margin: 8px 0;
  padding: 12px;
  background: var(--n-color-embedded);
  border-radius: 6px;
  border: 1px solid var(--n-border-color);
}
.tools-expansion-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--n-border-color);
}
.tools-loading,
.tools-error,
.tools-empty {
  padding: 16px;
  text-align: center;
}
.tools-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 400px;
  overflow-y: auto;
}
.tool-item-clickable {
  background: var(--n-color);
  border-radius: 4px;
  border: 1px solid var(--n-border-color);
  overflow: hidden;
}
.tool-name-row {
  display: flex;
  align-items: center;
  padding: 8px 10px;
  cursor: pointer;
  user-select: none;
  gap: 8px;
}
.tool-name-row:hover {
  background: var(--n-color-hover);
}
.tool-expand-icon {
  margin-left: auto;
  color: var(--n-text-color-3);
  font-size: 10px;
}
.tool-detail-panel {
  padding: 8px 10px;
  border-top: 1px dashed var(--n-border-color);
  background: var(--n-color-embedded);
}
.tool-header {
  margin-bottom: 4px;
}
.tool-description {
  margin-bottom: 6px;
  font-size: 13px;
}
.tool-schema {
  margin-top: 6px;
}
.schema-code {
  margin: 4px 0 0 0;
  padding: 8px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  font-size: 11px;
  overflow-x: auto;
  max-height: 150px;
  white-space: pre-wrap;
  word-break: break-all;
}

/* Group services list styles */
.group-services-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-height: 500px;
  overflow-y: auto;
}
.group-service-item {
  padding: 10px;
  background: var(--n-color);
  border-radius: 4px;
  border: 1px solid var(--n-border-color);
}
.group-service-header {
  margin-bottom: 8px;
  padding-bottom: 6px;
  border-bottom: 1px dashed var(--n-border-color);
}
.group-service-tools {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-left: 8px;
}
</style>

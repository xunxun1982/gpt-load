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
const selectedTemplate = computed(
  () => templates.value.find(t => t.id === selectedTemplateId.value) || null
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

// JSON import state
const showJsonImportModal = ref(false);
const jsonImportText = ref("");
const jsonImportLoading = ref(false);

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

function addEnvVar() {
  envVars.value.push({
    id: `env-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
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
function loadEnvVarsFromRecord(record: Record<string, string> | undefined) {
  envVars.value = [];
  if (record) {
    for (const [key, value] of Object.entries(record)) {
      envVars.value.push({
        id: `env-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        key,
        value,
        enabled: true,
      });
    }
  }
}

// Group form
const groupForm = reactive<CreateGroupRequest>({
  name: "",
  display_name: "",
  description: "",
  service_ids: [],
  enabled: true,
  aggregation_enabled: false,
  access_token: "",
});

// Options
const typeOptions = computed<SelectOption[]>(() => [
  { label: t("mcpSkills.typeStdio"), value: "stdio" },
  { label: t("mcpSkills.typeSse"), value: "sse" },
  { label: t("mcpSkills.typeStreamableHttp"), value: "streamable_http" },
  { label: t("mcpSkills.typeApiBridge"), value: "api_bridge" },
]);

const categoryOptions = computed<SelectOption[]>(() => [
  { label: t("mcpSkills.categorySearch"), value: "search" },
  { label: t("mcpSkills.categoryCode"), value: "code" },
  { label: t("mcpSkills.categoryData"), value: "data" },
  { label: t("mcpSkills.categoryUtility"), value: "utility" },
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

const serviceOptions = computed<SelectOption[]>(() =>
  services.value.map(s => ({
    label: `${s.icon} ${s.display_name || s.name}`,
    value: s.id,
  }))
);

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
      await mcpSkillsApi.updateService(editingService.value.id, payload);
      message.success(t("mcpSkills.serviceUpdated"));
    } else {
      await mcpSkillsApi.createService({
        ...serviceForm,
        api_key_value: apiKeyInput.value,
        default_envs: defaultEnvs,
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
    enabled: true,
    aggregation_enabled: false,
    access_token: "",
  });
}

function openCreateGroup() {
  editingGroup.value = null;
  resetGroupForm();
  loadAllServices();
  showGroupModal.value = true;
}

function openEditGroup(group: MCPServiceGroupDTO) {
  editingGroup.value = group;
  Object.assign(groupForm, {
    name: group.name,
    display_name: group.display_name,
    description: group.description,
    service_ids: group.service_ids || [],
    enabled: group.enabled,
    aggregation_enabled: group.aggregation_enabled,
    access_token: "",
  });
  loadAllServices();
  showGroupModal.value = true;
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
    message.error(t("keys.copyFailed"));
  }
}

async function exportGroupAsSkill(group: MCPServiceGroupDTO) {
  try {
    const blob = await mcpSkillsApi.exportGroupAsSkill(group.id);
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `skill-${group.name}-${Date.now()}.zip`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    message.success(t("mcpSkills.skillExported"));
  } catch (_) {
    /* handled by centralized error handler */
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
  showJsonImportModal.value = true;
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
      // Show API key name for api_bridge type
      if (row.type === "api_bridge" && row.api_key_name) {
        items.push(
          h(NTag, { size: "tiny", bordered: false, type: "info" }, () => row.api_key_name)
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
    render: row => h(NText, null, () => t("mcpSkills.serviceCount", { count: row.service_count })),
  },
  {
    title: t("mcpSkills.tools"),
    key: "total_tool_count",
    width: 80,
    align: "center",
    render: row => h(NText, null, () => t("mcpSkills.toolCount", { count: row.total_tool_count })),
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
                class="tool-item"
              >
                <div class="tool-header">
                  <n-text strong>{{ tool.name }}</n-text>
                </div>
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
      <n-form label-placement="left" label-width="90" size="small" class="compact-form">
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
          <n-form-item :label="t('mcpSkills.icon')" style="width: 120px">
            <n-input
              v-model:value="serviceForm.icon"
              :placeholder="t('mcpSkills.iconPlaceholder')"
            />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.sort')" style="width: 120px">
            <n-input-number v-model:value="serviceForm.sort" :min="0" style="width: 100%" />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.enabled')" style="width: 100px">
            <n-switch v-model:value="serviceForm.enabled" />
          </n-form-item>
          <n-form-item :label="t('mcpSkills.mcpEnabled')" style="flex: 1">
            <n-switch v-model:value="serviceForm.mcp_enabled" />
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
        <n-form-item :label="t('mcpSkills.rpdLimit')">
          <n-input-number v-model:value="serviceForm.rpd_limit" :min="0" style="width: 150px" />
          <n-text depth="3" style="margin-left: 8px; font-size: 11px">
            {{ t("mcpSkills.mcpEnabledTooltip") }}
          </n-text>
        </n-form-item>
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
      style="width: 600px"
    >
      <n-form label-placement="left" label-width="100">
        <n-form-item :label="t('mcpSkills.groupName')">
          <n-input
            v-model:value="groupForm.name"
            :placeholder="t('mcpSkills.groupNamePlaceholder')"
          />
        </n-form-item>
        <n-form-item :label="t('mcpSkills.groupDisplayName')">
          <n-input
            v-model:value="groupForm.display_name"
            :placeholder="t('mcpSkills.displayNamePlaceholder')"
          />
        </n-form-item>
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
        <n-form-item :label="t('mcpSkills.enabled')">
          <n-switch v-model:value="groupForm.enabled" />
        </n-form-item>
        <n-form-item :label="t('mcpSkills.aggregationEnabled')">
          <n-switch v-model:value="groupForm.aggregation_enabled" />
        </n-form-item>
        <n-form-item v-if="editingGroup" :label="t('mcpSkills.accessToken')">
          <n-input
            v-model:value="groupForm.access_token"
            type="password"
            show-password-on="click"
            :placeholder="t('mcpSkills.accessTokenPlaceholder')"
          />
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
      <div style="display: flex; flex-direction: column; gap: 8px">
        <n-text strong>{{ t("mcpSkills.jsonImportLabel") }}</n-text>
        <n-input
          v-model:value="jsonImportText"
          type="textarea"
          :rows="12"
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
  margin-bottom: 8px;
}
.compact-form :deep(.n-form-item-label) {
  font-size: 13px;
}
.form-row {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.form-row > * {
  margin-bottom: 8px;
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
  gap: 8px;
  max-height: 400px;
  overflow-y: auto;
}
.tool-item {
  padding: 10px;
  background: var(--n-color);
  border-radius: 4px;
  border: 1px solid var(--n-border-color);
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
</style>

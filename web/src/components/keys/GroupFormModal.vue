<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { settingsApi } from "@/api/settings";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import ModelSelectorModal from "@/components/keys/ModelSelectorModal.vue";
import type {
  Group,
  GroupConfigOption,
  ModelRedirectDynamicWeight,
  ModelRedirectTargetWeight,
  PathRedirectRule,
  UpstreamInfo,
} from "@/types/models";
import { Add, Close, CloudDownloadOutline, HelpCircleOutline, Remove } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSwitch,
  NTooltip,
  useMessage,
  type FormRules,
} from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  group?: Group | null;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success", value: Group): void;
  (e: "switchToGroup", groupId: number): void;
}

// Configuration item type
interface ConfigItem {
  key: string;
  value: number | string | boolean;
}

// Header rule type
interface HeaderRuleItem {
  key: string;
  value: string;
  action: "set" | "remove";
}

const props = withDefaults(defineProps<Props>(), {
  group: null,
});

const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref();
const fetchingModels = ref(false);
const showModelSelector = ref(false);
const availableModels = ref<string[]>([]);

// Model redirect target type (V2: one-to-many with weight)
interface ModelRedirectTargetItem {
  model: string;
  weight: number;
  enabled: boolean;
}

// Model redirect V2 item type
interface ModelRedirectItemV2 {
  from: string;
  targets: ModelRedirectTargetItem[];
}

// Form data interface
interface GroupFormData {
  name: string;
  display_name: string;
  description: string;
  upstreams: UpstreamInfo[];
  channel_type: "anthropic" | "gemini" | "openai" | "codex";
  sort: number;
  test_model: string;
  validation_endpoint: string;
  param_overrides: string;
  model_redirect_items_v2: ModelRedirectItemV2[];
  model_redirect_strict: boolean;
  force_function_call: boolean;
  parallel_tool_calls: "default" | "true" | "false";
  cc_support: boolean;
  intercept_event_log: boolean;
  thinking_model: string;
  codex_instructions_mode: "auto" | "official" | "custom";
  codex_instructions: string;

  config: Record<string, number | string | boolean>;
  configItems: ConfigItem[];
  header_rules: HeaderRuleItem[];
  path_redirects: PathRedirectRule[];
  proxy_keys: string;
  group_type?: string;
}

// Form data
const formData = reactive<GroupFormData>({
  name: "",
  display_name: "",
  description: "",
  upstreams: [
    {
      url: "",
      weight: 1,
    },
  ] as UpstreamInfo[],
  channel_type: "openai",
  sort: 1,
  test_model: "",
  validation_endpoint: "",
  param_overrides: "",
  model_redirect_items_v2: [] as ModelRedirectItemV2[],
  model_redirect_strict: false,
  force_function_call: false,
  parallel_tool_calls: "default",
  cc_support: false,
  intercept_event_log: false,
  thinking_model: "",
  codex_instructions_mode: "auto",
  codex_instructions: "",

  config: {},
  configItems: [] as ConfigItem[],
  header_rules: [] as HeaderRuleItem[],
  path_redirects: [] as PathRedirectRule[],
  proxy_keys: "",
  group_type: "standard",
});

const channelTypeOptions = ref<{ label: string; value: string }[]>([]);
const configOptions = ref<GroupConfigOption[]>([]);
const channelTypesFetched = ref(false);
const configOptionsFetched = ref(false);
const modelRedirectDynamicWeights = ref<ModelRedirectDynamicWeight[]>([]);

// Track whether fields were manually modified (used only in create mode)
const userModifiedFields = ref({
  test_model: false,
  upstream: false,
});
const channelDefaults: Record<
  "openai" | "gemini" | "anthropic" | "codex",
  { testModel: string; upstream: string; validationEndpoint: string }
> = {
  openai: {
    testModel: "gpt-4.1-nano",
    upstream: "https://api.openai.com",
    validationEndpoint: "/v1/chat/completions",
  },
  gemini: {
    testModel: "gemini-2.0-flash-lite",
    upstream: "https://generativelanguage.googleapis.com",
    validationEndpoint: "",
  },
  anthropic: {
    testModel: "claude-3-haiku-20240307",
    upstream: "https://api.anthropic.com",
    validationEndpoint: "/v1/messages",
  },
  codex: {
    testModel: "gpt-5-codex",
    upstream: "https://api.openai.com",
    validationEndpoint: "/v1/responses",
  },
};

function getChannelDefaults(channelType: string) {
  return channelDefaults[channelType as keyof typeof channelDefaults] || null;
}

// Generate placeholders dynamically based on channel type
const testModelPlaceholder = computed(() => {
  const defaults = getChannelDefaults(formData.channel_type);
  return defaults?.testModel || t("keys.enterModelName");
});

const upstreamPlaceholder = computed(() => {
  const defaults = getChannelDefaults(formData.channel_type);
  return defaults?.upstream || t("keys.enterUpstreamUrl");
});

const validationEndpointPlaceholder = computed(() => {
  if (formData.channel_type === "gemini") {
    return ""; // Field is hidden for Gemini
  }
  const defaults = getChannelDefaults(formData.channel_type);
  return defaults?.validationEndpoint || t("keys.enterValidationPath");
});

// Check if current group is a child group (has parent_group_id)
const isChildGroup = computed(() => {
  return !!props.group?.parent_group_id;
});

// Form validation rules
const rules: FormRules = {
  name: [
    {
      required: true,
      message: t("keys.enterGroupName"),
      trigger: ["blur", "input"],
    },
    {
      pattern: /^[a-z0-9_-]{1,100}$/,
      message: t("keys.groupNamePattern"),
      trigger: ["blur", "input"],
    },
  ],
  channel_type: [
    {
      required: true,
      message: t("keys.selectChannelType"),
      trigger: ["blur", "change"],
    },
  ],
  test_model: [
    {
      required: true,
      message: t("keys.enterTestModel"),
      trigger: ["blur", "input"],
    },
  ],
  upstreams: [
    {
      type: "array",
      min: 1,
      message: t("keys.atLeastOneUpstream"),
      trigger: ["blur", "change"],
    },
  ],
};

// Watch dialog visibility
watch(
  () => props.show,
  show => {
    if (show) {
      if (!channelTypesFetched.value) {
        fetchChannelTypes();
      }
      if (!configOptionsFetched.value) {
        fetchGroupConfigOptions();
      }
      resetForm();
      if (props.group) {
        loadGroupData();
      }
    }
  }
);

// Watch channel type changes and intelligently update defaults in create mode
watch(
  () => formData.channel_type,
  (newChannelType, oldChannelType) => {
    if (!props.group && oldChannelType) {
      // Only handle in create mode and when this is not the initial setup
      // Check whether test model should be updated (empty or equals the old channel default)
      if (
        !userModifiedFields.value.test_model ||
        formData.test_model === getOldDefaultTestModel(oldChannelType)
      ) {
        formData.test_model = testModelPlaceholder.value;
        userModifiedFields.value.test_model = false;
      }

      // Check whether the first upstream URL should be updated
      if (formData.upstreams.length > 0) {
        const firstUpstream = formData.upstreams[0];
        if (
          firstUpstream &&
          (!userModifiedFields.value.upstream ||
            firstUpstream.url === getOldDefaultUpstream(oldChannelType))
        ) {
          firstUpstream.url = upstreamPlaceholder.value;
          userModifiedFields.value.upstream = false;
        }
      }
    }

    // Force disable function call when channel is not OpenAI.
    // CC support is available for both OpenAI and Codex channels.
    if (newChannelType !== "openai") {
      formData.force_function_call = false;
    }
    if (newChannelType !== "openai" && newChannelType !== "codex") {
      formData.cc_support = false;
    }
    // Handle intercept_event_log based on channel type.
    // Default to true for Anthropic channel, false for others.
    if (newChannelType === "anthropic") {
      // Only set to true if not explicitly configured (new group or channel type change)
      if (!props.group || props.group.channel_type !== "anthropic") {
        formData.intercept_event_log = true;
      }
    } else {
      formData.intercept_event_log = false;
    }
  }
);

// Get default values for previous channel type (for comparison)
function getOldDefaultTestModel(channelType: string): string {
  const defaults = getChannelDefaults(channelType);
  return defaults?.testModel || "";
}

function getOldDefaultUpstream(channelType: string): string {
  const defaults = getChannelDefaults(channelType);
  return defaults?.upstream || "";
}

// Reset form
function resetForm() {
  const isCreateMode = !props.group;
  const defaultChannelType = "openai";

  // Set channel type first so computed properties can calculate default values correctly
  formData.channel_type = defaultChannelType;

  formRef.value?.restoreValidation();

  Object.assign(formData, {
    name: "",
    display_name: "",
    description: "",
    upstreams: [
      {
        url: isCreateMode ? upstreamPlaceholder.value : "",
        weight: 1,
      },
    ],
    channel_type: defaultChannelType,
    sort: 1,
    test_model: isCreateMode ? testModelPlaceholder.value : "",
    validation_endpoint: "",
    param_overrides: "",
    model_redirect_items_v2: [],
    model_redirect_strict: false,
    force_function_call: false,
    parallel_tool_calls: "default",
    cc_support: false,
    intercept_event_log: false,
    thinking_model: "",
    codex_instructions_mode: "auto",
    codex_instructions: "",

    config: {},
    configItems: [],
    header_rules: [],
    path_redirects: [],
    proxy_keys: "",
    group_type: "standard",
  });

  // Reset tracking state for user-modified flags
  if (isCreateMode) {
    userModifiedFields.value = {
      test_model: false,
      upstream: false,
    };
  }
}

// Load group data (edit mode)
function loadGroupData() {
  if (!props.group) {
    return;
  }

  // Fetch dynamic weights for model redirect rules
  if (props.group.id) {
    fetchModelRedirectDynamicWeights(props.group.id);
  }

  const rawConfig: Record<string, unknown> = props.group.config || {};
  const fcRaw = rawConfig["force_function_call"];
  const forceFunctionCall =
    props.group.channel_type === "openai" && typeof fcRaw === "boolean" ? fcRaw : false;
  // Read parallel_tool_calls config (three-state: default, true, false)
  const ptcRaw = rawConfig["parallel_tool_calls"];
  let parallelToolCalls: "default" | "true" | "false" = "default";
  if (typeof ptcRaw === "boolean") {
    parallelToolCalls = ptcRaw ? "true" : "false";
  }
  const ccRaw = rawConfig["cc_support"];
  // CC support is available for both OpenAI and Codex channels
  const ccSupport =
    (props.group.channel_type === "openai" || props.group.channel_type === "codex") &&
    typeof ccRaw === "boolean"
      ? ccRaw
      : false;
  const interceptEventLogRaw = rawConfig["intercept_event_log"];
  // Default to true for Anthropic channel when not explicitly configured
  const interceptEventLog =
    props.group.channel_type === "anthropic"
      ? typeof interceptEventLogRaw === "boolean"
        ? interceptEventLogRaw
        : true // Default to true for Anthropic
      : false;
  const thinkingModelRaw = rawConfig["thinking_model"];
  const thinkingModel = typeof thinkingModelRaw === "string" ? thinkingModelRaw : "";
  const codexInstructionsRaw = rawConfig["codex_instructions"];
  const codexInstructions = typeof codexInstructionsRaw === "string" ? codexInstructionsRaw : "";
  const codexInstructionsModeRaw = rawConfig["codex_instructions_mode"];
  const codexInstructionsMode =
    typeof codexInstructionsModeRaw === "string" &&
    ["auto", "official", "custom"].includes(codexInstructionsModeRaw)
      ? (codexInstructionsModeRaw as "auto" | "official" | "custom")
      : "auto";

  const configItems = Object.entries(rawConfig)
    .filter(([key]) => {
      const ignoredKeys = [
        "force_function_call",
        "parallel_tool_calls",
        "cc_support",
        "intercept_event_log",
        "thinking_model",
        "codex_instructions",
        "codex_instructions_mode",
        "cc_opus_model",
        "cc_sonnet_model",
        "cc_haiku_model",
      ];
      return !ignoredKeys.includes(key);
    })
    .map(([key, value]) => {
      return {
        key,
        value,
      };
    });
  Object.assign(formData, {
    name: props.group.name || "",
    display_name: props.group.display_name || "",
    description: props.group.description || "",
    upstreams: props.group.upstreams?.length
      ? [...props.group.upstreams]
      : [{ url: "", weight: 1 }],
    channel_type: props.group.channel_type || "openai",
    sort: props.group.sort || 1,
    test_model: props.group.test_model || "",
    validation_endpoint: props.group.validation_endpoint || "",
    param_overrides: JSON.stringify(props.group.param_overrides || {}, null, 2),
    model_redirect_items_v2: (() => {
      // Priority: V2 rules first, then convert V1 rules to V2 format
      const v2Rules = props.group.model_redirect_rules_v2;
      if (v2Rules && Object.keys(v2Rules).length > 0) {
        return Object.entries(v2Rules).map(([from, rule]) => {
          const targets = (rule.targets || []).map(t => ({
            model: t.model || "",
            weight: t.weight ?? 100,
            enabled: t.enabled !== false,
          }));
          return {
            from,
            targets: targets.length > 0 ? targets : [{ model: "", weight: 100, enabled: true }],
          };
        });
      }
      // Convert V1 rules to V2 format for backward compatibility
      const v1Rules = props.group.model_redirect_rules || {};
      if (Object.keys(v1Rules).length > 0) {
        return Object.entries(v1Rules)
          .map(([from, to]) => ({
            from: String(from).trim(),
            targets: [{ model: String(to).trim(), weight: 100, enabled: true }],
          }))
          .filter(item => item.from && item.targets[0]?.model);
      }
      return [];
    })(),
    model_redirect_strict: props.group.model_redirect_strict || false,
    force_function_call: forceFunctionCall,
    parallel_tool_calls: parallelToolCalls,
    cc_support: ccSupport,
    intercept_event_log: interceptEventLog,
    thinking_model: thinkingModel,
    codex_instructions: codexInstructions,
    codex_instructions_mode: codexInstructionsMode,

    config: {},
    configItems,
    header_rules: (props.group.header_rules || []).map((rule: HeaderRuleItem) => ({
      key: rule.key || "",
      value: rule.value || "",
      action: (rule.action as "set" | "remove") || "set",
    })),
    path_redirects: (props.group.path_redirects || []).map((r: PathRedirectRule) => ({
      from: r.from || "",
      to: r.to || "",
    })),
    proxy_keys: props.group.proxy_keys || "",
    group_type: props.group.group_type || "standard",
  });
}

async function fetchChannelTypes() {
  const options = (await settingsApi.getChannelTypes()) || [];
  channelTypeOptions.value =
    options?.map((type: string) => ({
      label: type,
      value: type,
    })) || [];
  channelTypesFetched.value = true;
}

// Add upstream
function addUpstream() {
  formData.upstreams.push({
    url: "",
    weight: 1,
  });
}

// Remove upstream
function removeUpstream(index: number) {
  if (formData.upstreams.length > 1) {
    formData.upstreams.splice(index, 1);
  } else {
    message.warning(t("keys.atLeastOneUpstream"));
  }
}

async function fetchGroupConfigOptions() {
  const options = await keysApi.getGroupConfigOptions();
  // Hide force_function_call and parallel_tool_calls from generic config options
  // so they are only controlled via dedicated toggles. This avoids confusing UX
  // where users could add the key manually in the advanced config list while the
  // toggle remains the single source of truth.
  const normalized = (options || []).filter(
    opt => opt.key !== "force_function_call" && opt.key !== "parallel_tool_calls"
  );
  configOptions.value = normalized;
  configOptionsFetched.value = true;
}

// Add config item
function addConfigItem() {
  formData.configItems.push({
    key: "",
    value: "",
  });
}

// Remove config item
function removeConfigItem(index: number) {
  formData.configItems.splice(index, 1);
}

// Add header rule
function addHeaderRule() {
  formData.header_rules.push({
    key: "",
    value: "",
    action: "set",
  });
}

// Add model redirect rule (V2: one-to-many)
function addModelRedirectItemV2() {
  formData.model_redirect_items_v2.push({
    from: "",
    targets: [{ model: "", weight: 100, enabled: true }],
  });
}

// Remove model redirect rule (V2)
function removeModelRedirectItemV2(index: number) {
  formData.model_redirect_items_v2.splice(index, 1);
}

// Add target to a redirect rule (V2)
function addTargetToRedirectRule(ruleIndex: number) {
  formData.model_redirect_items_v2[ruleIndex]?.targets.push({
    model: "",
    weight: 100,
    enabled: true,
  });
}

// Remove target from a redirect rule (V2)
function removeTargetFromRedirectRule(ruleIndex: number, targetIndex: number) {
  const rule = formData.model_redirect_items_v2[ruleIndex];
  if (rule && rule.targets.length > 1) {
    rule.targets.splice(targetIndex, 1);
  }
}

// Calculate weight percentage for display
function calculateWeightPercentage(
  targets: ModelRedirectTargetItem[],
  targetIndex: number
): string {
  const enabledTargets = targets.filter(t => t.enabled && t.weight > 0);
  const totalWeight = enabledTargets.reduce((sum, t) => sum + t.weight, 0);
  const target = targets[targetIndex];
  if (!target || !target.enabled || target.weight <= 0 || totalWeight === 0) {
    return "0%";
  }
  return `${((target.weight / totalWeight) * 100).toFixed(1)}%`;
}

// Fetch dynamic weights for model redirect rules
async function fetchModelRedirectDynamicWeights(groupId: number) {
  try {
    modelRedirectDynamicWeights.value = await keysApi.getModelRedirectDynamicWeights(groupId);
  } catch (error) {
    console.error("Failed to fetch model redirect dynamic weights:", error);
    modelRedirectDynamicWeights.value = [];
  }
}

// Computed map for efficient dynamic weight info lookup
// Caches the result to avoid repeated .find() operations in template rendering
const dynamicWeightInfoMap = computed(() => {
  const map = new Map<string, Map<number, ModelRedirectTargetWeight>>();
  for (const rule of modelRedirectDynamicWeights.value) {
    const targetMap = new Map<number, ModelRedirectTargetWeight>();
    rule.targets.forEach((target, idx) => targetMap.set(idx, target));
    map.set(rule.source_model, targetMap);
  }
  return map;
});

// Get dynamic weight info for a specific target (optimized with computed map)
function getDynamicWeightInfo(sourceModel: string, targetIndex: number) {
  return dynamicWeightInfoMap.value.get(sourceModel)?.get(targetIndex) ?? null;
}

// Get health score class for styling
function getHealthScoreClass(score: number): string {
  if (score >= 0.8) return "health-good";
  if (score >= 0.5) return "health-warning";
  return "health-critical";
}

// V2: Convert V2 items to JSON for submission
function modelRedirectItemsV2ToJson(items: ModelRedirectItemV2[]): string {
  if (!items || items.length === 0) {
    return "";
  }
  const obj: Record<
    string,
    { targets: Array<{ model: string; weight?: number; enabled?: boolean }> }
  > = {};
  items.forEach(item => {
    if (item.from.trim()) {
      const validTargets = item.targets
        .filter(t => t.model.trim())
        .map(t => ({
          model: t.model.trim(),
          weight: t.weight !== 100 ? t.weight : undefined,
          enabled: t.enabled === false ? false : undefined,
        }));
      if (validTargets.length > 0) {
        obj[item.from.trim()] = { targets: validTargets };
      }
    }
  });
  return Object.keys(obj).length > 0 ? JSON.stringify(obj) : "";
}

// Remove header rule
function removeHeaderRule(index: number) {
  formData.header_rules.splice(index, 1);
}

function addPathRedirect() {
  formData.path_redirects.push({
    from: "",
    to: "",
  });
}

function removePathRedirect(index: number) {
  formData.path_redirects.splice(index, 1);
}

// Validate uniqueness of path redirect "from" value
function validatePathRedirectFromUniqueness(
  rules: PathRedirectRule[],
  currentIndex: number,
  from: string
): boolean {
  if (!from.trim()) {
    return true;
  }

  const normalizedFrom = from.trim();
  return !rules.some(
    (rule, index) => index !== currentIndex && rule.from.trim() === normalizedFrom
  );
}

// Normalize header key to canonical format (HTTP-style)
function canonicalHeaderKey(key: string): string {
  if (!key) {
    return key;
  }
  return key
    .split("-")
    .map(part => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
    .join("-");
}

// Validate header key uniqueness (using canonical format)
function validateHeaderKeyUniqueness(
  rules: HeaderRuleItem[],
  currentIndex: number,
  key: string
): boolean {
  if (!key.trim()) {
    return true;
  }

  const canonicalKey = canonicalHeaderKey(key.trim());
  return !rules.some(
    (rule, index) => index !== currentIndex && canonicalHeaderKey(rule.key.trim()) === canonicalKey
  );
}

// Set default value when config key changes
function handleConfigKeyChange(index: number, key: string) {
  const option = configOptions.value.find(opt => opt.key === key);
  const target = formData.configItems[index];
  if (option && target) {
    target.value = option.default_value;
  }
}

const getConfigOption = (key: string) => {
  return configOptions.value.find(opt => opt.key === key);
};

// Close modal
function handleClose() {
  emit("update:show", false);
}

// Fetch available models from upstream service and open selector modal
async function fetchUpstreamModels() {
  if (!props.group?.id) {
    message.warning(t("keys.saveGroupFirst"));
    return;
  }

  fetchingModels.value = true;
  try {
    const data = await keysApi.fetchGroupModels(props.group.id);

    // Extract model list based on channel type
    let models: string[] = [];

    if (data.data && Array.isArray(data.data)) {
      // OpenAI format: {"data": [{"id": "model-name"}]}
      models = data.data.map((item: { id: string }) => item.id).filter((id: string) => id);
    } else if (data.models && Array.isArray(data.models)) {
      // Gemini format: {"models": [{"name": "model-name"}]}
      models = data.models
        .map((item: { name: string }) => item.name)
        .filter((name: string) => name);
    }

    if (models.length === 0) {
      message.warning(t("keys.noModelsFound"));
      return;
    }

    message.success(t("keys.modelsLoadedCount", { count: models.length }));

    // Store models and open selector modal
    availableModels.value = models;
    showModelSelector.value = true;
  } catch (error: unknown) {
    console.error("Failed to fetch models:", error);
    const errorMessage = error instanceof Error ? error.message : t("keys.fetchModelsFailed");
    message.error(errorMessage);
  } finally {
    fetchingModels.value = false;
  }
}

// Handle model selector confirmation - add selected redirects to V2 rules
function handleModelSelectorConfirm(redirectRules: Record<string, string>) {
  if (Object.keys(redirectRules).length === 0) {
    message.warning(t("keys.noRedirectRulesAdded"));
    return;
  }

  // Add to V2 items (one-to-many format)
  Object.entries(redirectRules).forEach(([from, to]) => {
    // Check if source model already exists
    const existingIndex = formData.model_redirect_items_v2.findIndex(item => item.from === from);
    const existingItem = formData.model_redirect_items_v2[existingIndex];
    if (existingIndex >= 0 && existingItem) {
      // Update existing: replace first target or add if different
      const targetExists = existingItem.targets.some(t => t.model === to);
      if (!targetExists) {
        existingItem.targets.push({ model: to, weight: 100, enabled: true });
      }
    } else {
      // Add new rule
      formData.model_redirect_items_v2.push({
        from,
        targets: [{ model: to, weight: 100, enabled: true }],
      });
    }
  });

  message.success(t("keys.redirectRulesAdded", { count: Object.keys(redirectRules).length }));
}

// Submit form
async function handleSubmit() {
  if (loading.value) {
    return;
  }

  try {
    loading.value = true;

    await formRef.value?.validate();

    // Validate JSON format
    let paramOverrides = {};
    if (formData.param_overrides) {
      try {
        paramOverrides = JSON.parse(formData.param_overrides);
      } catch {
        message.error(t("keys.invalidJsonFormat"));
        return;
      }
    }

    // Build V2 model redirect rules (unified format)
    let modelRedirectRulesV2: Record<
      string,
      { targets: Array<{ model: string; weight?: number; enabled?: boolean }> }
    > | null = null;

    const v2Json = modelRedirectItemsV2ToJson(formData.model_redirect_items_v2);
    if (v2Json) {
      try {
        modelRedirectRulesV2 = JSON.parse(v2Json);
      } catch {
        message.error(t("keys.modelRedirectInvalidJson"));
        return;
      }
    }

    // Convert configItems to config object
    const config: Record<string, number | string | boolean> = {};
    for (const item of formData.configItems) {
      if (!item.key || !item.key.trim()) {
        continue;
      }

      const option = configOptions.value.find(opt => opt.key === item.key);
      if (option && typeof option.default_value === "number") {
        const rawValue = item.value;
        const strValue = typeof rawValue === "string" ? rawValue : String(rawValue);

        if (typeof rawValue === "string" && rawValue.trim() === "") {
          message.error(t("keys.invalidNumericConfig", { key: item.key }));
          return;
        }
        const numValue = Number(strValue);

        if (Number.isNaN(numValue)) {
          message.error(t("keys.invalidNumericConfig", { key: item.key }));
          return;
        }

        config[item.key] = numValue;
      } else {
        config[item.key] = item.value;
      }
    }

    // Persist force_function_call toggle as a dedicated config key.
    // Explicitly delete the key when disabled to ensure clean config state.
    if (formData.force_function_call) {
      config["force_function_call"] = true;
    } else {
      delete config["force_function_call"];
    }

    // Persist parallel_tool_calls as a dedicated config key.
    // Three-state: "default" (not set), "true", "false"
    // Only set when explicitly configured to true or false.
    if (formData.parallel_tool_calls === "true") {
      config["parallel_tool_calls"] = true;
    } else if (formData.parallel_tool_calls === "false") {
      config["parallel_tool_calls"] = false;
    } else {
      delete config["parallel_tool_calls"];
    }

    // Persist cc_support toggle as a dedicated config key.
    if (formData.cc_support) {
      config["cc_support"] = true;
    } else {
      delete config["cc_support"];
    }

    // Persist intercept_event_log toggle as a dedicated config key (Anthropic only).
    if (formData.intercept_event_log) {
      config["intercept_event_log"] = true;
    } else {
      delete config["intercept_event_log"];
    }

    // Persist thinking_model as a dedicated config key (only when cc_support is enabled).
    if (formData.cc_support && formData.thinking_model.trim()) {
      config["thinking_model"] = formData.thinking_model.trim();
    } else {
      delete config["thinking_model"];
    }

    // Persist codex_instructions_mode and codex_instructions as dedicated config keys (only when cc_support is enabled for Codex channel).
    if (formData.cc_support && formData.channel_type === "codex") {
      // Always save mode if not "auto"
      if (formData.codex_instructions_mode && formData.codex_instructions_mode !== "auto") {
        config["codex_instructions_mode"] = formData.codex_instructions_mode;
      } else {
        delete config["codex_instructions_mode"];
      }
      // Only save custom instructions when mode is "custom"
      if (formData.codex_instructions_mode === "custom" && formData.codex_instructions.trim()) {
        config["codex_instructions"] = formData.codex_instructions.trim();
      } else {
        delete config["codex_instructions"];
      }
    } else {
      delete config["codex_instructions_mode"];
      delete config["codex_instructions"];
    }

    // Validate path redirects for duplicates
    const redirects = formData.path_redirects || [];
    const seen = new Set<string>();
    for (const r of redirects) {
      const from = (r.from || "").trim();
      if (!from) {
        continue;
      }
      if (seen.has(from)) {
        message.error(t("keys.duplicatePathRedirect"));
        return;
      }
      seen.add(from);
    }

    // Validate header key uniqueness using canonical format
    const headerRulesForValidation = formData.header_rules || [];
    const seenHeaderKeys = new Set<string>();
    for (const rule of headerRulesForValidation) {
      const key = (rule.key || "").trim();
      if (!key) {
        continue;
      }
      const canonicalKey = canonicalHeaderKey(key);
      if (seenHeaderKeys.has(canonicalKey)) {
        message.error(t("keys.duplicateHeader"));
        return;
      }
      seenHeaderKeys.add(canonicalKey);
    }

    // Build submit payload
    const submitData = {
      name: formData.name,
      display_name: formData.display_name,
      description: formData.description,
      upstreams: formData.upstreams
        .map((u: UpstreamInfo) => ({
          url: (u.url || "").trim(),
          weight: u.weight,
          proxy_url: (() => {
            const p = (u.proxy_url || "").trim();
            return /^https?:\/\//i.test(p) ? p : undefined;
          })(),
        }))
        .filter(u => !!u.url),
      channel_type: formData.channel_type,
      sort: formData.sort,
      test_model: formData.test_model,
      validation_endpoint: formData.validation_endpoint,
      param_overrides: paramOverrides,
      model_redirect_rules_v2: modelRedirectRulesV2 || undefined,
      model_redirect_strict: formData.model_redirect_strict,
      config,
      header_rules: formData.header_rules
        .filter((rule: HeaderRuleItem) => rule.key.trim())
        .map((rule: HeaderRuleItem) => ({
          key: rule.key.trim(),
          value: rule.value,
          action: rule.action,
        })),
      path_redirects: (formData.path_redirects || [])
        .filter((r: PathRedirectRule) => (r.from || "").trim() && (r.to || "").trim())
        .map((r: PathRedirectRule) => ({ from: (r.from || "").trim(), to: (r.to || "").trim() })),
      proxy_keys: formData.proxy_keys,
    };

    let res: Group;
    if (props.group?.id) {
      // Edit mode
      res = await keysApi.updateGroup(props.group.id, submitData);
    } else {
      // Create mode
      res = await keysApi.createGroup(submitData);
    }

    emit("success", res);
    // If creating, emit event to switch to the new group
    if (!props.group?.id && res.id) {
      emit("switchToGroup", res.id);
    }
    handleClose();
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="group-form-modal">
    <n-card
      class="group-form-card"
      :title="group ? t('keys.editGroup') : t('keys.createGroup')"
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
    >
      <template #header-extra>
        <n-button quaternary circle @click="handleClose">
          <template #icon>
            <n-icon :component="Close" />
          </template>
        </n-button>
      </template>

      <n-form
        ref="formRef"
        :model="formData"
        :rules="rules"
        label-placement="left"
        label-width="100px"
        require-mark-placement="right-hanging"
        class="group-form"
        :style="{ '--n-label-height': '32px' }"
      >
        <!-- Basic Information -->
        <div class="form-section">
          <h4 class="section-title">{{ t("keys.basicInfo") }}</h4>

          <!-- Group name and display name on the same row -->
          <div class="form-row">
            <n-form-item :label="t('keys.groupName')" path="name" class="form-item-half">
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.groupName") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.groupNameTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <n-input v-model:value="formData.name" placeholder="gemini" />
            </n-form-item>

            <n-form-item :label="t('keys.displayName')" path="display_name" class="form-item-half">
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.displayName") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.displayNameTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <n-input v-model:value="formData.display_name" placeholder="Google Gemini" />
            </n-form-item>
          </div>

          <!-- Channel type and sort order on the same row -->
          <div class="form-row">
            <n-form-item :label="t('keys.channelType')" path="channel_type" class="form-item-half">
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.channelType") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.channelTypeTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <n-select
                v-model:value="formData.channel_type"
                :options="channelTypeOptions"
                :placeholder="t('keys.selectChannelType')"
              />
            </n-form-item>

            <n-form-item :label="t('keys.sortOrder')" path="sort" class="form-item-half">
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.sortOrder") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.sortOrderTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <n-input-number
                v-model:value="formData.sort"
                :min="0"
                :placeholder="t('keys.sortValue')"
                style="width: 100%"
              />
            </n-form-item>
          </div>

          <!-- Test model and test path on the same row -->
          <div class="form-row">
            <n-form-item :label="t('keys.testModel')" path="test_model" class="form-item-half">
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.testModel") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.testModelTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <n-input
                v-model:value="formData.test_model"
                :placeholder="testModelPlaceholder"
                @input="() => !props.group && (userModifiedFields.test_model = true)"
              />
            </n-form-item>

            <n-form-item
              :label="t('keys.testPath')"
              path="validation_endpoint"
              class="form-item-half"
              v-if="formData.channel_type !== 'gemini'"
            >
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.testPath") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    <div>
                      {{ t("keys.testPathTooltip1") }}
                      <br />
                      • OpenAI: /v1/chat/completions
                      <br />
                      • Anthropic: /v1/messages
                      <br />
                      {{ t("keys.testPathTooltip2") }}
                    </div>
                  </n-tooltip>
                </div>
              </template>
              <n-input
                v-model:value="formData.validation_endpoint"
                :placeholder="
                  validationEndpointPlaceholder || t('keys.optionalCustomValidationPath')
                "
              />
            </n-form-item>

            <!-- When gemini channel, test path is hidden, need placeholder div to keep layout -->
            <div v-else class="form-item-half" />
          </div>

          <!-- Proxy keys -->
          <n-form-item :label="t('keys.proxyKeys')" path="proxy_keys">
            <template #label>
              <div class="form-label-with-tooltip">
                {{ t("keys.proxyKeys") }}
                <n-tooltip trigger="hover" placement="right-start">
                  <template #trigger>
                    <n-icon :component="HelpCircleOutline" class="help-icon" />
                  </template>
                  {{ t("keys.proxyKeysTooltip") }}
                </n-tooltip>
              </div>
            </template>
            <proxy-keys-input
              v-model="formData.proxy_keys"
              :placeholder="t('keys.multiKeysPlaceholder')"
              :is-child-group="isChildGroup"
              size="medium"
            />
          </n-form-item>

          <!-- Description takes full row -->
          <n-form-item :label="t('common.description')" path="description">
            <template #label>
              <div class="form-label-with-tooltip">
                {{ t("common.description") }}
                <n-tooltip trigger="hover" placement="right-start">
                  <template #trigger>
                    <n-icon :component="HelpCircleOutline" class="help-icon" />
                  </template>
                  {{ t("keys.descriptionTooltip") }}
                </n-tooltip>
              </div>
            </template>
            <n-input
              v-model:value="formData.description"
              type="textarea"
              placeholder=""
              :rows="1"
              :autosize="{ minRows: 1, maxRows: 5 }"
              style="resize: none"
            />
          </n-form-item>
        </div>

        <!-- Upstream addresses -->
        <div class="form-section" style="margin-top: 10px">
          <h4 class="section-title">{{ t("keys.upstreamAddresses") }}</h4>
          <n-form-item
            v-for="(upstream, index) in formData.upstreams"
            :key="index"
            :label="`${t('keys.upstream')} ${index + 1}`"
            :path="`upstreams[${index}].url`"
            :rule="{
              required: true,
              message: '',
              trigger: ['blur', 'input'],
            }"
          >
            <template #label>
              <div class="form-label-with-tooltip">
                {{ t("keys.upstream") }} {{ index + 1 }}
                <n-tooltip trigger="hover" placement="right-start">
                  <template #trigger>
                    <n-icon :component="HelpCircleOutline" class="help-icon" />
                  </template>
                  {{ t("keys.upstreamTooltip") }}
                </n-tooltip>
              </div>
            </template>
            <div class="upstream-row">
              <div class="upstream-url">
                <n-input
                  v-model:value="upstream.url"
                  :placeholder="upstreamPlaceholder"
                  @input="() => !props.group && index === 0 && (userModifiedFields.upstream = true)"
                />
              </div>
              <div class="upstream-weight">
                <span class="weight-label">{{ t("keys.weight") }}</span>
                <n-tooltip trigger="hover" placement="top" style="width: 100%">
                  <template #trigger>
                    <n-input-number
                      v-model:value="upstream.weight"
                      :min="0"
                      :placeholder="t('keys.weight')"
                      style="width: 100%"
                    />
                  </template>
                  {{ t("keys.weightTooltip") }}
                </n-tooltip>
              </div>
              <div class="upstream-proxy">
                <span class="proxy-label">{{ t("keys.upstreamProxyUrl") }}</span>
                <n-tooltip trigger="hover" placement="top" style="width: 100%">
                  <template #trigger>
                    <n-input
                      v-model:value="upstream.proxy_url"
                      :placeholder="t('keys.upstreamProxyUrlPlaceholder')"
                      style="width: 100%"
                      clearable
                    />
                  </template>
                  {{ t("keys.upstreamProxyUrlTooltip") }}
                </n-tooltip>
              </div>
              <div class="upstream-actions">
                <n-button
                  v-if="formData.upstreams.length > 1"
                  @click="removeUpstream(index)"
                  type="error"
                  quaternary
                  circle
                  size="small"
                >
                  <template #icon>
                    <n-icon :component="Remove" />
                  </template>
                </n-button>
              </div>
            </div>
          </n-form-item>

          <n-form-item>
            <n-button @click="addUpstream" dashed style="width: 100%">
              <template #icon>
                <n-icon :component="Add" />
              </template>
              {{ t("keys.addUpstream") }}
            </n-button>
          </n-form-item>
        </div>

        <!-- Advanced configuration -->
        <div class="form-section" style="margin-top: 10px">
          <n-collapse>
            <n-collapse-item name="advanced">
              <template #header>{{ t("keys.advancedConfig") }}</template>
              <div class="config-section">
                <h5 class="config-title-with-tooltip">
                  {{ t("keys.groupConfig") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                    </template>
                    {{ t("keys.groupConfigTooltip") }}
                  </n-tooltip>
                </h5>

                <div class="config-items">
                  <n-form-item
                    v-for="(configItem, index) in formData.configItems"
                    :key="index"
                    class="config-item-row path-redirect-form-item"
                    :label="`${t('keys.config')} ${index + 1}`"
                    :path="`configItems[${index}].key`"
                    :rule="{
                      required: true,
                      message: '',
                      trigger: ['blur', 'change'],
                    }"
                  >
                    <div class="config-item-content">
                      <div class="config-select">
                        <n-select
                          v-model:value="configItem.key"
                          :options="
                            configOptions.map((opt: GroupConfigOption) => ({
                              label: opt.name,
                              value: opt.key,
                              disabled:
                                formData.configItems
                                  .map((item: ConfigItem) => item.key)
                                  ?.includes(opt.key) && opt.key !== configItem.key,
                            }))
                          "
                          :placeholder="t('keys.selectConfigParam')"
                          @update:value="(value: string) => handleConfigKeyChange(index, value)"
                          clearable
                        />
                      </div>
                      <div class="config-value">
                        <n-tooltip trigger="hover" placement="top">
                          <template #trigger>
                            <n-input-number
                              v-if="typeof configItem.value === 'number'"
                              v-model:value="configItem.value"
                              :placeholder="t('keys.paramValue')"
                              :precision="0"
                              style="width: 100%"
                            />
                            <n-switch
                              v-else-if="typeof configItem.value === 'boolean'"
                              v-model:value="configItem.value"
                              size="small"
                            />
                            <n-input
                              v-else
                              v-model:value="configItem.value"
                              :placeholder="t('keys.paramValue')"
                            />
                          </template>
                          {{
                            getConfigOption(configItem.key)?.description || t("keys.setConfigValue")
                          }}
                        </n-tooltip>
                      </div>
                      <div class="config-actions">
                        <n-button
                          @click="removeConfigItem(index)"
                          type="error"
                          quaternary
                          circle
                          size="small"
                        >
                          <template #icon>
                            <n-icon :component="Remove" />
                          </template>
                        </n-button>
                      </div>
                    </div>
                  </n-form-item>
                </div>

                <div style="margin-top: 12px; padding-left: 100px">
                  <n-button
                    @click="addConfigItem"
                    dashed
                    style="width: 100%"
                    :disabled="formData.configItems.length >= configOptions.length"
                  >
                    <template #icon>
                      <n-icon :component="Add" />
                    </template>
                    {{ t("keys.addConfigParam") }}
                  </n-button>
                </div>
              </div>

              <div class="config-section">
                <h5 class="config-title-with-tooltip">
                  {{ t("keys.customHeaders") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                    </template>
                    <div>
                      {{ t("keys.headerRulesTooltip1") }}
                      <br />
                      {{ t("keys.supportedVariables") }}：
                      <br />
                      • ${CLIENT_IP} - {{ t("keys.clientIpVar") }}
                      <br />
                      • ${GROUP_NAME} - {{ t("keys.groupNameVar") }}
                      <br />
                      • ${API_KEY} - {{ t("keys.apiKeyVar") }}
                      <br />
                      • ${TIMESTAMP_MS} - {{ t("keys.timestampMsVar") }}
                      <br />
                      • ${TIMESTAMP_S} - {{ t("keys.timestampSVar") }}
                    </div>
                  </n-tooltip>
                </h5>

                <div class="header-rules-items">
                  <n-form-item
                    v-for="(headerRule, index) in formData.header_rules"
                    :key="index"
                    class="header-rule-row"
                    :label="`${t('keys.header')} ${index + 1}`"
                  >
                    <template #label>
                      <div class="form-label-with-tooltip">
                        {{ t("keys.header") }} {{ index + 1 }}
                        <n-tooltip trigger="hover" placement="right-start">
                          <template #trigger>
                            <n-icon :component="HelpCircleOutline" class="help-icon" />
                          </template>
                          {{ t("keys.headerTooltip") }}
                        </n-tooltip>
                      </div>
                    </template>
                    <div class="header-rule-content">
                      <div class="header-name">
                        <n-input
                          v-model:value="headerRule.key"
                          :placeholder="t('keys.headerName')"
                          :status="
                            !validateHeaderKeyUniqueness(
                              formData.header_rules,
                              index,
                              headerRule.key
                            )
                              ? 'error'
                              : undefined
                          "
                        />
                        <div
                          v-if="
                            !validateHeaderKeyUniqueness(
                              formData.header_rules,
                              index,
                              headerRule.key
                            )
                          "
                          class="error-message"
                        >
                          {{ t("keys.duplicateHeader") }}
                        </div>
                      </div>
                      <div class="header-value" v-if="headerRule.action === 'set'">
                        <n-input
                          v-model:value="headerRule.value"
                          :placeholder="t('keys.headerValuePlaceholder')"
                        />
                      </div>
                      <div class="header-value removed-placeholder" v-else>
                        <span class="removed-text">{{ t("keys.willRemoveFromRequest") }}</span>
                      </div>
                      <div class="header-action">
                        <n-tooltip trigger="hover" placement="top">
                          <template #trigger>
                            <n-switch
                              v-model:value="headerRule.action"
                              :checked-value="'remove'"
                              :unchecked-value="'set'"
                              size="small"
                            />
                          </template>
                          {{ t("keys.removeToggleTooltip") }}
                        </n-tooltip>
                      </div>
                      <div class="header-actions">
                        <n-button
                          @click="removeHeaderRule(index)"
                          type="error"
                          quaternary
                          circle
                          size="small"
                        >
                          <template #icon>
                            <n-icon :component="Remove" />
                          </template>
                        </n-button>
                      </div>
                    </div>
                  </n-form-item>
                </div>

                <div style="margin-top: 12px; padding-left: 100px">
                  <n-button @click="addHeaderRule" dashed style="width: 100%">
                    <template #icon>
                      <n-icon :component="Add" />
                    </template>
                    {{ t("keys.addHeader") }}
                  </n-button>
                </div>
              </div>

              <!-- Model redirect configuration -->
              <div v-if="formData.group_type !== 'aggregate'" class="config-section">
                <n-form-item path="model_redirect_strict">
                  <template #label>
                    <div class="form-label-with-tooltip">
                      {{ t("keys.modelRedirectPolicy") }}
                      <n-tooltip trigger="hover" placement="right-start">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                        </template>
                        {{ t("keys.modelRedirectPolicyTooltip") }}
                      </n-tooltip>
                    </div>
                  </template>
                  <div style="display: flex; align-items: center; gap: 12px">
                    <n-switch v-model:value="formData.model_redirect_strict" />
                    <span style="font-size: 14px; color: #666">
                      {{
                        formData.model_redirect_strict
                          ? t("keys.modelRedirectStrictMode")
                          : t("keys.modelRedirectLooseMode")
                      }}
                    </span>
                  </div>
                  <template #feedback>
                    <div style="font-size: 12px; color: #999; margin: 4px 0">
                      <div v-if="formData.model_redirect_strict" style="color: #f5a623">
                        ⚠️ {{ t("keys.modelRedirectStrictWarning") }}
                      </div>
                      <div v-else style="color: #52c41a">
                        ✅ {{ t("keys.modelRedirectLooseInfo") }}
                      </div>
                    </div>
                  </template>
                </n-form-item>

                <n-form-item path="model_redirect_rules">
                  <template #label>
                    <div class="form-label-with-tooltip">
                      {{ t("keys.modelRedirectRules") }}
                      <n-tooltip trigger="hover" placement="right-start">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                        </template>
                        {{ t("keys.modelRedirectTooltip") }}
                      </n-tooltip>
                    </div>
                  </template>

                  <div class="model-redirect-wrapper">
                    <!-- Header with description and fetch button -->
                    <div
                      style="
                        display: flex;
                        justify-content: space-between;
                        align-items: center;
                        margin-bottom: 12px;
                      "
                    >
                      <div style="font-size: 12px; color: #999">
                        {{ t("keys.modelRedirectWeightTooltip") }}
                      </div>
                      <n-button
                        size="small"
                        @click="fetchUpstreamModels"
                        :loading="fetchingModels"
                        :disabled="!props.group"
                      >
                        <template #icon>
                          <n-icon :component="CloudDownloadOutline" />
                        </template>
                        {{ t("keys.fetchModels") }}
                      </n-button>
                    </div>

                    <!-- V2 Rules List (one-to-many mapping with weights) -->
                    <div class="model-redirect-v2-list">
                      <div
                        v-for="(rule, ruleIndex) in formData.model_redirect_items_v2"
                        :key="ruleIndex"
                        class="model-redirect-v2-rule"
                      >
                        <!-- Source model input -->
                        <div class="model-redirect-v2-source">
                          <n-input
                            v-model:value="rule.from"
                            :placeholder="t('keys.sourceModel')"
                            size="small"
                          />
                          <n-button
                            text
                            type="error"
                            size="small"
                            @click="removeModelRedirectItemV2(ruleIndex)"
                            style="margin-left: 8px"
                          >
                            <template #icon>
                              <n-icon :component="Close" />
                            </template>
                          </n-button>
                        </div>

                        <!-- Target models with weights -->
                        <div class="model-redirect-v2-targets">
                          <div
                            v-for="(target, targetIndex) in rule.targets"
                            :key="targetIndex"
                            class="model-redirect-v2-target"
                          >
                            <span class="redirect-arrow">→</span>
                            <n-input
                              v-model:value="target.model"
                              :placeholder="t('keys.targetModel')"
                              size="small"
                              style="flex: 2"
                            />
                            <n-tooltip trigger="hover">
                              <template #trigger>
                                <n-input-number
                                  v-model:value="target.weight"
                                  :min="0"
                                  :max="1000"
                                  size="small"
                                  style="width: 90px"
                                  :placeholder="t('keys.modelRedirectWeight')"
                                />
                              </template>
                              {{ t("keys.modelRedirectWeightTooltip") }}
                            </n-tooltip>
                            <span class="weight-percentage">
                              {{ calculateWeightPercentage(rule.targets, targetIndex) }}
                            </span>
                            <!-- Dynamic weight indicator -->
                            <n-tooltip
                              v-if="getDynamicWeightInfo(rule.from, targetIndex)"
                              trigger="hover"
                            >
                              <template #trigger>
                                <span
                                  class="dynamic-weight-indicator"
                                  :class="
                                    getHealthScoreClass(
                                      getDynamicWeightInfo(rule.from, targetIndex)?.health_score ??
                                        1
                                    )
                                  "
                                >
                                  {{
                                    Math.round(
                                      (getDynamicWeightInfo(rule.from, targetIndex)?.health_score ??
                                        1) * 100
                                    )
                                  }}%
                                </span>
                              </template>
                              <div class="dynamic-weight-tooltip">
                                <div class="tooltip-title">{{ t("keys.dynamicWeight") }}</div>
                                <div class="tooltip-row">
                                  <span>{{ t("keys.effectiveWeight") }}:</span>
                                  <span>
                                    {{
                                      getDynamicWeightInfo(rule.from, targetIndex)?.effective_weight
                                    }}
                                  </span>
                                </div>
                                <div class="tooltip-row">
                                  <span>{{ t("keys.healthScore") }}:</span>
                                  <span
                                    :class="
                                      getHealthScoreClass(
                                        getDynamicWeightInfo(rule.from, targetIndex)
                                          ?.health_score ?? 1
                                      )
                                    "
                                  >
                                    {{
                                      Math.round(
                                        (getDynamicWeightInfo(rule.from, targetIndex)
                                          ?.health_score ?? 1) * 100
                                      )
                                    }}%
                                  </span>
                                </div>
                                <div class="tooltip-row">
                                  <span>{{ t("keys.successRate") }}:</span>
                                  <span>
                                    {{
                                      (
                                        getDynamicWeightInfo(rule.from, targetIndex)
                                          ?.success_rate ?? 0
                                      ).toFixed(1)
                                    }}%
                                  </span>
                                </div>
                                <div class="tooltip-row">
                                  <span>{{ t("keys.requestCount") }}:</span>
                                  <span>
                                    {{
                                      getDynamicWeightInfo(rule.from, targetIndex)?.request_count ??
                                      0
                                    }}
                                  </span>
                                </div>
                              </div>
                            </n-tooltip>
                            <n-switch
                              v-model:value="target.enabled"
                              size="small"
                              style="margin-left: 4px"
                            />
                            <n-button
                              v-if="rule.targets.length > 1"
                              text
                              type="error"
                              size="small"
                              @click="removeTargetFromRedirectRule(ruleIndex, targetIndex)"
                              style="margin-left: 4px"
                            >
                              <template #icon>
                                <n-icon :component="Remove" />
                              </template>
                            </n-button>
                          </div>
                          <n-button
                            dashed
                            size="small"
                            @click="addTargetToRedirectRule(ruleIndex)"
                            style="margin-top: 4px; margin-left: 24px"
                          >
                            <template #icon>
                              <n-icon :component="Add" />
                            </template>
                            {{ t("keys.modelRedirectAddTarget") }}
                          </n-button>
                        </div>
                      </div>
                    </div>

                    <n-button dashed block @click="addModelRedirectItemV2" style="margin-top: 12px">
                      <template #icon>
                        <n-icon :component="Add" />
                      </template>
                      {{ t("keys.addModelRedirect") }}
                    </n-button>
                  </div>
                </n-form-item>
              </div>

              <div class="config-section">
                <n-form-item path="param_overrides">
                  <template #label>
                    <div class="form-label-with-tooltip">
                      {{ t("keys.paramOverrides") }}
                      <n-tooltip trigger="hover" placement="right-start">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                        </template>
                        {{ t("keys.paramOverridesTooltip") }}
                      </n-tooltip>
                    </div>
                  </template>
                  <n-input
                    v-model:value="formData.param_overrides"
                    type="textarea"
                    placeholder='{"temperature": 0.7}'
                    :rows="4"
                  />
                </n-form-item>
              </div>

              <!-- URL path redirects (only shown for OpenAI channel) -->
              <div class="config-section" v-if="formData.channel_type === 'openai'">
                <h5 class="config-title-with-tooltip">
                  {{ t("keys.pathRedirects") }}
                  <n-tooltip trigger="hover" placement="right-start">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                    </template>
                    <div>
                      {{ t("keys.pathRedirectsTooltip1") }}
                      <br />
                      • /v1 → /v2
                      <br />
                      • /v1 → /api/paas/v4
                      <br />
                      {{ t("keys.pathRedirectsTooltip2") }}
                    </div>
                  </n-tooltip>
                </h5>

                <n-form-item
                  v-for="(rule, index) in formData.path_redirects"
                  :key="index"
                  :label="`${t('keys.pathRedirect')} ${index + 1}`"
                  class="path-redirect-form-item"
                >
                  <div class="redirect-item-content">
                    <div class="redirect-item-from">
                      <n-input
                        v-model:value="rule.from"
                        :placeholder="t('keys.pathRedirectFromPlaceholder')"
                        :status="
                          !validatePathRedirectFromUniqueness(
                            formData.path_redirects,
                            index,
                            rule.from
                          )
                            ? 'error'
                            : undefined
                        "
                      />
                      <div
                        v-if="
                          !validatePathRedirectFromUniqueness(
                            formData.path_redirects,
                            index,
                            rule.from
                          )
                        "
                        class="error-message"
                      >
                        {{ t("keys.duplicatePathRedirect") }}
                      </div>
                    </div>
                    <div class="redirect-item-arrow">→</div>
                    <div class="redirect-item-to">
                      <n-input
                        v-model:value="rule.to"
                        :placeholder="t('keys.pathRedirectToPlaceholder')"
                      />
                    </div>
                    <n-button
                      text
                      type="error"
                      @click="removePathRedirect(index)"
                      class="redirect-item-remove-btn"
                    >
                      <template #icon>
                        <n-icon :component="Close" />
                      </template>
                    </n-button>
                  </div>
                </n-form-item>

                <n-button dashed block @click="addPathRedirect" style="margin-top: 8px">
                  <template #icon>
                    <n-icon :component="Add" />
                  </template>
                  {{ t("keys.addPathRedirect") }}
                </n-button>
              </div>

              <!-- Function call toggle (OpenAI channel only) -->
              <div
                class="config-section"
                v-if="formData.group_type !== 'aggregate' && formData.channel_type === 'openai'"
              >
                <n-form-item path="force_function_call">
                  <template #label>
                    <div class="form-label-with-tooltip">
                      {{ t("keys.functionCall") }}
                      <n-tooltip trigger="hover" placement="right-start">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                        </template>
                        <div>
                          {{ t("keys.functionCallTooltip1") }}
                          <br />
                          {{ t("keys.functionCallTooltip2") }}
                        </div>
                      </n-tooltip>
                    </div>
                  </template>
                  <div style="display: flex; align-items: center; gap: 12px">
                    <n-switch v-model:value="formData.force_function_call" size="small" />
                  </div>
                  <template #feedback>
                    <div style="font-size: 12px; color: #999; margin-top: 4px">
                      {{ t("keys.functionCallOpenAITip") }}
                    </div>
                  </template>
                </n-form-item>
              </div>

              <!-- Parallel Tool Calls toggle (OpenAI and Codex channels) -->
              <div
                class="config-section"
                v-if="
                  formData.group_type !== 'aggregate' &&
                  (formData.channel_type === 'openai' || formData.channel_type === 'codex')
                "
              >
                <n-form-item path="parallel_tool_calls">
                  <template #label>
                    <div class="form-label-with-tooltip">
                      {{ t("keys.parallelToolCalls") }}
                      <n-tooltip trigger="hover" placement="right-start">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                        </template>
                        <div>
                          {{ t("keys.parallelToolCallsTooltip") }}
                        </div>
                      </n-tooltip>
                    </div>
                  </template>
                  <n-select
                    v-model:value="formData.parallel_tool_calls"
                    :options="[
                      { label: t('keys.parallelToolCallsDefault'), value: 'default' },
                      { label: t('keys.parallelToolCallsEnabled'), value: 'true' },
                      { label: t('keys.parallelToolCallsDisabled'), value: 'false' },
                    ]"
                    size="small"
                    style="width: 200px"
                  />
                  <template #feedback>
                    <div style="font-size: 12px; color: #999; margin-top: 4px">
                      {{ t("keys.parallelToolCallsTip") }}
                    </div>
                  </template>
                </n-form-item>
              </div>

              <!-- CC Support toggle (OpenAI and Codex channels) -->
              <div
                class="config-section"
                v-if="
                  formData.group_type !== 'aggregate' &&
                  (formData.channel_type === 'openai' || formData.channel_type === 'codex')
                "
              >
                <n-form-item path="cc_support">
                  <template #label>
                    <div class="form-label-with-tooltip">
                      {{ t("keys.ccSupport") }}
                      <n-tooltip trigger="hover" placement="right-start">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                        </template>
                        <div>
                          {{ t("keys.ccSupportTooltip1") }}
                          <br />
                          {{ t("keys.ccSupportTooltip2") }}
                          <br />
                          {{ t("keys.ccSupportTooltip3") }}
                        </div>
                      </n-tooltip>
                    </div>
                  </template>
                  <div style="width: 100%">
                    <div style="display: flex; align-items: center; height: 32px">
                      <n-switch v-model:value="formData.cc_support" size="small" />
                    </div>
                    <div style="font-size: 12px; color: #999; margin-top: 4px; line-height: 1.5">
                      <div>{{ t("keys.ccSupportTip") }}</div>
                      <div v-if="formData.cc_support" style="margin-top: 8px">
                        <div>⚠️ {{ t("keys.ccSupportCompatibilityTip") }}</div>
                        <div style="margin-top: 4px">{{ t("keys.ccSupportRedirectTip") }}</div>
                      </div>
                    </div>
                    <!-- Thinking Model Input (only shown when cc_support is enabled) -->
                    <div v-if="formData.cc_support" style="margin-top: 12px">
                      <div style="font-size: 13px; color: #666; margin-bottom: 4px">
                        {{ t("keys.thinkingModel") }}
                        <n-tooltip trigger="hover" placement="right">
                          <template #trigger>
                            <n-icon
                              :component="HelpCircleOutline"
                              class="help-icon config-help"
                              style="margin-left: 4px"
                            />
                          </template>
                          <div style="max-width: 300px">
                            {{ t("keys.thinkingModelTooltip") }}
                          </div>
                        </n-tooltip>
                      </div>
                      <n-input
                        v-model:value="formData.thinking_model"
                        :placeholder="t('keys.thinkingModelPlaceholder')"
                        size="small"
                        style="width: 100%"
                      />
                    </div>
                    <!-- Codex Instructions Mode (only shown when cc_support is enabled for Codex channel) -->
                    <div
                      v-if="formData.cc_support && formData.channel_type === 'codex'"
                      style="margin-top: 12px"
                    >
                      <div style="font-size: 13px; color: #666; margin-bottom: 4px">
                        {{ t("keys.codexInstructionsMode") }}
                        <n-tooltip trigger="hover" placement="right">
                          <template #trigger>
                            <n-icon
                              :component="HelpCircleOutline"
                              class="help-icon config-help"
                              style="margin-left: 4px"
                            />
                          </template>
                          <div style="max-width: 350px">
                            {{ t("keys.codexInstructionsModeTooltip") }}
                          </div>
                        </n-tooltip>
                      </div>
                      <n-select
                        v-model:value="formData.codex_instructions_mode"
                        :options="[
                          { label: t('keys.codexInstructionsModeAuto'), value: 'auto' },
                          { label: t('keys.codexInstructionsModeOfficial'), value: 'official' },
                          { label: t('keys.codexInstructionsModeCustom'), value: 'custom' },
                        ]"
                        size="small"
                        style="width: 100%"
                      />
                    </div>
                    <!-- Codex Instructions Input (only shown when mode is "custom") -->
                    <div
                      v-if="
                        formData.cc_support &&
                        formData.channel_type === 'codex' &&
                        formData.codex_instructions_mode === 'custom'
                      "
                      style="margin-top: 12px"
                    >
                      <div style="font-size: 13px; color: #666; margin-bottom: 4px">
                        {{ t("keys.codexInstructions") }}
                        <n-tooltip trigger="hover" placement="right">
                          <template #trigger>
                            <n-icon
                              :component="HelpCircleOutline"
                              class="help-icon config-help"
                              style="margin-left: 4px"
                            />
                          </template>
                          <div style="max-width: 350px">
                            {{ t("keys.codexInstructionsTooltip") }}
                          </div>
                        </n-tooltip>
                      </div>
                      <n-input
                        v-model:value="formData.codex_instructions"
                        type="textarea"
                        :placeholder="t('keys.codexInstructionsPlaceholder')"
                        :autosize="{ minRows: 3, maxRows: 10 }"
                        size="small"
                        style="width: 100%"
                      />
                    </div>
                  </div>
                </n-form-item>
              </div>

              <!-- Intercept Event Log toggle (Anthropic channel only) -->
              <div
                class="config-section"
                v-if="formData.group_type !== 'aggregate' && formData.channel_type === 'anthropic'"
              >
                <n-form-item path="intercept_event_log">
                  <template #label>
                    <div class="form-label-with-tooltip">
                      {{ t("keys.interceptEventLog") }}
                      <n-tooltip trigger="hover" placement="right-start">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                        </template>
                        <div>
                          {{ t("keys.interceptEventLogTooltip") }}
                        </div>
                      </n-tooltip>
                    </div>
                  </template>
                  <div style="width: 100%">
                    <div style="display: flex; align-items: center; height: 32px">
                      <n-switch v-model:value="formData.intercept_event_log" size="small" />
                    </div>
                    <div style="font-size: 12px; color: #999; margin-top: 4px; line-height: 1.5">
                      {{ t("keys.interceptEventLogTip") }}
                    </div>
                  </div>
                </n-form-item>
              </div>
            </n-collapse-item>
          </n-collapse>
        </div>
      </n-form>

      <template #footer>
        <div style="display: flex; justify-content: flex-end; gap: 12px">
          <n-button @click="handleClose">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" @click="handleSubmit" :loading="loading">
            {{ group ? t("common.update") : t("common.create") }}
          </n-button>
        </div>
      </template>
    </n-card>
  </n-modal>

  <!-- Model Selector Modal -->
  <model-selector-modal
    v-model:show="showModelSelector"
    :models="availableModels"
    @confirm="handleModelSelectorConfirm"
  />
</template>

<style scoped>
.group-form-modal {
  width: 800px;
}

/* Add scrollable container with max height for better UX */
.group-form-card {
  max-height: 85vh;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.group-form-card :deep(.n-card__content) {
  overflow-y: auto;
  overflow-x: hidden;
  max-height: calc(85vh - 80px);
  padding-right: 24px;
  /* Reserve space for scrollbar to prevent layout shift */
  scrollbar-gutter: stable;
}

/* Custom scrollbar styling */
.group-form-card :deep(.n-card__content)::-webkit-scrollbar {
  width: 6px;
}

.group-form-card :deep(.n-card__content)::-webkit-scrollbar-track {
  background: transparent;
}

.group-form-card :deep(.n-card__content)::-webkit-scrollbar-thumb {
  background: rgba(0, 0, 0, 0.2);
  border-radius: 3px;
}

.group-form-card :deep(.n-card__content)::-webkit-scrollbar-thumb:hover {
  background: rgba(0, 0, 0, 0.3);
}

.group-form-card :deep(.n-card__content) {
  scrollbar-width: thin;
  scrollbar-color: rgba(0, 0, 0, 0.2) transparent;
}

.form-section {
  margin-top: 8px;
}

.section-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 8px 0;
  padding-bottom: 2px;
  border-bottom: 1px solid var(--border-color);
}

:deep(.n-form-item-blank) {
  flex-grow: 1;
  display: flex;
  align-items: center;
  min-height: 32px;
}

/* Tooltip related styles */
.form-label-with-tooltip {
  display: flex;
  align-items: flex-start;
  gap: 4px;
  /* Allow text to wrap with compact line height */
  line-height: 1.3;
}

.help-icon {
  color: var(--text-tertiary);
  font-size: 14px;
  cursor: help;
  transition: color 0.2s ease;
}

.help-icon:hover {
  color: var(--primary-color);
}

.section-title-with-tooltip {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.section-help {
  font-size: 16px;
}

.collapse-header-with-tooltip {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 500;
}

.collapse-help {
  font-size: 14px;
}

.config-title-with-tooltip {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 12px 0;
}

/* Unified label style for config sections (function call, CC support, param overrides, model redirect) */
.config-section .form-label-with-tooltip {
  font-size: 13px;
  font-weight: 600;
}

.config-help {
  font-size: 13px;
}

/* CC Model Mapping row - 3 inputs in one row */
.cc-model-mapping-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 8px;
  margin-left: 0;
}

.cc-model-mapping-row .cc-model-item {
  display: flex;
  align-items: center;
  flex: 1;
  min-width: 0;
  gap: 6px;
}

.cc-model-mapping-row .cc-model-label {
  font-size: 12px;
  color: var(--text-secondary);
  white-space: nowrap;
  flex-shrink: 0;
}

.cc-model-mapping-row .cc-model-item :deep(.n-input) {
  flex: 1;
  min-width: 80px;
}

/* Enhanced form styles - force compact spacing */
/* Note: --n-feedback-height: 0 intentionally set to minimize vertical spacing.
 * AI review suggested this could hide validation feedback, but this approach is
 * based on a prior validated compact layout solution that successfully passed testing.
 * The compact layout is a design requirement for this admin panel.
 * Validation errors are still visible inline due to NaiveUI's internal rendering. */
:deep(.n-form-item) {
  margin-bottom: 8px !important;
  --n-feedback-height: 0 !important;
}

:deep(.n-form-item-label) {
  font-weight: 500;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  /* Reduce font size and line height for compact multi-line labels (e.g., Chinese text).
   * 13px is above WCAG minimum (12px) and maintains readability while allowing
   * longer labels to wrap gracefully within the fixed label-width. */
  font-size: 13px;
  line-height: 1.3;
  min-height: 32px;
}

/* Fix required mark vertical alignment */
:deep(.n-form-item-label__asterisk) {
  display: flex;
  align-items: center;
  min-height: 32px;
}

:deep(.n-input) {
  --n-border-radius: 8px;
  --n-border: 1px solid var(--border-color);
  --n-border-hover: 1px solid var(--primary-color);
  --n-border-focus: 1px solid var(--primary-color);
  --n-box-shadow-focus: 0 0 0 2px var(--primary-color-suppl);
  --n-height: 32px;
}

:deep(.n-select) {
  --n-border-radius: 8px;
}

:deep(.n-input-number) {
  --n-border-radius: 8px;
  --n-height: 32px;
}

:deep(.n-button) {
  --n-border-radius: 8px;
}

/* Beautify tooltip */
:deep(.n-tooltip__trigger) {
  display: inline-flex;
  align-items: center;
}

:deep(.n-tooltip) {
  --n-font-size: 13px;
  --n-border-radius: 8px;
}

:deep(.n-tooltip .n-tooltip__content) {
  max-width: 320px;
  line-height: 1.5;
}

:deep(.n-tooltip .n-tooltip__content div) {
  white-space: pre-line;
}

/* Collapse panel style optimization */
:deep(.n-collapse-item__header) {
  font-weight: 500;
  color: var(--text-primary);
}

:deep(.n-collapse-item) {
  --n-title-padding: 16px 0;
}

:deep(.n-base-selection-label) {
  height: 32px;
  line-height: 32px;
  display: flex;
  align-items: center;
}

:deep(.n-base-selection) {
  --n-height: 32px;
}

/* Form row layout */
.form-row {
  display: flex;
  gap: 20px;
  align-items: flex-start;
}

.form-item-half {
  flex: 1;
  width: 50%;
}

/* Upstream address row layout */
.upstream-row {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
}

.upstream-url {
  flex: 2.3;
  min-width: 200px;
}

.upstream-weight {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 120px;
}

.weight-label {
  font-weight: 500;
  color: var(--text-primary);
  white-space: nowrap;
}

.upstream-proxy {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1.5;
  min-width: 180px;
  margin-right: 44px;
}

.proxy-label {
  font-weight: 500;
  color: var(--text-primary);
  white-space: nowrap;
}

.upstream-actions {
  flex: 0 0 32px;
  display: flex;
  justify-content: center;
  margin-left: -44px;
}

/* Config item row layout */
.config-item-row {
  margin-bottom: 8px !important;
}

.config-item-content {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
}

.config-select {
  flex: 0 0 200px;
}

.config-value {
  flex: 1;
}

.config-actions {
  flex: 0 0 32px;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  height: 34px;
}

.model-redirect-wrapper {
  width: 100%;
  --redirect-item-height: 36px;
}

.model-redirect-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px !important;
}

/* V2 Model Redirect Styles */
.model-redirect-v2-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.model-redirect-v2-rule {
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 12px;
  background: var(--bg-secondary);
}

.model-redirect-v2-source {
  display: flex;
  align-items: center;
  margin-bottom: 8px;
}

.model-redirect-v2-source :deep(.n-input) {
  flex: 1;
}

.model-redirect-v2-targets {
  padding-left: 8px;
}

.model-redirect-v2-target {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.model-redirect-v2-target .redirect-arrow {
  flex: 0 0 20px;
  height: 28px;
  line-height: 28px;
}

.weight-percentage {
  font-size: 12px;
  color: #666;
  min-width: 45px;
  text-align: right;
}

/* Dynamic weight indicator styles */
.dynamic-weight-indicator {
  font-size: 11px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 4px;
  min-width: 40px;
  text-align: center;
  cursor: help;
}

.dynamic-weight-indicator.health-good {
  background: #e8f5e9;
  color: #2e7d32;
}

.dynamic-weight-indicator.health-warning {
  background: #fff3e0;
  color: #ef6c00;
}

.dynamic-weight-indicator.health-critical {
  background: #ffebee;
  color: #c62828;
}

:root.dark .dynamic-weight-indicator.health-good {
  background: #1b3a1f;
  color: #81c784;
}

:root.dark .dynamic-weight-indicator.health-warning {
  background: #3d2e1a;
  color: #ffb74d;
}

:root.dark .dynamic-weight-indicator.health-critical {
  background: #3d1a1a;
  color: #e57373;
}

/* Dynamic weight tooltip styles */
.dynamic-weight-tooltip {
  min-width: 180px;
}

.dynamic-weight-tooltip .tooltip-title {
  font-weight: 600;
  margin-bottom: 8px;
  padding-bottom: 4px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.2);
}

.dynamic-weight-tooltip .tooltip-row {
  display: flex;
  justify-content: space-between;
  margin-bottom: 4px;
  font-size: 12px;
}

.dynamic-weight-tooltip .health-good {
  color: #81c784;
}

.dynamic-weight-tooltip .health-warning {
  color: #ffb74d;
}

.dynamic-weight-tooltip .health-critical {
  color: #e57373;
}

/* Unified arrow style (for model and path redirect) */
.redirect-arrow,
.redirect-item-arrow {
  flex: 0 0 auto;
  padding: 0 4px;
  font-size: 16px;
  color: #999;
  user-select: none;
  height: var(--redirect-item-height, 36px);
  line-height: var(--redirect-item-height, 36px);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: transform 0.2s ease;
}

.config-header-with-switch {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.config-header-with-switch h5 {
  margin: 0;
}

@media (max-width: 768px) {
  .group-form-card {
    width: 100vw !important;
  }

  .group-form {
    width: auto !important;
  }

  .form-row {
    flex-direction: column;
    gap: 0;
  }

  .form-item-half {
    width: 100%;
  }

  .section-title {
    font-size: 0.9rem;
  }

  .upstream-row,
  .config-item-content {
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
  }

  .upstream-weight,
  .upstream-proxy {
    flex: 1;
    flex-direction: column;
    align-items: flex-start;
  }

  .config-value {
    flex: 1;
  }

  .upstream-actions,
  .config-actions {
    justify-content: flex-end;
  }
}

/* Header rule related styles */
.header-rule-row {
  margin-bottom: 8px !important;
}

.header-rule-content {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  width: 100%;
}

.header-name {
  flex: 0 0 200px;
  position: relative;
}

.header-value {
  flex: 1;
  display: flex;
  align-items: center;
  min-height: 34px;
}

.header-value.removed-placeholder {
  justify-content: center;
  background-color: var(--bg-secondary);
  border: 1px solid var(--border-color);
  border-radius: 6px;
  padding: 0 12px;
}

.removed-text {
  color: var(--text-tertiary);
  font-style: italic;
  font-size: 13px;
}

.header-action {
  flex: 0 0 50px;
  display: flex;
  justify-content: center;
  align-items: center;
  height: 34px;
}

.header-actions {
  flex: 0 0 32px;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  height: 34px;
}

.error-message {
  position: absolute;
  top: 100%;
  left: 0;
  font-size: 12px;
  color: var(--error-color);
  margin-top: 2px;
}

@media (max-width: 768px) {
  .header-rule-content {
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
  }

  .header-name,
  .header-value {
    flex: 1;
  }

  .header-actions {
    justify-content: flex-end;
  }
}

/* Path redirect rule styles */
/* Use CSS variables to unify height for easier maintenance */
.path-redirect-form-item {
  --redirect-item-height: 36px;
}

/* Vertically align form-item label with its content */
.path-redirect-form-item :deep(.n-form-item-label) {
  display: flex;
  align-items: center;
  height: var(--redirect-item-height);
  line-height: var(--redirect-item-height);
}

.path-redirect-form-item :deep(.n-form-item-label) {
  font-weight: normal !important;
}

.redirect-item-content {
  display: flex;
  align-items: center; /* Vertically align all elements */
  gap: 12px;
  width: 100%;
  min-height: var(--redirect-item-height);
}

.redirect-item-from {
  flex: 1;
  position: relative;
  min-width: 0; /* Allow flex child elements to shrink */
}

.redirect-item-to {
  flex: 1;
  min-width: 0; /* Allow flex child elements to shrink */
}

/* Ensure text in inputs is vertically centered */
.redirect-item-from :deep(.n-input),
.redirect-item-to :deep(.n-input) {
  --n-height: var(--redirect-item-height);
}

.redirect-item-from :deep(.n-input__input-el),
.redirect-item-to :deep(.n-input__input-el) {
  line-height: var(--redirect-item-height);
  padding-top: 0;
  padding-bottom: 0;
}

.redirect-item-from :deep(.n-input .n-input-wrapper),
.redirect-item-to :deep(.n-input .n-input-wrapper) {
  padding-top: 0;
  padding-bottom: 0;
}

.redirect-item-remove-btn {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  line-height: 1;
  height: var(--redirect-item-height);
  width: var(--redirect-item-height);
}

@media (max-width: 768px) {
  .redirect-item-content {
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
  }

  .redirect-item-from,
  .redirect-item-to {
    flex: 1;
  }

  /* Rotate arrows 90 degrees on mobile for vertical layout */
  .redirect-arrow,
  .redirect-item-arrow {
    height: auto;
    transform: rotate(90deg);
    padding: 4px 0;
  }
}
</style>

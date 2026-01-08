<script setup lang="ts">
/**
 * Runtime Management Modal Component
 * Manages MCP service runtime environments (Node.js, Python, Bun, Deno, etc.)
 */
import { mcpSkillsApi, type RuntimeInfo } from "@/api/mcp-skills";
import { RefreshOutline } from "@vicons/ionicons5";
import {
  NButton,
  NIcon,
  NInput,
  NModal,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  NText,
  useMessage,
} from "naive-ui";
import { ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

// Props
const props = defineProps<{
  show: boolean;
}>();

// Emits
const emit = defineEmits<{
  (e: "update:show", value: boolean): void;
}>();

// Runtime state
const runtimes = ref<RuntimeInfo[]>([]);
const loadingRuntimes = ref(false);
const installingRuntimes = ref<Set<string>>(new Set());
const uninstallingRuntimes = ref<Set<string>>(new Set());
const upgradingRuntimes = ref<Set<string>>(new Set());

// Proxy settings
const runtimeUseProxy = ref(false);
const runtimeProxyUrl = ref("");

// Custom package installation state
const customPackageName = ref("");
const customInstallCommand = ref("");
const customRuntimeType = ref("nodejs");
const installingCustomPackage = ref(false);

// Runtime type options for custom package
const runtimeTypeOptions = [
  { label: "Node.js - npm", value: "nodejs" },
  { label: "Python - uv/pip", value: "python" },
  { label: "Bun", value: "bun" },
  { label: "Deno", value: "deno" },
  { label: "Custom", value: "custom" },
];

// Load runtime status
async function loadRuntimes() {
  loadingRuntimes.value = true;
  try {
    runtimes.value = await mcpSkillsApi.getRuntimes();
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    loadingRuntimes.value = false;
  }
}

// Install a runtime
async function installRuntime(runtimeType: string) {
  if (installingRuntimes.value.has(runtimeType)) {
    return;
  }
  installingRuntimes.value.add(runtimeType);
  try {
    await mcpSkillsApi.installRuntime(runtimeType, runtimeUseProxy.value, runtimeProxyUrl.value);
    message.success(t("mcpSkills.runtimeInstalled_success", { name: runtimeType }));
    await loadRuntimes();
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    installingRuntimes.value.delete(runtimeType);
  }
}

// Uninstall a runtime
async function uninstallRuntime(runtimeType: string) {
  if (uninstallingRuntimes.value.has(runtimeType)) {
    return;
  }
  uninstallingRuntimes.value.add(runtimeType);
  try {
    await mcpSkillsApi.uninstallRuntime(runtimeType);
    message.success(t("mcpSkills.runtimeUninstalled_success", { name: runtimeType }));
    await loadRuntimes();
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    uninstallingRuntimes.value.delete(runtimeType);
  }
}

// Upgrade a runtime
async function upgradeRuntime(runtimeType: string) {
  if (upgradingRuntimes.value.has(runtimeType)) {
    return;
  }
  upgradingRuntimes.value.add(runtimeType);
  try {
    await mcpSkillsApi.upgradeRuntime(runtimeType, runtimeUseProxy.value, runtimeProxyUrl.value);
    message.success(t("mcpSkills.runtimeUpgraded_success", { name: runtimeType }));
    await loadRuntimes();
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    upgradingRuntimes.value.delete(runtimeType);
  }
}

// Install custom package
async function installCustomPackage() {
  if (!customPackageName.value || !customInstallCommand.value) {
    message.warning(t("mcpSkills.customPackageRequired"));
    return;
  }
  if (installingCustomPackage.value) {
    return;
  }
  installingCustomPackage.value = true;
  try {
    await mcpSkillsApi.installCustomPackage(
      customPackageName.value,
      customInstallCommand.value,
      customRuntimeType.value,
      runtimeUseProxy.value,
      runtimeProxyUrl.value
    );
    message.success(
      t("mcpSkills.customPackageInstalled_success", { name: customPackageName.value })
    );
    // Clear form
    customPackageName.value = "";
    customInstallCommand.value = "";
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    installingCustomPackage.value = false;
  }
}

// Close modal
function closeModal() {
  emit("update:show", false);
}

// Expose loadRuntimes for parent component to call when opening modal
defineExpose({ loadRuntimes });
</script>

<template>
  <n-modal
    :show="props.show"
    preset="card"
    :title="t('mcpSkills.runtimeManagement')"
    style="width: 600px"
    @update:show="emit('update:show', $event)"
  >
    <div style="display: flex; flex-direction: column; gap: 16px">
      <n-text depth="3" style="font-size: 12px">
        {{ t("mcpSkills.runtimeManagementHint") }}
      </n-text>

      <!-- Proxy Settings -->
      <div class="proxy-settings">
        <div style="display: flex; align-items: center; gap: 8px">
          <n-switch v-model:value="runtimeUseProxy" size="small" />
          <n-text>{{ t("mcpSkills.runtimeUseProxy") }}</n-text>
        </div>
        <n-input
          v-if="runtimeUseProxy"
          v-model:value="runtimeProxyUrl"
          :placeholder="t('mcpSkills.runtimeProxyUrlPlaceholder')"
          size="small"
        />
      </div>

      <!-- Loading State -->
      <div v-if="loadingRuntimes" style="text-align: center; padding: 20px">
        <n-text depth="3">{{ t("common.loading") }}</n-text>
      </div>

      <!-- Runtime List -->
      <div v-else class="runtime-list">
        <div v-for="runtime in runtimes" :key="runtime.type" class="runtime-item">
          <div class="runtime-info">
            <div class="runtime-header">
              <n-text strong><span v-text="runtime.name" /></n-text>
              <n-tag v-if="runtime.installed" size="small" type="success">
                {{ t("mcpSkills.runtimeInstalled") }}
              </n-tag>
              <n-tag v-else size="small" type="warning">
                {{ t("mcpSkills.runtimeNotInstalled") }}
              </n-tag>
              <n-tag v-if="runtime.is_host_only" size="small" type="info">
                {{ t("mcpSkills.runtimeHostOnly") }}
              </n-tag>
            </div>
            <n-text depth="3" style="font-size: 12px">
              <span v-text="runtime.install_hint || ''" />
            </n-text>
            <n-text v-if="runtime.version" depth="2" style="font-size: 12px">
              <span v-text="t('mcpSkills.runtimeVersion')" />
              :
              <span v-text="runtime.version" />
            </n-text>
            <n-text
              v-if="runtime.in_container && runtime.is_host_only && !runtime.installed"
              type="warning"
              style="font-size: 12px"
            >
              {{ t("mcpSkills.runtimeDockerWarning") }}
            </n-text>
          </div>
          <div class="runtime-actions">
            <template v-if="runtime.can_install">
              <n-button
                v-if="!runtime.installed"
                size="small"
                type="primary"
                :loading="installingRuntimes.has(runtime.type)"
                @click="installRuntime(runtime.type)"
              >
                {{ t("mcpSkills.runtimeInstall") }}
              </n-button>
              <template v-else>
                <n-button
                  size="small"
                  :loading="upgradingRuntimes.has(runtime.type)"
                  @click="upgradeRuntime(runtime.type)"
                >
                  {{ t("mcpSkills.runtimeUpgrade") }}
                </n-button>
                <n-button
                  size="small"
                  type="error"
                  :loading="uninstallingRuntimes.has(runtime.type)"
                  @click="uninstallRuntime(runtime.type)"
                >
                  {{ t("mcpSkills.runtimeUninstall") }}
                </n-button>
              </template>
            </template>
            <n-text v-else depth="3" style="font-size: 12px">
              {{ t("mcpSkills.runtimeCannotInstall") }}
            </n-text>
          </div>
        </div>
      </div>

      <!-- Container Note -->
      <n-text v-if="runtimes.some(r => r.in_container)" type="info" style="font-size: 12px">
        {{ t("mcpSkills.runtimeContainerNote") }}
      </n-text>

      <!-- Custom Package Installation -->
      <div class="custom-package-section">
        <n-text strong style="font-size: 14px">{{ t("mcpSkills.customPackageInstall") }}</n-text>
        <n-text depth="3" style="font-size: 12px">{{ t("mcpSkills.customPackageHint") }}</n-text>
        <div style="display: flex; gap: 8px; flex-wrap: wrap">
          <n-input
            v-model:value="customPackageName"
            :placeholder="t('mcpSkills.customPackageNamePlaceholder')"
            size="small"
            style="flex: 1; min-width: 120px"
          />
          <n-select
            v-model:value="customRuntimeType"
            :options="runtimeTypeOptions"
            size="small"
            style="width: 140px"
          />
        </div>
        <n-input
          v-model:value="customInstallCommand"
          :placeholder="t('mcpSkills.customInstallCommandPlaceholder')"
          size="small"
        />
        <n-button
          type="primary"
          size="small"
          :loading="installingCustomPackage"
          :disabled="!customPackageName || !customInstallCommand"
          style="align-self: flex-start"
          @click="installCustomPackage"
        >
          {{ t("mcpSkills.customPackageInstallBtn") }}
        </n-button>
      </div>
    </div>

    <template #footer>
      <n-space justify="end">
        <n-button :loading="loadingRuntimes" @click="loadRuntimes">
          <template #icon><n-icon :component="RefreshOutline" /></template>
          {{ t("common.refresh") }}
        </n-button>
        <n-button @click="closeModal">{{ t("common.close") }}</n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<style scoped>
.proxy-settings {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  background: var(--n-color-modal);
  border-radius: 4px;
  border: 1px solid var(--n-border-color);
}

.runtime-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.runtime-item {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  padding: 12px;
  background: var(--n-color-modal);
  border-radius: 4px;
  border: 1px solid var(--n-border-color);
}

.runtime-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  flex: 1;
}

.runtime-header {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.runtime-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
  margin-left: 12px;
}

.custom-package-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  background: var(--n-color-modal);
  border-radius: 4px;
  border: 1px solid var(--n-border-color);
  margin-top: 8px;
}
</style>

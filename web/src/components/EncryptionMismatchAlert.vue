<script setup lang="ts">
import http from "@/utils/http";
import { NAlert, NButton, NCollapse, NCollapseItem } from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

// Encryption status response interface
interface EncryptionStatusResponse {
  has_mismatch: boolean;
  scenario_type: string;
  message: string;
  suggestion: string;
}

// Whether to show the alert
const showAlert = ref(false);

// Alert message info
const message = ref("");
const suggestion = ref("");
const scenarioType = ref("");

// Whether alert has been closed in this session
const isClosedThisSession = ref(false);

// Which detail sections are expanded
const showDetails = ref<string[]>([]);

// Whether alert should be shown
const shouldShow = computed(() => {
  return showAlert.value && !isClosedThisSession.value;
});

// i18n
const { t } = useI18n();

// Check encryption status
const checkEncryptionStatus = async () => {
  try {
    const response = await http.get<EncryptionStatusResponse>("/dashboard/encryption-status");
    if (response.data.has_mismatch) {
      showAlert.value = true;
      scenarioType.value = response.data.scenario_type;
      message.value = response.data.message;
      suggestion.value = response.data.suggestion;
    }
  } catch (error) {
    console.error("Failed to check encryption status:", error);
  }
};

// Close alert (current session only)
const handleClose = () => {
  isClosedThisSession.value = true;
};

// Open documentation
const openDocs = () => {
  window.open("https://www.gpt-load.com/docs/configuration/security", "_blank");
};

// Check status when component is mounted
onMounted(() => {
  checkEncryptionStatus();
});
</script>

<template>
  <n-alert
    v-if="shouldShow"
    type="error"
    :show-icon="false"
    closable
    @close="handleClose"
    style="margin-bottom: 16px"
  >
    <template #header>
      <strong>{{ t("encryptionAlert.title") }}</strong>
    </template>

    <div>
      <div style="margin-bottom: 16px; font-size: 14px; line-height: 1.5">
        {{ message }}
      </div>

      <n-collapse v-model:expanded-names="showDetails" style="margin-bottom: 12px">
        <n-collapse-item name="solution" :title="t('encryptionAlert.viewSolution')">
          <div
            class="solution-content"
            style="padding: 16px; border-radius: 6px; font-size: 13px; line-height: 1.6"
          >
            <!-- Scenario A: ENCRYPTION_KEY configured but data is not encrypted -->
            <template v-if="scenarioType === 'data_not_encrypted'">
              <p style="margin: 0 0 8px 0">
                1. {{ t("encryptionAlert.scenario.dataNotEncrypted.step1") }}
              </p>
              <p style="margin: 0 0 8px 0">
                2. {{ t("encryptionAlert.scenario.dataNotEncrypted.step2") }}
              </p>
              <pre
                style="
                  margin: 8px 0;
                  padding: 10px;
                  border-radius: 4px;
                  overflow-x: auto;
                  font-family: monospace;
                  font-size: 12px;
                "
              >
docker compose run --rm gpt-load migrate-keys --to "your-encryption-key"</pre
              >
              <p style="margin: 8px 0 0 0">
                3. {{ t("encryptionAlert.scenario.dataNotEncrypted.step3") }}
              </p>
            </template>

            <!-- Scenario C: encryption key mismatch -->
            <template v-else-if="scenarioType === 'key_mismatch'">
              <div style="margin-bottom: 16px">
                <strong style="color: var(--primary-color)">
                  {{ t("encryptionAlert.scenario.keyMismatch.solution1Title") }}
                </strong>
                <p style="margin: 8px 0 4px 0">
                  1. {{ t("encryptionAlert.scenario.keyMismatch.solution1Step1") }}
                </p>
                <pre
                  style="
                    margin: 4px 0 8px 0;
                    padding: 10px;
                    border-radius: 4px;
                    overflow-x: auto;
                    font-family: monospace;
                    font-size: 12px;
                  "
                >
ENCRYPTION_KEY=your-correct-encryption-key</pre
                >
                <p style="margin: 4px 0">
                  2. {{ t("encryptionAlert.scenario.keyMismatch.solution1Step2") }}
                </p>
              </div>

              <div>
                <strong style="color: var(--warning-color)">
                  {{ t("encryptionAlert.scenario.keyMismatch.solution2Title") }}
                </strong>
                <p style="margin: 0 0 8px 0">
                  1. {{ t("encryptionAlert.scenario.keyMismatch.solution2Step1") }}
                </p>
                <p style="margin: 4px 0">
                  2. {{ t("encryptionAlert.scenario.keyMismatch.solution2Step2") }}
                </p>
                <pre
                  style="
                    margin: 4px 0 8px 0;
                    padding: 10px;
                    border-radius: 4px;
                    overflow-x: auto;
                    font-family: monospace;
                    font-size: 12px;
                  "
                >
docker compose run --rm gpt-load migrate-keys --from "old-key" --to "new-key"</pre
                >
                <p style="margin: 4px 0">
                  3. {{ t("encryptionAlert.scenario.keyMismatch.solution2Step3") }}
                </p>
                <p style="margin: 4px 0">
                  4. {{ t("encryptionAlert.scenario.keyMismatch.solution2Step4") }}
                </p>
              </div>
            </template>

            <!-- Scenario B: data encrypted but ENCRYPTION_KEY not configured -->
            <template v-else-if="scenarioType === 'key_not_configured'">
              <div style="margin-bottom: 16px">
                <strong style="color: var(--primary-color)">
                  {{ t("encryptionAlert.scenario.keyNotConfigured.solution1Title") }}
                </strong>
                <p style="margin: 8px 0 4px 0">
                  1. {{ t("encryptionAlert.scenario.keyNotConfigured.solution1Step1") }}
                </p>
                <pre
                  style="
                    margin: 4px 0 8px 0;
                    padding: 10px;
                    border-radius: 4px;
                    overflow-x: auto;
                    font-family: monospace;
                    font-size: 12px;
                  "
                >
ENCRYPTION_KEY=your-original-encryption-key</pre
                >
                <p style="margin: 4px 0">
                  2. {{ t("encryptionAlert.scenario.keyNotConfigured.solution1Step2") }}
                </p>
              </div>

              <div>
                <strong style="color: var(--warning-color)">
                  {{ t("encryptionAlert.scenario.keyNotConfigured.solution2Title") }}
                </strong>
                <p style="margin: 0 0 8px 0">
                  1. {{ t("encryptionAlert.scenario.keyNotConfigured.solution2Step1") }}
                </p>
                <p style="margin: 4px 0">
                  2. {{ t("encryptionAlert.scenario.keyNotConfigured.solution2Step2") }}
                </p>
                <pre
                  style="
                    margin: 4px 0 8px 0;
                    padding: 10px;
                    border-radius: 4px;
                    overflow-x: auto;
                    font-family: monospace;
                    font-size: 12px;
                  "
                >
docker compose run --rm gpt-load migrate-keys --from "old-key"</pre
                >
                <p style="margin: 4px 0">
                  3. {{ t("encryptionAlert.scenario.keyNotConfigured.solution2Step3") }}
                </p>
              </div>
            </template>
          </div>
        </n-collapse-item>
      </n-collapse>

      <n-button
        size="small"
        type="primary"
        :bordered="false"
        @click="openDocs"
        class="encryption-docs-btn"
      >
        {{ t("encryptionAlert.viewDocs") }}
      </n-button>
    </div>
  </n-alert>
</template>

<style scoped>
/* Solution content background */
.solution-content {
  background: #f7f9fc;
  border: 1px solid #e1e4e8;
}

/* Code blocks in light mode */
.solution-content pre {
  background: #f0f2f5;
  border: 1px solid #d6dae0;
}

/* Solution background in dark mode */
:root.dark .solution-content {
  background: #1a1a1a;
  border: 1px solid rgba(255, 255, 255, 0.1);
}

/* Code blocks in dark mode */
:root.dark .solution-content pre {
  background: #0d0d0d !important;
  border: 1px solid rgba(255, 255, 255, 0.08);
}

/* Button styles */
.encryption-docs-btn {
  font-weight: 600;
}

/* Button tweaks in dark mode */
:root.dark .encryption-docs-btn {
  background: #d32f2f !important;
  color: white !important;
  border: none !important;
}

:root.dark .encryption-docs-btn:hover {
  background: #b71c1c !important;
  color: white !important;
}

/* Button styles in light mode */
:root:not(.dark) .encryption-docs-btn {
  background: #d32f2f !important;
  color: white !important;
  border: none !important;
}

:root:not(.dark) .encryption-docs-btn:hover {
  background: #b71c1c !important;
  color: white !important;
}
</style>

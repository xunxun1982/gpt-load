<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import { NButton, NForm, NFormItem, NInput, NModal, useMessage } from "naive-ui";
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

interface Props {
  show: boolean;
  parentGroup: Group | null;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success", group: Group): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const loading = ref(false);
const formData = ref({
  name: "",
  display_name: "",
  description: "",
});

// Reset form when modal opens
watch(
  () => props.show,
  newVal => {
    if (newVal) {
      formData.value = {
        name: "",
        display_name: "",
        description: "",
      };
    }
  }
);

function handleClose() {
  emit("update:show", false);
}

async function handleSubmit() {
  if (!props.parentGroup?.id) {
    return;
  }

  loading.value = true;
  try {
    const newGroup = await keysApi.createChildGroup(props.parentGroup.id, {
      name: formData.value.name || undefined,
      display_name: formData.value.display_name || undefined,
      description: formData.value.description || undefined,
    });
    message.success(t("keys.childGroupCreated"));
    emit("success", newGroup);
    handleClose();
  } catch (error) {
    const errorMessage = error instanceof Error ? error.message : t("keys.childGroupCreateFailed");
    message.error(errorMessage);
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    :title="t('keys.createChildGroup')"
    style="width: 500px"
    :mask-closable="false"
    @update:show="handleClose"
  >
    <n-form label-placement="left" label-width="120px">
      <n-form-item :label="t('keys.parentGroupLabel')">
        <span class="parent-group-name">{{ parentGroup?.display_name || parentGroup?.name }}</span>
      </n-form-item>
      <n-form-item :label="t('keys.childGroupName')">
        <n-input v-model:value="formData.name" :placeholder="t('keys.childGroupNamePlaceholder')" />
      </n-form-item>
      <n-form-item :label="t('keys.childGroupDisplayName')">
        <n-input
          v-model:value="formData.display_name"
          :placeholder="t('keys.childGroupDisplayNamePlaceholder')"
        />
      </n-form-item>
      <n-form-item :label="t('keys.childGroupDescription')">
        <n-input
          v-model:value="formData.description"
          type="textarea"
          :placeholder="t('keys.childGroupDescriptionPlaceholder')"
          :rows="3"
        />
      </n-form-item>
    </n-form>
    <template #footer>
      <div class="modal-footer">
        <n-button @click="handleClose">{{ t("common.cancel") }}</n-button>
        <n-button type="primary" :loading="loading" @click="handleSubmit">
          {{ t("common.create") }}
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

.parent-group-name {
  font-weight: 600;
  color: var(--primary-color);
}
</style>

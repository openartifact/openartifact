<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Create / edit drawer of a cleanup policy, shared by the repository and the project policy lists. -->
<template>
  <AdminDialog
    drawer
    max-width="max-w-md"
    :open="open"
    :eyebrow="editingName === null ? t('cleanup.newLabel') : t('cleanup.editTitle')"
    :title="editingName === null ? t('cleanup.addTitle') : `${t('cleanup.editTitle')} · ${editingName}`"
    @close="emit('close')"
  >
    <div class="space-y-5">
      <CleanupPolicyForm v-model="form" />
      <CleanupLivePreview v-if="previewRepo" :status="preview.status.value" :result="preview.result.value" :error="preview.error.value" />
    </div>
    <template #footer>
      <AppButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton :disabled="!cleanupFormValid(form)" :loading="saving" @click="emit('save')">
        {{ editingName === null ? t('cleanup.create') : t('common.save') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import CleanupLivePreview from './CleanupLivePreview.vue'
import CleanupPolicyForm from './CleanupPolicyForm.vue'
import { cleanupFormValid, type CleanupPolicyForm as FormState } from '../lib/cleanupPolicy'
import { useCleanupLivePreview } from '../composables/useCleanupLivePreview'
import { t } from '../i18n'

const props = defineProps<{
  open: boolean
  /** Name of the policy being edited; `null` when creating. */
  editingName: string | null
  saving: boolean
  /** Repository the policy belongs to; when set the editor shows a live preview of the policy. */
  previewRepo?: string
}>()

const form = defineModel<FormState>({ required: true })

const preview = useCleanupLivePreview(
  computed(() => props.previewRepo ?? ''),
  form,
  computed(() => props.open && !!props.previewRepo),
)

const emit = defineEmits<{ close: []; save: [] }>()
</script>

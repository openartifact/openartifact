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
    <CleanupPolicyForm v-model="form" />
    <template #footer>
      <AppButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton :disabled="!cleanupFormValid(form)" :loading="saving" @click="emit('save')">
        {{ editingName === null ? t('cleanup.create') : t('common.save') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import CleanupPolicyForm from './CleanupPolicyForm.vue'
import { cleanupFormValid, type CleanupPolicyForm as FormState } from '../lib/cleanupPolicy'
import { t } from '../i18n'

defineProps<{
  open: boolean
  /** Name of the policy being edited; `null` when creating. */
  editingName: string | null
  saving: boolean
}>()

const form = defineModel<FormState>({ required: true })

const emit = defineEmits<{ close: []; save: [] }>()
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ConfirmDialog
    :open="!!target"
    :title="t('adm.projects.deleteTitle')"
    :description="t('adm.projects.deleteHint')"
    :confirm-label="t('adm.projects.delete')"
    :confirm-disabled="!target || confirmName !== target.name"
    :busy="deleting"
    @confirm="confirm"
    @close="emit('close')"
  >
    <div v-if="target" class="space-y-4">
      <div class="rounded-lg border border-danger/30 bg-danger-soft p-3">
        <div class="flex items-start gap-2.5">
          <ExclamationCircleIcon class="w-4 h-4 text-danger mt-0.5 shrink-0" aria-hidden="true" />
          <div>
            <p class="text-xs font-semibold text-danger">{{ t('adm.projects.deleteWarnTitle') }}</p>
            <p class="text-caption text-danger mt-0.5 leading-relaxed">{{ t('adm.projects.deleteWarnBody') }}</p>
          </div>
        </div>
      </div>
      <div>
        <label for="project-delete-confirm" class="block text-xs font-semibold text-foreground mb-1.5">
          {{ t('adm.projects.typeToConfirm') }}
          <code class="font-mono text-danger bg-danger-soft px-1 py-0.5 rounded mx-1">{{ target.name }}</code>
        </label>
        <input
          id="project-delete-confirm"
          v-model="confirmName"
          type="text"
          :placeholder="target.name"
          class="w-full px-3 py-2 rounded-lg border bg-surface text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 transition-colors border-border-strong focus:ring-danger"
        />
      </div>
    </div>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ExclamationCircleIcon } from '@heroicons/vue/24/outline'
import { deleteProject, type Project } from '../api/projects'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'
import ConfirmDialog from './ConfirmDialog.vue'

const props = defineProps<{
  /** The project awaiting deletion; the dialog is open while set. */
  target: Project | null
}>()

const emit = defineEmits<{
  close: []
  deleted: []
}>()

const toasts = useToastStore()
const confirmName = ref('')
const deleting = ref(false)

// The project name must be typed again for every project.
watch(
  () => props.target,
  () => {
    confirmName.value = ''
  },
)

async function confirm(): Promise<void> {
  const project = props.target
  if (!project?.name || confirmName.value !== project.name) return
  deleting.value = true
  try {
    await deleteProject(project.name)
    toasts.success(t('adm.projects.deleted'))
    emit('close')
    emit('deleted')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.deleteFailed'))
  } finally {
    deleting.value = false
  }
}
</script>

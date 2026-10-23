<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog drawer max-width="max-w-xl" :open="open" :eyebrow="t('bundles.promote.eyebrow')" :title="t('bundles.promote.title')" @close="emit('close')">
    <p v-if="stages.length === 0" class="text-sm text-muted">{{ t('bundles.promote.noStages') }}</p>
    <form v-else id="bundle-promote-form" class="space-y-5" @submit.prevent="submit">
      <FormField :label="t('bundles.promote.stage')" id="promote-stage">
        <select id="promote-stage" v-model="toStage" required :class="INPUT_CLASS">
          <option v-for="s in stages" :key="s.name" :value="s.name">{{ s.name }}</option>
        </select>
      </FormField>
      <FormField
        :label="t('bundles.promote.repo')"
        id="promote-repo"
        :hint="fixedRepo ? t('bundles.promote.repoFixed') : undefined"
      >
        <select id="promote-repo" v-model="targetRepo" required :disabled="fixedRepo !== undefined" :class="INPUT_CLASS">
          <option value="" disabled>{{ t('bundles.items.chooseRepo') }}</option>
          <option v-for="r in repos" :key="r.id" :value="r.name">{{ r.name }} ({{ r.format }})</option>
        </select>
      </FormField>
      <FormField :label="`${t('bundles.promote.notes')} (${t('common.optional')})`" id="promote-notes">
        <textarea id="promote-notes" v-model="notes" rows="3" :class="INPUT_CLASS" />
      </FormField>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton
        v-if="stages.length > 0"
        variant="primary"
        size="sm"
        type="submit"
        form="bundle-promote-form"
        :loading="busy"
      >
        {{ t('bundles.promote.submit') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import { ApiError } from '../api/client'
import { promoteBundle, type Bundle, type PipelineStage } from '../api/bundles'
import type { Repository } from '../api/repositories'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{
  open: boolean
  bundle: Bundle
  /** Stages after the bundle's current one. */
  stages: PipelineStage[]
  repos: Repository[]
}>()
const emit = defineEmits<{ close: []; promoted: [] }>()

const toasts = useToastStore()
const toStage = ref('')
const targetRepo = ref('')
const notes = ref('')
const busy = ref(false)

/** Name of the repository the chosen stage insists on, when it has one. */
const fixedRepo = computed(() => {
  const id = props.stages.find((s) => s.name === toStage.value)?.repo_id
  return id ? props.repos.find((r) => r.id === id)?.name : undefined
})

watch(fixedRepo, (name) => {
  if (name) targetRepo.value = name
})

watch(
  () => props.open,
  (open) => {
    if (!open) return
    toStage.value = props.stages[0]?.name ?? ''
    targetRepo.value = fixedRepo.value ?? ''
    notes.value = ''
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await promoteBundle(props.bundle.name, props.bundle.version, {
      to_stage: toStage.value,
      target_repo: targetRepo.value,
      notes: notes.value || undefined,
    })
    toasts.success(t('bundles.promote.done'))
    emit('promoted')
  } catch (err) {
    if (err instanceof ApiError && err.status === 501) toasts.error(t('bundles.promote.unsupported'))
    else toasts.error(err instanceof Error ? err.message : t('bundles.promote.failed'))
  } finally {
    busy.value = false
  }
}
</script>

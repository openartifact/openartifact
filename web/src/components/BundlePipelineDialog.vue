<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog drawer max-width="max-w-xl" :open="open" :eyebrow="t('bundles.pipeline.eyebrow')" :title="t('bundles.pipeline.new')" @close="emit('close')">
    <form id="pipeline-create-form" class="space-y-5" @submit.prevent="submit">
      <FormField :label="t('bundles.pipeline.name')" id="pipeline-name" :hint="t('bundles.create.labelHint')">
        <input id="pipeline-name" v-model="name" required type="text" :class="INPUT_CLASS" autocomplete="off" />
      </FormField>
      <FormField :label="`${t('bundles.pipeline.description')} (${t('common.optional')})`" id="pipeline-description">
        <input id="pipeline-description" v-model="description" type="text" :class="INPUT_CLASS" />
      </FormField>

      <fieldset class="space-y-3">
        <legend class="text-sm font-medium text-foreground mb-1">{{ t('bundles.pipeline.stages') }}</legend>
        <div v-for="(stage, i) in stages" :key="i" class="flex items-end gap-2" data-testid="pipeline-stage">
          <div class="flex-1 min-w-0">
            <label :for="`stage-name-${i}`" class="block text-xs text-muted mb-1">{{ t('bundles.pipeline.stageName') }}</label>
            <input :id="`stage-name-${i}`" v-model="stage.name" required type="text" :class="INPUT_CLASS" autocomplete="off" />
          </div>
          <div class="flex-1 min-w-0">
            <label :for="`stage-repo-${i}`" class="block text-xs text-muted mb-1">{{ t('bundles.pipeline.stageRepo') }}</label>
            <select :id="`stage-repo-${i}`" v-model="stage.repo_id" :class="INPUT_CLASS">
              <option value="">{{ t('bundles.pipeline.anyRepo') }}</option>
              <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}</option>
            </select>
          </div>
          <AppButton
            variant="secondary"
            size="sm"
            :disabled="stages.length === 1"
            :aria-label="`${t('bundles.pipeline.removeStage')} ${i + 1}`"
            @click="stages.splice(i, 1)"
          >
            <XMarkIcon class="w-4 h-4" aria-hidden="true" />
          </AppButton>
        </div>
        <AppButton variant="secondary" size="sm" @click="stages.push({ name: '', repo_id: '' })">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('bundles.pipeline.addStage') }}
        </AppButton>
      </fieldset>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="pipeline-create-form" :loading="busy">
        {{ t('common.create') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { PlusIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import { createPipeline, type Pipeline } from '../api/bundles'
import type { Repository } from '../api/repositories'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ open: boolean; repos: Repository[] }>()
const emit = defineEmits<{ close: []; created: [pipeline: Pipeline] }>()

const toasts = useToastStore()
const name = ref('')
const description = ref('')
const stages = ref<{ name: string; repo_id: string }[]>([{ name: '', repo_id: '' }])
const busy = ref(false)

watch(
  () => props.open,
  (open) => {
    if (!open) return
    name.value = ''
    description.value = ''
    stages.value = [{ name: '', repo_id: '' }]
  },
)

async function submit(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    const pipeline = await createPipeline({
      name: name.value,
      description: description.value || undefined,
      stages: stages.value.map((s) => (s.repo_id ? { name: s.name, repo_id: s.repo_id } : { name: s.name })),
    })
    toasts.success(t('bundles.pipeline.done'))
    emit('created', pipeline)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.pipeline.failed'))
  } finally {
    busy.value = false
  }
}
</script>

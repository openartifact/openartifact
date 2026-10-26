<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Quick create: a draft with a name, a version, an optional description and an optional pipeline. -->
<template>
  <Drawer :open="open" :title="t('bundles.create')" :eyebrow="t('bundles.create.eyebrow')" :busy="busy" @close="emit('close')">
    <form id="bundle-create-form" class="space-y-4" novalidate @submit.prevent="submit">
      <FormField :label="t('bundles.create.name')" id="bundle-name" :error="nameError" :hint="t('bundles.create.labelHint')">
        <input
          id="bundle-name"
          v-model="name"
          type="text"
          autocomplete="off"
          :aria-invalid="nameError ? 'true' : undefined"
          :aria-describedby="nameError ? 'bundle-name-error' : undefined"
          :class="[INPUT_CLASS, 'font-mono']"
        />
      </FormField>
      <FormField :label="t('bundles.create.version')" id="bundle-version" :error="versionError" :hint="t('bundles.create.labelHint')">
        <input
          id="bundle-version"
          v-model="version"
          type="text"
          autocomplete="off"
          :aria-invalid="versionError ? 'true' : undefined"
          :aria-describedby="versionError ? 'bundle-version-error' : undefined"
          :class="[INPUT_CLASS, 'font-mono']"
        />
      </FormField>
      <FormField :label="`${t('bundles.create.description')} (${t('common.optional')})`" id="bundle-description">
        <textarea id="bundle-description" v-model="description" rows="3" :class="INPUT_CLASS" />
      </FormField>
      <FormField
        :label="`${t('bundles.create.pipeline')} (${t('common.optional')})`"
        id="bundle-pipeline"
        :hint="pipelines.length === 0 ? t('bundles.create.noPipelines') : t('bundles.create.pipelineHint')"
      >
        <select id="bundle-pipeline" v-model="pipelineId" :class="INPUT_CLASS">
          <option value="">{{ t('bundles.create.noPipeline') }}</option>
          <option v-for="p in pipelines" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
      </FormField>
      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="bundle-create-form" :loading="busy">
        {{ t('bundles.create.action') }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import { createBundle, type Bundle, type Pipeline } from '../../api/bundles'
import { validateBundleLabel } from '../../lib/bundles'
import { INPUT_CLASS } from '../../lib/formClasses'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const props = defineProps<{ open: boolean; pipelines: Pipeline[] }>()
const emit = defineEmits<{ close: []; created: [bundle: Bundle] }>()

const toasts = useToastStore()
const name = ref('')
const version = ref('')
const description = ref('')
const pipelineId = ref('')
const busy = ref(false)
const submitted = ref(false)
const error = ref('')

const nameError = computed(() => (submitted.value && !validateBundleLabel(name.value) ? t('bundles.create.invalidLabel') : ''))
const versionError = computed(() => (submitted.value && !validateBundleLabel(version.value) ? t('bundles.create.invalidLabel') : ''))

// Every opening starts from an empty form.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    name.value = ''
    version.value = ''
    description.value = ''
    pipelineId.value = ''
    submitted.value = false
    error.value = ''
  },
)

async function submit(): Promise<void> {
  if (busy.value) return
  submitted.value = true
  if (!validateBundleLabel(name.value) || !validateBundleLabel(version.value)) return
  busy.value = true
  error.value = ''
  try {
    const bundle = await createBundle({
      name: name.value,
      version: version.value,
      description: description.value.trim() || undefined,
      pipeline_id: pipelineId.value || undefined,
    })
    toasts.success(t('bundles.create.done'))
    emit('created', bundle)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('bundles.create.failed')
  } finally {
    busy.value = false
  }
}
</script>

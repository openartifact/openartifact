<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog drawer max-width="max-w-xl" :open="open" :eyebrow="t('bundles.create.eyebrow')" :title="t('bundles.create')" @close="emit('close')">
    <form id="bundle-create-form" class="space-y-5" @submit.prevent="submit">
      <FormField :label="t('bundles.create.name')" id="bundle-name" :error="nameError" :hint="t('bundles.create.labelHint')">
        <input id="bundle-name" v-model="name" required type="text" :class="INPUT_CLASS" autocomplete="off" />
      </FormField>
      <FormField :label="t('bundles.create.version')" id="bundle-version" :error="versionError" :hint="t('bundles.create.labelHint')">
        <input id="bundle-version" v-model="version" required type="text" :class="INPUT_CLASS" autocomplete="off" />
      </FormField>
      <FormField :label="`${t('bundles.create.description')} (${t('common.optional')})`" id="bundle-description">
        <textarea id="bundle-description" v-model="description" rows="3" :class="INPUT_CLASS" />
      </FormField>
      <FormField :label="`${t('bundles.create.pipeline')} (${t('common.optional')})`" id="bundle-pipeline">
        <select id="bundle-pipeline" v-model="pipelineId" :class="INPUT_CLASS">
          <option value="">{{ t('bundles.create.noPipeline') }}</option>
          <option v-for="p in pipelines" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
      </FormField>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="bundle-create-form" :loading="busy">
        {{ t('common.create') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import { createBundle, type Bundle, type Pipeline } from '../api/bundles'
import { validateBundleLabel } from '../lib/bundles'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ open: boolean; pipelines: Pipeline[] }>()
const emit = defineEmits<{ close: []; created: [bundle: Bundle] }>()

const toasts = useToastStore()
const name = ref('')
const version = ref('')
const description = ref('')
const pipelineId = ref('')
const busy = ref(false)
const submitted = ref(false)

const nameError = computed(() => (submitted.value && !validateBundleLabel(name.value) ? t('bundles.create.invalidLabel') : ''))
const versionError = computed(() => (submitted.value && !validateBundleLabel(version.value) ? t('bundles.create.invalidLabel') : ''))

watch(
  () => props.open,
  (open) => {
    if (!open) return
    name.value = ''
    version.value = ''
    description.value = ''
    pipelineId.value = ''
    submitted.value = false
  },
)

async function submit(): Promise<void> {
  if (busy.value) return
  submitted.value = true
  if (!validateBundleLabel(name.value) || !validateBundleLabel(version.value)) return
  busy.value = true
  try {
    const bundle = await createBundle({
      name: name.value,
      version: version.value,
      description: description.value || undefined,
      pipeline_id: pipelineId.value || undefined,
    })
    toasts.success(t('bundles.create.done'))
    emit('created', bundle)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('bundles.create.failed'))
  } finally {
    busy.value = false
  }
}
</script>

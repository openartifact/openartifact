<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Quick edit of the custom properties of one file: names are unique, removing a row deletes the property on save. -->
<template>
  <Drawer :open="open" :title="t('artifact.props.edit')" :eyebrow="label" :busy="saving" @close="emit('close')">
    <form id="artifact-props-form" class="space-y-3" novalidate @submit.prevent="save">
      <p class="text-sm text-muted">{{ t('artifact.props.editHint') }}</p>
      <div v-for="(row, i) in rows" :key="row.id" class="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-2">
        <input
          v-model="row.key"
          :aria-label="tp('artifact.props.nameOf', { n: String(i + 1) })"
          spellcheck="false"
          :class="[INPUT_CLASS, 'font-mono max-md:h-11']"
        />
        <input
          v-model="row.value"
          :aria-label="tp('artifact.props.valueOf', { n: String(i + 1) })"
          spellcheck="false"
          :class="[INPUT_CLASS, 'font-mono max-md:h-11']"
        />
        <button
          type="button"
          class="inline-flex h-9 w-9 items-center justify-center rounded text-muted hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11 max-md:w-11"
          :aria-label="tp('artifact.props.remove', { n: String(i + 1) })"
          @click="rows.splice(i, 1)"
        >
          <XMarkIcon class="h-4 w-4" aria-hidden="true" />
        </button>
      </div>
      <AppButton variant="secondary" size="sm" @click="addRow">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />{{ t('artifact.props.add') }}
      </AppButton>
      <p v-if="error" role="alert" class="text-sm text-danger">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="artifact-props-form" :loading="saving">{{ t('artifact.props.save') }}</AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { PlusIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import Drawer from '../ui/Drawer.vue'
import { deleteArtifactProperty, setArtifactProperties, type ArtifactProperties } from '../../api/artifactProperties'
import { INPUT_CLASS } from '../../lib/formClasses'
import { duplicateName, propertyChanges, propertyRows } from '../../lib/artifactProperties'
import { t, tp } from '../../i18n/en'

const props = defineProps<{
  open: boolean
  repo: string
  artifactId: string
  /** Names the edited file in the drawer header. */
  label: string
  properties: ArtifactProperties
}>()

const emit = defineEmits<{ close: []; saved: [properties: ArtifactProperties] }>()

let nextId = 0
const rows = ref<Array<{ id: number; key: string; value: string }>>([])
const saving = ref(false)
const error = ref('')

function addRow(): void {
  rows.value.push({ id: nextId++, key: '', value: '' })
}

// The form restarts from the stored properties every time it opens.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    rows.value = propertyRows(props.properties).map((r) => ({ ...r, id: nextId++ }))
    error.value = ''
  },
  { immediate: true },
)

async function save(): Promise<void> {
  const dup = duplicateName(rows.value)
  if (dup !== undefined) {
    error.value = tp('artifact.props.duplicate', { name: dup })
    return
  }
  const { set, remove } = propertyChanges(props.properties, rows.value)
  saving.value = true
  error.value = ''
  try {
    if (Object.keys(set).length > 0) await setArtifactProperties(props.repo, props.artifactId, set)
    for (const key of remove) await deleteArtifactProperty(props.repo, props.artifactId, key)
    const next: ArtifactProperties = { ...props.properties, ...set }
    for (const key of remove) delete next[key]
    emit('saved', next)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('artifact.props.saveFailed')
  } finally {
    saving.value = false
  }
}
</script>

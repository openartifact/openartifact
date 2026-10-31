<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  A card listing what a service account holds (a role, a group, a project) with a select to add one more.
  The caller does the requests: it gets `add` with the chosen option and `remove` with a row id.
-->
<template>
  <CardSection :title="title" :description="description">
    <p v-if="error" role="alert" class="px-5 py-3 text-sm text-danger">{{ error }}</p>
    <p v-else-if="loading && rows.length === 0" class="px-5 py-4 text-sm text-muted" role="status">{{ t('common.loading') }}</p>
    <p v-else-if="rows.length === 0" class="px-5 py-4 text-sm text-muted" data-testid="access-empty">{{ emptyText }}</p>
    <ul v-else class="divide-y divide-border">
      <li v-for="row in rows" :key="row.id" class="flex flex-wrap items-center gap-x-3 gap-y-1 px-5 py-2.5" :data-testid="`access-row-${row.id}`">
        <div class="min-w-0 flex-1">
          <router-link v-if="row.to" :to="row.to" class="block truncate font-mono text-ui text-primary hover:underline" :title="row.label">{{ row.label }}</router-link>
          <span v-else class="block truncate font-mono text-ui" :title="row.label">{{ row.label }}</span>
          <span v-if="row.hint" class="block truncate text-xs text-muted" :title="row.hint">{{ row.hint }}</span>
        </div>
        <slot name="row-extra" :row="row" />
        <RowActions :actions="[{ key: 'remove', label: removeLabel, icon: XMarkIcon, tone: 'danger' }]" :subject="row.label" @action="emit('remove', row.id)" />
      </li>
    </ul>

    <form v-if="options.length > 0" class="flex flex-wrap items-center gap-2 border-t border-border px-5 py-3" @submit.prevent="add">
      <select v-model="pick" :aria-label="addLabel" :class="[INPUT_CLASS, 'min-w-0 flex-1 basis-48']">
        <option value="" disabled>{{ addLabel }}</option>
        <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select>
      <slot name="add-extra" />
      <AppButton type="submit" variant="secondary" size="sm" class="max-md:h-11" :disabled="!pick" :loading="busy">{{ t('common.add') }}</AppButton>
    </form>
    <p v-else-if="!loading && !error && noOptionsText" class="border-t border-border px-5 py-3 text-xs text-muted">{{ noOptionsText }}</p>
  </CardSection>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { XMarkIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import CardSection from '../ui/CardSection.vue'
import RowActions from '../ui/RowActions.vue'
import { INPUT_CLASS } from '../../lib/formClasses'
import { t } from '../../i18n'

export interface AccessRow {
  id: string
  label: string
  hint?: string
  to?: string
}

defineProps<{
  title: string
  description: string
  rows: AccessRow[]
  /** What can still be added; the form is hidden when empty. */
  options: { value: string; label: string }[]
  addLabel: string
  removeLabel: string
  emptyText: string
  noOptionsText?: string
  loading?: boolean
  error?: string
  busy?: boolean
}>()
const emit = defineEmits<{ add: [value: string]; remove: [id: string] }>()

const pick = ref('')

function add(): void {
  if (!pick.value) return
  emit('add', pick.value)
  pick.value = ''
}
</script>

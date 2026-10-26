<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="overflow-x-auto" role="region" :aria-label="t('shell.dataTable')">
    <table class="min-w-full divide-y divide-border bg-surface">
      <thead class="bg-surface-hover">
        <tr>
          <th
            v-for="col in columns"
            :key="col.key"
            scope="col"
            class="px-4 py-3 text-left text-xs font-medium text-muted"
            :class="col.sortable ? 'cursor-pointer select-none hover:text-foreground' : ''"
            :aria-sort="col.sortable ? 'none' : undefined"
            @click="col.sortable ? emit('sort', col.key) : undefined"
          >
            {{ col.label }}
          </th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        <tr v-for="row in rows" :key="String(row[rowKey ?? 'id'])" class="hover:bg-surface-hover">
          <td v-for="col in columns" :key="col.key" class="px-4 py-3 text-sm text-foreground">
            <slot :name="`cell-${col.key}`" :row="row" :value="row[col.key]">
              {{ row[col.key] }}
            </slot>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { t } from '../i18n'

export interface Column {
  key: string
  label: string
  sortable?: boolean
}

withDefaults(
  defineProps<{
    columns: Column[]
    rows: Record<string, unknown>[]
    rowKey?: string
  }>(),
  { rowKey: 'id' },
)

const emit = defineEmits<{
  sort: [key: string]
}>()
</script>

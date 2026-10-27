<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <TableScroller>
  <table class="w-full">
    <thead>
      <tr class="bg-surface-hover/30">
        <th scope="col" class="text-left text-caption font-semibold text-muted px-5 py-2.5">
          {{ t('adm.storage.column.name') }}
        </th>
        <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
          {{ t('adm.storage.column.type') }}
        </th>
        <th scope="col" data-drop-order="1" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
          {{ t('adm.storage.column.location') }}
        </th>
        <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
          {{ t('adm.storage.column.status') }}
        </th>
        <th scope="col" data-drop-order="2" class="text-right text-caption font-semibold text-muted px-3 py-2.5">
          {{ t('sx.usage.stored') }}
        </th>
        <th scope="col" class="text-right text-caption font-semibold text-muted px-5 py-2.5" :class="ACTIONS_HEAD">
          {{ t('adm.storage.column.actions') }}
        </th>
      </tr>
    </thead>
    <tbody class="divide-y divide-border">
      <!-- Primary store: set by the server configuration, so read-only. -->
      <tr v-if="primary" class="group/row hover:bg-surface-hover/30 transition-colors" data-testid="storage-primary-row">
        <td class="px-5 py-3.5" :class="GROW_CELL">
          <div class="flex items-center gap-2.5">
            <div
              class="w-8 h-8 rounded-md flex items-center justify-center shrink-0"
              :style="{ background: typeColorBg(primary.kind) }"
            >
              <component :is="typeIcon(primary.kind)" class="w-4 h-4" :style="{ color: typeColor(primary.kind) }" />
            </div>
            <div class="flex items-center gap-1.5 min-w-0">
              <span class="font-mono text-ui font-medium text-foreground truncate" :title="primary.name">{{ primary.name }}</span>
              <span
                class="inline-flex items-center px-1.5 py-0.5 rounded-md bg-primary-soft text-primary text-micro font-semibold shrink-0"
              >
                {{ t('adm.storage.configBadge') }}
              </span>
            </div>
          </div>
        </td>
        <td class="px-3 py-3.5">
          <span class="font-mono text-caption font-medium text-foreground whitespace-nowrap">
            {{ typeMeta(primary.kind)?.label ?? primary.kind }}
          </span>
        </td>
        <td class="px-3 py-3.5 max-w-[16rem]">
          <span class="font-mono text-xs text-foreground/80 truncate block" :title="primaryLocationSummary(primary)">
            {{ primaryLocationSummary(primary) }}
          </span>
        </td>
        <td class="px-3 py-3.5">
          <div>
            <span
              class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium"
              :class="statusBadgeClass(primary.status)"
            >
              <span class="w-1.5 h-1.5 rounded-full" :class="statusDotClass(primary.status)" />
              {{ statusLabel(primary.status) }}
            </span>
            <p
              v-if="primary.status === 'unreachable' && primary.status_message"
              class="text-micro text-danger mt-0.5 max-w-64 break-words"
              data-testid="storage-primary-message"
            >
              {{ primary.status_message }}
            </p>
            <p v-if="primary.last_checked_at" class="text-micro text-muted font-mono mt-0.5 whitespace-nowrap">
              {{ tp('adm.storage.checkedAgo', { time: timeAgo(primary.last_checked_at) }) }}
            </p>
          </div>
        </td>
        <td class="px-3 py-3.5 text-right" data-testid="backend-usage-primary">
          <template v-if="primary.usage">
            <p class="text-sm tabular-nums text-foreground">{{ formatBytes(primary.usage.size_bytes) }}</p>
            <p class="text-xs tabular-nums text-muted">{{ tp('sx.usage.blobs', { n: formatNumber(primary.usage.blob_count) }) }}</p>
          </template>
          <span v-else class="text-xs text-muted" :title="t('sx.usage.none')">—</span>
        </td>
        <td class="px-5 py-3.5" :class="ACTIONS_CELL">
          <RowActions :actions="primaryActions" :subject="primary.name" @action="emit('test-primary')" />
        </td>
      </tr>
      <tr
        v-for="backend in items"
        :key="backend.id"
        class="group/row hover:bg-surface-hover/30 transition-colors"
      >
        <!-- Name cell -->
        <td class="px-5 py-3.5" :class="GROW_CELL">
          <div class="flex items-center gap-2.5">
            <div
              class="w-8 h-8 rounded-md flex items-center justify-center shrink-0"
              :style="{ background: typeColorBg(backend.kind) }"
            >
              <component
                :is="typeIcon(backend.kind)"
                class="w-4 h-4"
                :style="{ color: typeColor(backend.kind) }"
              />
            </div>
            <div class="flex items-center gap-1.5 min-w-0">
              <span class="font-mono text-ui font-medium text-foreground truncate" :title="backend.name">{{ backend.name }}</span>
            </div>
          </div>
        </td>

        <!-- Type chip cell -->
        <td class="px-3 py-3.5">
          <span
            class="inline-flex items-center pl-1.5 pr-2 py-1 rounded-md bg-surface-hover/60 border border-border relative overflow-hidden"
          >
            <span
              class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-md"
              :style="{ background: typeColor(backend.kind) }"
            />
            <span class="pl-1.5 flex items-center gap-1.5">
              <component
                :is="typeIcon(backend.kind)"
                class="w-3.5 h-3.5"
                :style="{ color: typeColor(backend.kind) }"
              />
              <span class="font-mono text-caption font-medium text-foreground whitespace-nowrap">
                {{ typeMeta(backend.kind)?.label ?? backend.kind }}
              </span>
            </span>
          </span>
        </td>

        <!-- Location cell -->
        <td class="px-3 py-3.5 max-w-[16rem]">
          <span class="font-mono text-xs text-foreground/80 truncate block" :title="locationSummary(backend)">
            {{ locationSummary(backend) }}
          </span>
        </td>

        <!-- Status cell -->
        <td class="px-3 py-3.5">
          <div>
            <span
              class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium"
              :class="statusBadgeClass(backend.status)"
            >
              <span class="w-1.5 h-1.5 rounded-full" :class="statusDotClass(backend.status)" />
              {{ statusLabel(backend.status) }}
            </span>
            <p v-if="backend.last_checked_at" class="text-micro text-muted font-mono mt-0.5 whitespace-nowrap">
              {{ tp('adm.storage.checkedAgo', { time: timeAgo(backend.last_checked_at) }) }}
            </p>
          </div>
        </td>

        <!-- Usage cell -->
        <td class="px-3 py-3.5 text-right" :data-testid="`backend-usage-${backend.name}`">
          <template v-if="backend.usage">
            <p class="text-sm tabular-nums text-foreground">{{ formatBytes(backend.usage.size_bytes) }}</p>
            <p class="text-xs tabular-nums text-muted">{{ tp('sx.usage.blobs', { n: formatNumber(backend.usage.blob_count) }) }}</p>
          </template>
          <span v-else class="text-xs text-muted" :title="t('sx.usage.none')">—</span>
        </td>

        <!-- Actions cell -->
        <td class="px-5 py-3.5" :class="ACTIONS_CELL">
          <RowActions :actions="backendActions(backend)" :subject="backend.name" @action="(key) => onBackendAction(key, backend)" />
        </td>
      </tr>
    </tbody>
  </table>
  </TableScroller>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { BoltIcon, PencilSquareIcon, TrashIcon } from '@heroicons/vue/20/solid'
import RowActions, { type RowAction } from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import type { StoragePrimary } from '../api/storage'
import { t, tp } from '../i18n'
import { timeAgo } from '../lib/datetime'
import { formatNumber } from '../lib/intl'
import { formatBytes } from '../lib/quota'
import {
  locationSummary,
  primaryLocationSummary,
  statusBadgeClass,
  statusDotClass,
  statusLabel,
  typeColor,
  typeColorBg,
  typeIcon,
  typeMeta,
  type StorageBackend,
} from '../lib/storageBackends'

const props = defineProps<{
  /** Primary store configured on the server; shown first, read-only. */
  primary?: StoragePrimary | null
  items: StorageBackend[]
  /** Name of the backend whose connection test is running, if any. */
  testingOne: string | null
  testingPrimary?: boolean
}>()

const emit = defineEmits<{
  (e: 'test', name: string): void
  (e: 'edit', backend: StorageBackend): void
  (e: 'test-primary'): void
  (e: 'delete', backend: StorageBackend): void
}>()

const primaryActions = computed<RowAction[]>(() => [
  { key: 'test', label: t('adm.storage.testConnection'), icon: BoltIcon, loading: props.testingPrimary },
])

function backendActions(backend: StorageBackend): RowAction[] {
  return [
    { key: 'test', label: t('adm.storage.testConnection'), icon: BoltIcon, loading: props.testingOne === backend.name },
    { key: 'edit', label: t('common.edit'), icon: PencilSquareIcon },
    { key: 'delete', label: t('adm.storage.deleteAction'), icon: TrashIcon, tone: 'danger' },
  ]
}

function onBackendAction(key: string, backend: StorageBackend): void {
  if (key === 'test') emit('test', backend.name)
  else if (key === 'edit') emit('edit', backend)
  else if (key === 'delete') emit('delete', backend)
}
</script>

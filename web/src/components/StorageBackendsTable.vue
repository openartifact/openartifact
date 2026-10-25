<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <table class="w-full">
    <thead>
      <tr class="bg-surface-hover/30">
        <th scope="col" class="text-left text-caption font-semibold text-muted px-5 py-2.5">
          {{ t('adm.storage.column.name') }}
        </th>
        <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
          {{ t('adm.storage.column.type') }}
        </th>
        <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
          {{ t('adm.storage.column.location') }}
        </th>
        <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
          {{ t('adm.storage.column.status') }}
        </th>
        <th scope="col" class="text-right text-caption font-semibold text-muted px-5 py-2.5">
          {{ t('adm.storage.column.actions') }}
        </th>
      </tr>
    </thead>
    <tbody class="divide-y divide-border">
      <tr
        v-for="backend in items"
        :key="backend.id"
        class="hover:bg-surface-hover/30 transition-colors"
      >
        <!-- Name cell -->
        <td class="px-5 py-3.5">
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
              <span class="font-mono text-ui font-medium text-foreground">{{ backend.name }}</span>
              <span
                v-if="backend.is_default"
                class="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded-md bg-primary-soft text-primary text-micro font-semibold shrink-0"
              >
                <StarIcon class="w-2.5 h-2.5" />
                {{ t('adm.storage.defaultBadge') }}
              </span>
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
        <td class="px-3 py-3.5 max-w-[260px]">
          <span class="font-mono text-xs text-foreground/80 truncate block max-w-[260px]">
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

        <!-- Actions cell -->
        <td class="px-5 py-3.5">
          <div class="flex items-center gap-0.5 justify-end">
            <!-- Test -->
            <button
              type="button"
              :title="t('adm.storage.testConnection')"
              :aria-label="tp('adm.storage.pair', { label: t('adm.storage.testConnection'), value: backend.name })"
              :disabled="testingOne === backend.name"
              class="p-1.5 rounded-md text-muted hover:text-primary hover:bg-primary/10 transition-colors disabled:opacity-40"
              @click="emit('test', backend.name)"
            >
              <ArrowPathIcon v-if="testingOne === backend.name" class="w-4 h-4 animate-spin" />
              <BoltIcon v-else class="w-4 h-4" />
            </button>
            <!-- Edit -->
            <button
              type="button"
              :title="t('common.edit')"
              :aria-label="tp('adm.storage.pair', { label: t('common.edit'), value: backend.name })"
              class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
              @click="emit('edit', backend)"
            >
              <PencilIcon class="w-4 h-4" />
            </button>
            <!-- Set default -->
            <button
              v-if="!backend.is_default"
              type="button"
              :title="t('adm.storage.setDefault')"
              :aria-label="tp('adm.storage.pair', { label: t('adm.storage.setDefault'), value: backend.name })"
              class="p-1.5 rounded-md text-muted hover:text-warning hover:bg-warning/10 transition-colors"
              @click="emit('set-default', backend)"
            >
              <StarIcon class="w-4 h-4" />
            </button>
            <!-- Delete -->
            <button
              type="button"
              :title="t('adm.storage.deleteAction')"
              :aria-label="tp('adm.storage.pair', { label: t('adm.storage.deleteAction'), value: backend.name })"
              :disabled="backend.is_default"
              class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
              @click="emit('delete', backend)"
            >
              <TrashIcon class="w-4 h-4" />
            </button>
          </div>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<script setup lang="ts">
import { ArrowPathIcon, BoltIcon, PencilIcon, StarIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { t, tp } from '../i18n/en'
import { timeAgo } from '../lib/datetime'
import {
  locationSummary,
  statusBadgeClass,
  statusDotClass,
  statusLabel,
  typeColor,
  typeColorBg,
  typeIcon,
  typeMeta,
  type StorageBackend,
} from '../lib/storageBackends'

defineProps<{
  items: StorageBackend[]
  /** Name of the backend whose connection test is running, if any. */
  testingOne: string | null
}>()

const emit = defineEmits<{
  (e: 'test', name: string): void
  (e: 'edit', backend: StorageBackend): void
  (e: 'set-default', backend: StorageBackend): void
  (e: 'delete', backend: StorageBackend): void
}>()
</script>

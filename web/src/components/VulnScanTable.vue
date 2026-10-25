<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="bg-surface rounded-xl border border-border overflow-hidden">
    <div class="px-4 py-3 border-b border-border flex items-center justify-between">
      <h3 class="text-sm font-semibold tracking-tight">{{ t('sec.vuln.results') }}</h3>
      <span class="text-caption font-mono text-muted">{{ tp('sec.vuln.scanCount', { count: `${scans.length}${hasMore ? '+' : ''}` }) }}</span>
    </div>

    <div v-if="loading" class="py-12 flex flex-col items-center gap-3 text-muted" role="status">
      <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
      <span class="text-sm">{{ t('sec.common.loadingScans') }}</span>
    </div>

    <div v-else-if="loadError" class="py-12 text-center" role="alert">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <AppButton variant="secondary" size="sm" @click="emit('refresh')">
        {{ t('sec.common.retry') }}
      </AppButton>
    </div>

    <div v-else-if="scans.length === 0" data-testid="scans-empty">
      <EmptyState
        v-if="hasFilters"
        :title="t('sec.vuln.noMatchTitle')"
        :description="t('sec.vuln.noMatchDesc')"
      />
      <EmptyState
        v-else
        :title="t('sec.vuln.emptyTitle')"
        :description="t('sec.vuln.emptyDesc')"
      />
      <div v-if="hasFilters" class="flex justify-center pb-10 -mt-8">
        <AppButton variant="secondary" size="sm" data-testid="empty-clear-filters" @click="emit('clear-filters')">
          {{ t('sec.vuln.clearFilters') }}
        </AppButton>
      </div>
    </div>

    <div v-else class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="border-b border-border bg-surface-hover/40">
          <tr>
            <th v-for="col in COLUMNS" :key="col" scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t(col) }}</th>
          </tr>
        </thead>
        <tbody>
          <VulnScanTableRow
            v-for="scan in scans"
            :key="scan.id"
            :scan="scan"
            :quarantining="quarantining === scan.id"
            @details="emit('details', scan)"
            @quarantine="emit('quarantine', scan)"
            @delete="emit('delete', scan)"
          />
        </tbody>
      </table>
    </div>

    <div v-if="hasMore && !loading && !loadError" class="px-4 py-3 border-t border-border flex justify-center">
      <AppButton variant="secondary" size="sm" :loading="loadingMore" :disabled="loadingMore" data-testid="load-more" @click="emit('load-more')">
        {{ loadingMore ? t('sec.vuln.loadingMore') : t('sec.vuln.loadMore') }}
      </AppButton>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import EmptyState from './EmptyState.vue'
import VulnScanTableRow from './VulnScanTableRow.vue'
import type { VulnScanRow } from '../api/security'
import { t, tp } from '../i18n/en'

const COLUMNS = [
  'sec.vuln.col.repository',
  'sec.vuln.col.package',
  'sec.vuln.col.vulns',
  'sec.vuln.col.status',
  'sec.vuln.col.scanner',
  'sec.vuln.col.scanned',
  'sec.vuln.col.actions',
] as const

defineProps<{
  scans: VulnScanRow[]
  hasMore: boolean
  hasFilters: boolean
  loading: boolean
  loadingMore: boolean
  loadError: string
  /** Id of the scan whose quarantine toggle is in flight. */
  quarantining: string | null
}>()

const emit = defineEmits<{
  refresh: []
  'clear-filters': []
  'load-more': []
  details: [scan: VulnScanRow]
  quarantine: [scan: VulnScanRow]
  delete: [scan: VulnScanRow]
}>()
</script>

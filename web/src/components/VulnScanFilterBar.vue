<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex items-center gap-3 flex-wrap">
    <select
      :value="filters.repo"
      :aria-label="t('sec.vuln.filterRepo')"
      :class="selectClass"
      data-testid="filter-repo"
      @change="onSelect('repo', $event)"
    >
      <option value="">{{ t('sec.vuln.allRepos') }}</option>
      <option v-if="filters.repo && !repos.some((r) => r.name === filters.repo)" :value="filters.repo">{{ filters.repo }}</option>
      <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }}</option>
    </select>
    <select
      :value="filters.severity"
      :aria-label="t('sec.vuln.filterSeverity')"
      :class="selectClass"
      data-testid="filter-severity"
      @change="onSelect('severity', $event)"
    >
      <option value="">{{ t('sec.vuln.anySeverity') }}</option>
      <option v-for="s in VULN_SEVERITY_FILTERS" :key="s" :value="s">{{ tDynamic(`sec.vuln.atLeast.${s}`) }}</option>
    </select>
    <select
      :value="filters.status"
      :aria-label="t('sec.vuln.filterStatus')"
      :class="selectClass"
      data-testid="filter-status"
      @change="onSelect('status', $event)"
    >
      <option value="">{{ t('sec.vuln.anyStatus') }}</option>
      <option v-for="s in VULN_STATUS_FILTERS" :key="s" :value="s">{{ vulnScanLabel(s) }}</option>
    </select>
    <select
      :value="filters.scanner"
      :aria-label="t('sec.vuln.filterScanner')"
      :class="selectClass"
      data-testid="filter-scanner"
      @change="onSelect('scanner', $event)"
    >
      <option value="">{{ t('sec.vuln.anyScanner') }}</option>
      <option v-for="s in VULN_SCANNER_OPTIONS" :key="s.value" :value="s.value">{{ s.label }}</option>
    </select>
    <select
      :value="filters.quarantined"
      :aria-label="t('sec.vuln.filterQuarantine')"
      :class="selectClass"
      data-testid="filter-quarantined"
      @change="onSelect('quarantined', $event)"
    >
      <option value="">{{ t('sec.vuln.anyQuarantine') }}</option>
      <option value="true">{{ t('sec.vuln.onlyQuarantined') }}</option>
      <option value="false">{{ t('sec.vuln.notQuarantined') }}</option>
    </select>
    <AppButton v-if="hasFilters" variant="secondary" size="sm" data-testid="clear-filters" @click="emit('clear')">
      {{ t('sec.vuln.clearFilters') }}
    </AppButton>
    <button
      type="button"
      :aria-label="t('common.refresh')"
      class="flex items-center justify-center w-9 h-9 rounded-lg border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50"
      :disabled="loading"
      @click="emit('refresh')"
    >
      <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': loading }" aria-hidden="true" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import type { Repository } from '../api/repositories'
import { vulnScanLabel } from '../lib/scanStatus'
import { VULN_SCANNER_OPTIONS } from '../lib/vulnScanners'
import { VULN_SEVERITY_FILTERS, VULN_STATUS_FILTERS, type VulnFilterState } from '../lib/vulnScanFilters'
import { t, tDynamic } from '../i18n/en'

defineProps<{
  filters: VulnFilterState
  repos: Repository[]
  hasFilters: boolean
  loading: boolean
}>()

const emit = defineEmits<{
  select: [patch: Partial<VulnFilterState>]
  clear: []
  refresh: []
}>()

const selectClass =
  'h-9 px-3 rounded-lg bg-surface border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50'

function onSelect(key: keyof VulnFilterState, event: Event): void {
  emit('select', { [key]: (event.target as HTMLSelectElement).value } as Partial<VulnFilterState>)
}
</script>

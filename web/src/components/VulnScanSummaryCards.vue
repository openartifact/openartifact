<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section :aria-label="t('sec.vuln.summaryLabel')" class="space-y-3">
    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <div class="grid grid-cols-2 sm:grid-cols-5 gap-3">
      <div class="rounded-xl border border-border bg-surface p-4" data-testid="summary-total">
        <div class="text-micro font-semibold text-muted">{{ t('sec.vuln.totalScans') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums">{{ summary ? summary.total : '—' }}</div>
      </div>
      <button
        v-for="card in cards"
        :key="card.testId"
        type="button"
        :data-testid="card.testId"
        :aria-pressed="isActive(card)"
        class="rounded-xl border bg-surface p-4 text-left transition-colors hover:bg-surface-hover/40 focus:outline-none focus:ring-2 focus:ring-primary/50"
        :class="isActive(card) ? 'border-primary ring-1 ring-primary/50' : 'border-border'"
        @click="emit('filter', { [card.key]: isActive(card) ? '' : card.value } as Partial<VulnFilterState>)"
      >
        <div class="text-micro font-semibold text-muted">{{ card.label }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums" :class="card.tone">{{ card.count === undefined ? '—' : card.count }}</div>
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { VulnScanSummary } from '../api/security'
import { vulnScanLabel } from '../lib/scanStatus'
import type { VulnFilterState } from '../lib/vulnScanFilters'
import { t, tDynamic } from '../i18n/en'

type CardFilter =
  | { key: 'severity'; value: VulnFilterState['severity'] }
  | { key: 'status'; value: VulnFilterState['status'] }
  | { key: 'quarantined'; value: 'true' }
type SummaryCard = CardFilter & { testId: string; label: string; count: number | undefined; tone: string }

const props = defineProps<{
  /** The summary of the filtered repository; null while loading or after a failure. */
  summary: VulnScanSummary | null
  error: string
  filters: VulnFilterState
}>()

/** Clicking a card sets (or, when it is active, clears) the filter it stands for. */
const emit = defineEmits<{ filter: [patch: Partial<VulnFilterState>] }>()

const cards = computed<SummaryCard[]>(() => {
  const s = props.summary
  const severity = (value: 'critical' | 'high' | 'medium' | 'low', tone: string): SummaryCard => ({
    key: 'severity',
    value,
    testId: `summary-${value}`,
    label: tDynamic(`sec.vuln.atLeast.${value}`),
    count: s?.severity_at_least[value],
    tone,
  })
  const status = (value: 'completed' | 'partial' | 'failed' | 'scanning', tone: string): SummaryCard => ({
    key: 'status',
    value,
    testId: `summary-${value}`,
    label: vulnScanLabel(value),
    count: s?.[value],
    tone,
  })
  return [
    severity('critical', 'text-danger'),
    severity('high', 'text-warning'),
    severity('medium', 'text-severity-medium'),
    severity('low', ''),
    status('completed', 'text-success'),
    status('partial', 'text-warning'),
    status('failed', 'text-danger'),
    status('scanning', ''),
    { key: 'quarantined', value: 'true', testId: 'summary-quarantined', label: t('sec.vuln.quarantined'), count: s?.quarantined, tone: 'text-danger' },
  ]
})

function isActive(card: CardFilter): boolean {
  return props.filters[card.key] === card.value
}
</script>

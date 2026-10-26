<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The numbers over the list. Every cell is a filter: it sets severity (or the
  quarantined status) and pressing it again clears it. The last cell is coverage
  and does not filter.
-->
<template>
  <section :aria-label="t('security.summary.label')" data-testid="security-summary">
    <p v-if="error" class="mb-2 text-sm text-danger" role="alert">{{ error }}</p>
    <div class="grid grid-cols-2 overflow-hidden rounded-lg border border-border bg-surface sm:grid-cols-3 lg:grid-cols-6">
      <button
        v-for="cell in CELLS"
        :key="cell"
        type="button"
        class="flex min-h-11 flex-col items-start gap-1 border-b border-r border-border px-4 py-3 text-left transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary"
        :class="isCellActive(filters, cell) ? 'bg-surface-hover ring-2 ring-inset ring-primary' : ''"
        :aria-pressed="isCellActive(filters, cell)"
        :data-testid="`strip-${cell}`"
        @click="emit('select', cell)"
      >
        <span class="text-xs text-muted">{{ cell === 'quarantined' ? t('security.status.quarantined') : t(`ui.severity.${cell}`) }}</span>
        <span class="text-2xl font-semibold tabular-nums" :class="CLASSES[cell]">{{ totals ? totals[cell] : '–' }}</span>
      </button>
      <div class="flex flex-col items-start gap-1 border-b border-border px-4 py-3" data-testid="strip-coverage">
        <span class="text-xs text-muted">{{ t('security.summary.coverage') }}</span>
        <span class="text-2xl font-semibold tabular-nums">
          {{ summary ? `${summary.scanned_repositories}/${summary.total_repositories}` : '–' }}
        </span>
        <span v-if="summary" class="text-caption text-muted">
          <template v-if="summary.last_scan_at">
            {{ t('security.summary.lastScan') }}
            <time :datetime="summary.last_scan_at" :title="formatDateTime(summary.last_scan_at)">{{ timeAgo(summary.last_scan_at) }}</time>
          </template>
          <template v-else>{{ t('security.summary.neverScanned') }}</template>
        </span>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { SecuritySummary } from '../../api/security'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import { isCellActive, summaryTotals, type InboxFilters, type SeverityFilter } from '../../lib/securityFindings'
import { t } from '../../i18n'

type Cell = SeverityFilter | 'quarantined'

const props = defineProps<{
  summary: SecuritySummary | null
  filters: Pick<InboxFilters, 'type' | 'severity' | 'status'>
  error?: string
}>()

const emit = defineEmits<{ select: [cell: Cell] }>()

const CELLS: readonly Cell[] = ['critical', 'high', 'medium', 'low', 'quarantined']
const CLASSES: Record<Cell, string> = {
  critical: 'text-danger',
  high: 'text-warning',
  medium: 'text-severity-medium',
  low: 'text-info',
  quarantined: 'text-foreground',
}

const totals = computed(() => (props.summary ? summaryTotals(props.summary, props.filters.type) : null))
</script>

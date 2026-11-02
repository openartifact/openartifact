<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Storage > Recovery: the state of the row journal that lets `openartifact rebuild` re-create the index from storage. -->
<template>
  <div class="space-y-5">
    <SectionRefresh :loading="loading" @refresh="reload" />

    <div v-if="loading && !status" role="status" class="rounded-lg border border-border bg-surface px-4 py-5 text-sm text-muted">
      {{ t('common.loading') }}
    </div>

    <div v-else-if="!status" class="rounded-lg border border-border bg-surface" data-testid="recovery-error">
      <StateBlock variant="error" :title="t('sx.recovery.error.title')" :hint="error">
        <template #action>
          <AppButton variant="secondary" @click="reload">{{ t('ui.state.retry') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <template v-else>
      <p class="flex flex-wrap items-center gap-2 text-sm" role="status" data-testid="recovery-summary">
        <StatusBadge :label="t(`sx.recovery.health.${health}`)" :variant="BADGE[health]" />
        <span class="text-muted">{{ t(`sx.recovery.health.${health}.hint`) }}</span>
      </p>

      <div v-if="health === 'off'" class="rounded-lg border border-border bg-surface" data-testid="recovery-off">
        <StateBlock variant="off" :title="t('sx.recovery.off.title')" :hint="t('sx.recovery.off.body')" command="recovery.journal.enabled: true" />
      </div>

      <template v-else>
        <StatTiles class="grid-cols-2 lg:grid-cols-5" :tiles="tiles" data-testid="recovery-tiles" />

        <div
          v-if="status.last_error"
          class="rounded-lg border border-danger bg-danger-soft p-4 text-sm"
          role="alert"
          data-testid="recovery-last-error"
        >
          <p class="flex items-center gap-2 font-medium text-foreground">
            <ExclamationTriangleIcon class="h-4 w-4 shrink-0 text-danger" aria-hidden="true" />
            {{ t('sx.recovery.lastError') }}
            <RelativeTime v-if="status.last_error_at" :value="status.last_error_at" class="font-normal text-muted" />
          </p>
          <p class="mt-1 break-words font-mono text-xs text-foreground">{{ status.last_error }}</p>
          <p class="mt-1 text-muted">{{ t('sx.recovery.lastError.hint') }}</p>
        </div>

        <section class="rounded-lg border border-border bg-surface p-4" data-testid="recovery-details">
          <h2 class="text-sm font-semibold text-foreground">{{ t('sx.recovery.details') }}</h2>
          <dl class="mt-3 grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
            <div>
              <dt class="text-muted">{{ t('sx.recovery.lastDrain') }}</dt>
              <dd class="text-foreground"><RelativeTime :value="status.last_drain_at" :fallback="t('sx.recovery.never')" /></dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('sx.recovery.lastReconcile') }}</dt>
              <dd class="text-foreground"><RelativeTime :value="status.last_reconcile_at" :fallback="t('sx.recovery.never')" /></dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('sx.recovery.schemaVersion') }}</dt>
              <dd class="font-mono text-foreground">{{ status.schema_version ?? '—' }}</dd>
            </div>
          </dl>
        </section>
      </template>

      <section class="rounded-lg border border-border bg-surface p-4" data-testid="recovery-rebuild">
        <h2 class="text-sm font-semibold text-foreground">{{ t('sx.recovery.rebuild') }}</h2>
        <p class="mt-1 text-sm text-muted">{{ t('sx.recovery.rebuild.body') }}</p>
        <ol class="mt-3 space-y-3 text-sm">
          <li v-for="step in steps" :key="step.label">
            <p class="text-foreground">{{ step.label }}</p>
            <div class="mt-1 flex items-start gap-2">
              <code class="min-w-0 flex-1 break-all rounded-md bg-surface-hover px-3 py-2 font-mono text-xs text-foreground">{{ step.command }}</code>
              <CopyButton :text="step.command" :label="step.label" />
            </div>
          </li>
        </ol>
        <p class="mt-3 text-xs text-muted">{{ t('sx.recovery.rebuild.note') }}</p>
        <DocsLink class="mt-2" :url="docsUrl" />
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/20/solid'
import { ArchiveBoxIcon, ClockIcon, ExclamationCircleIcon, QueueListIcon, TableCellsIcon } from '@heroicons/vue/24/outline'
import { getRecoveryStatus } from '../../../api/recovery'
import AppButton from '../../../components/AppButton.vue'
import DocsLink from '../../../components/DocsLink.vue'
import SectionRefresh from '../../../components/SectionRefresh.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import CopyButton from '../../../components/ui/CopyButton.vue'
import RelativeTime from '../../../components/ui/RelativeTime.vue'
import StateBlock from '../../../components/ui/StateBlock.vue'
import StatTiles, { type StatTile } from '../../../components/ui/StatTiles.vue'
import { useDocsLink } from '../../../composables/useDocsLink'
import { useLoadable } from '../../../composables/useLoadable'
import { formatDuration } from '../../../lib/duration'
import { formatNumber } from '../../../lib/intl'
import { backlogCapped, recoveryHealth, REBUILD_DRY_RUN, REBUILD_RUN, type RecoveryHealth } from '../../../lib/recoveryStatus'
import { t, tp } from '../../../i18n'
import type { RecoveryStatus } from '../../../api/recovery'

const BADGE: Record<RecoveryHealth, 'success' | 'warning' | 'danger' | 'neutral'> = {
  healthy: 'success',
  'catching-up': 'warning',
  failing: 'danger',
  off: 'neutral',
}

const { data: status, loading, error, reload } = useLoadable<RecoveryStatus | null>(getRecoveryStatus, null)
const docsUrl = useDocsLink('settings-recovery')

const health = computed<RecoveryHealth>(() => (status.value ? recoveryHealth(status.value) : 'off'))

const tiles = computed<StatTile[]>(() => {
  const s = status.value
  if (!s) return []
  const backlog = `${backlogCapped(s) ? '≥ ' : ''}${formatNumber(s.backlog)}`
  return [
    { label: t('sx.recovery.backlog'), value: backlog, icon: QueueListIcon, tone: (s.backlog ?? 0) > 0 ? 'warning' : 'success' },
    { label: t('sx.recovery.lag'), value: (s.backlog ?? 0) > 0 ? formatDuration(s.oldest_dirty_age_seconds, { maxUnits: 2 }) : '—', icon: ClockIcon, tone: 'primary' },
    { label: t('sx.recovery.retrying'), value: formatNumber(s.retrying_rows), icon: ExclamationCircleIcon, tone: (s.retrying_rows ?? 0) > 0 ? 'danger' : 'success' },
    { label: t('sx.recovery.sidecars'), value: formatNumber(s.sidecars_tracked), icon: ArchiveBoxIcon, tone: 'primary' },
    {
      label: t('sx.recovery.tables'),
      value: tp('sx.recovery.tablesOf', { done: formatNumber(s.tables_reconciled), total: formatNumber(s.tables_total) }),
      icon: TableCellsIcon,
      tone: (s.tables_reconciled ?? 0) >= (s.tables_total ?? 0) ? 'success' : 'warning',
    },
  ]
})

const steps = computed(() => [
  { label: t('sx.recovery.step.dryRun'), command: REBUILD_DRY_RUN },
  { label: t('sx.recovery.step.run'), command: REBUILD_RUN },
])
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Storage > Quotas: every repository and project that has a limit, most used first. -->
<template>
  <div>
    <p
      v-if="all.length > 0"
      class="mb-4 flex items-center gap-2 text-sm"
      :class="counts.over > 0 ? 'text-danger' : counts.near > 0 ? 'text-warning' : 'text-muted'"
      role="status"
      data-testid="quota-summary"
    >
      <ExclamationTriangleIcon v-if="counts.over + counts.near > 0" class="h-4 w-4 shrink-0" aria-hidden="true" />
      <CheckCircleIcon v-else class="h-4 w-4 shrink-0" aria-hidden="true" />
      {{ summary }}
    </p>

    <FilterBar
      :search="list.text.q"
      :search-label="t('sx.quotas.search')"
      :placeholder="t('sx.quotas.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear"
    >
      <SegmentedControl
        :options="scopeOptions"
        :model-value="list.filters.value.scope"
        :label="t('sx.quotas.scope')"
        data-testid="scope-filter"
        @update:model-value="list.setFilter('scope', $event as QuotaScope)"
      />
    </FilterBar>

    <p v-if="truncated" class="mb-3 text-sm text-muted" data-testid="quota-truncated">
      {{ tp('sx.quotas.truncated', { shown: String(all.length), total: String(total) }) }}
    </p>

    <ListTable
      :columns="columns"
      :caption="t('sx.quotas.table')"
      :query="list"
      :row-key="quotaKey"
      :empty-title="t('sx.quotas.empty')"
      :empty-hint="t('sx.quotas.emptyHint')"
    >
      <template #cell-name="{ row }">
        <div class="min-w-0">
          <span class="font-mono font-medium text-foreground">{{ (row as QuotaOverviewItem).name }}</span>
          <p class="text-xs text-muted">
            {{ t(`sx.quotas.type.${(row as QuotaOverviewItem).scope}`) }}
            <template v-if="(row as QuotaOverviewItem).project"> · {{ (row as QuotaOverviewItem).project }}</template>
          </p>
        </div>
      </template>

      <template #cell-used="{ row }">
        <span class="tabular-nums">{{ formatBytes((row as QuotaOverviewItem).used_bytes) }}</span>
        <p v-if="(row as QuotaOverviewItem).artifact_count !== undefined" class="text-xs text-muted tabular-nums">
          {{ tp('sx.quotas.artifacts', { n: String((row as QuotaOverviewItem).artifact_count) }) }}
        </p>
      </template>

      <template #cell-limit="{ row }">
        <span v-if="(row as QuotaOverviewItem).limit_bytes" class="tabular-nums">{{ formatBytes((row as QuotaOverviewItem).limit_bytes!) }}</span>
        <span v-else class="text-muted">{{ t('sx.quotas.noSizeLimit') }}</span>
        <p v-if="(row as QuotaOverviewItem).max_artifact_count" class="text-xs text-muted tabular-nums">
          {{ tp('sx.quotas.maxArtifacts', { max: String((row as QuotaOverviewItem).max_artifact_count) }) }}
        </p>
      </template>

      <template #cell-usage="{ row }">
        <div class="flex min-w-40 items-center gap-2" :data-state="quotaState(row as QuotaOverviewItem)">
          <div
            class="h-1.5 w-24 shrink-0 overflow-hidden rounded-full bg-surface-hover"
            role="progressbar"
            :aria-label="t('sx.quotas.col.usage')"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="Math.min(Math.round((row as QuotaOverviewItem).usage_percent), 100)"
          >
            <div
              class="h-full rounded-full"
              :class="BAR[quotaState(row as QuotaOverviewItem)]"
              :style="{ width: `${Math.min((row as QuotaOverviewItem).usage_percent, 100)}%` }"
            />
          </div>
          <span class="w-10 text-right text-xs tabular-nums text-foreground">{{ tp('sx.quotas.percent', { n: String(Math.round((row as QuotaOverviewItem).usage_percent)) }) }}</span>
          <StatusBadge :label="t(`sx.quotas.state.${quotaState(row as QuotaOverviewItem)}`)" :variant="BADGE[quotaState(row as QuotaOverviewItem)]" />
        </div>
      </template>

      <template #cell-actions="{ row }">
        <AppButton
          variant="ghost"
          size="sm"
          class="max-md:min-h-11"
          :to="openTarget(row as QuotaOverviewItem)"
          :aria-label="openLabel(row as QuotaOverviewItem)"
        >{{ t('sx.open') }}</AppButton>
      </template>
    </ListTable>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { CheckCircleIcon, ExclamationTriangleIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../../components/AppButton.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import SegmentedControl from '../../../components/ui/SegmentedControl.vue'
import { listQuotas, type QuotaOverviewItem } from '../../../api/quota'
import { useLocalList } from '../../../composables/useLocalList'
import { enumParam, strParam } from '../../../composables/useUrlState'
import { formatBytes } from '../../../lib/quota'
import { compareQuotas, quotaCounts, quotaKey, quotaState, type QuotaState } from '../../../lib/quotaOverview'
import { t, tp } from '../../../i18n/en'

const SCOPES = ['all', 'repository', 'project', 'near'] as const
type QuotaScope = (typeof SCOPES)[number]

const BAR: Record<QuotaState, string> = { over: 'bg-danger', near: 'bg-warning', ok: 'bg-success' }
const BADGE: Record<QuotaState, 'danger' | 'warning' | 'success'> = { over: 'danger', near: 'warning', ok: 'success' }

const total = ref(0)

const { list, all } = useLocalList({
  filters: { q: strParam(), scope: enumParam(SCOPES, 'all') },
  debounce: ['q'],
  sorts: ['usage', 'name', 'used'],
  defaultDir: 'desc',
  limit: 25,
  async load() {
    const res = await listQuotas()
    total.value = res.total
    return res.quotas
  },
  match(row, f) {
    if (f.q && !row.name.toLowerCase().includes(f.q.trim().toLowerCase())) return false
    if (f.scope === 'near') return quotaState(row) !== 'ok'
    return f.scope === 'all' || row.scope === f.scope
  },
  compare: compareQuotas,
})

const counts = computed(() => quotaCounts(all.value))
const truncated = computed(() => total.value > all.value.length)

const summary = computed(() =>
  counts.value.over + counts.value.near === 0
    ? tp('sx.quotas.summaryAllOk', { n: String(all.value.length) })
    : tp('sx.quotas.summary', { over: String(counts.value.over), near: String(counts.value.near), ok: String(counts.value.ok) }),
)

const scopeOptions = computed(() => SCOPES.map((s) => ({ value: s, label: t(`sx.quotas.scope.${s}`) })))

const columns: ListColumn[] = [
  { key: 'name', label: t('sx.quotas.col.name'), sortable: true },
  { key: 'used', label: t('sx.quotas.col.used'), sortable: true },
  { key: 'limit', label: t('sx.quotas.col.limit') },
  { key: 'usage', label: t('sx.quotas.col.usage'), sortable: true },
  { key: 'actions', label: t('sx.quotas.col.actions'), actions: true },
]

/** Where the limit is edited: the repository configuration or the project usage tab. */
function openTarget(q: QuotaOverviewItem): string {
  const name = encodeURIComponent(q.name)
  return q.scope === 'project' ? `/projects/${name}?tab=usage` : `/repos/${name}?tab=configuration`
}

function openLabel(q: QuotaOverviewItem): string {
  return tp(q.scope === 'project' ? 'sx.quotas.openProject' : 'sx.quotas.openRepo', { name: q.name })
}
</script>

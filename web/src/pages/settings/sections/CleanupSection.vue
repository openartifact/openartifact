<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Storage > Cleanup: every cleanup policy of the instance, gathered from the hosted repositories. -->
<template>
  <div>
    <p class="mb-4 text-sm text-muted" data-testid="cleanup-scope">{{ t('sx.cleanup.scope') }}</p>

    <p v-if="failed.length > 0" class="mb-4 text-sm text-warning" role="status" data-testid="cleanup-partial">
      {{ tp('sx.cleanup.partial', { names: failed.join(', ') }) }}
    </p>

    <FilterBar
      :search="list.text.q"
      :search-label="t('sx.cleanup.search')"
      :placeholder="t('sx.cleanup.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear"
    >
      <SegmentedControl
        :options="statusOptions"
        :model-value="list.filters.value.status"
        :label="t('sx.cleanup.status')"
        @update:model-value="list.setFilter('status', $event as CleanupStatus)"
      />
    </FilterBar>

    <ListTable
      :columns="columns"
      :caption="t('sx.cleanup.table')"
      :query="list"
      :row-key="rowKey"
      :empty-title="t('sx.cleanup.empty')"
      :empty-hint="t('sx.cleanup.emptyHint')"
    >
      <template #cell-policy="{ row }">
        <span class="font-medium text-foreground">{{ (row as CleanupOverviewRow).name }}</span>
      </template>

      <template #cell-repository="{ row }">
        <div class="flex flex-wrap items-center gap-2">
          <RouterLink
            :to="repoPath(row as CleanupOverviewRow)"
            class="font-mono text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          >{{ (row as CleanupOverviewRow).repository }}</RouterLink>
          <FormatBadge :format="(row as CleanupOverviewRow).format" size="sm" />
        </div>
      </template>

      <template #cell-rule="{ row }">
        <span class="text-muted">
          <template v-for="r in [row as CleanupOverviewRow]" :key="r.id">
            <span v-if="r.keep_count != null">
              {{ t('cleanup.keepLast') }} <span class="font-mono font-semibold text-foreground">{{ r.keep_count }}</span>
            </span>
            <span v-if="r.keep_count != null && r.keep_days != null"> · </span>
            <span v-if="r.keep_days != null">
              {{ t('cleanup.keepFor') }} <span class="font-mono font-semibold text-foreground">{{ r.keep_days }}</span> {{ t('cleanup.days') }}
            </span>
            <span v-if="r.keep_count == null && r.keep_days == null">{{ t('sx.cleanup.noRule') }}</span>
          </template>
        </span>
      </template>

      <template #cell-status="{ row }">
        <StatusBadge
          :label="(row as CleanupOverviewRow).enabled ? t('sx.cleanup.enabled') : t('sx.cleanup.disabled')"
          :variant="(row as CleanupOverviewRow).enabled ? 'success' : 'neutral'"
        />
      </template>

      <template #cell-lastRun="{ row }">
        <time
          v-if="(row as CleanupOverviewRow).last_run_at"
          :datetime="(row as CleanupOverviewRow).last_run_at"
          :title="formatDateTime((row as CleanupOverviewRow).last_run_at)"
          class="text-sm text-muted"
        >{{ timeAgo((row as CleanupOverviewRow).last_run_at) }}</time>
        <span v-else class="text-sm text-muted">{{ t('cleanup.never') }}</span>
      </template>

      <template #cell-actions="{ row }">
        <div class="flex flex-wrap items-center gap-1">
          <AppButton
            variant="ghost"
            size="sm"
            class="max-md:min-h-11"
            :loading="busy === `preview:${rowKey(row as CleanupOverviewRow)}`"
            :aria-label="tp('sx.cleanup.preview', { name: (row as CleanupOverviewRow).name })"
            @click="preview(row as CleanupOverviewRow)"
          >{{ t('cleanup.dryRun') }}</AppButton>
          <AppButton
            variant="ghost"
            size="sm"
            class="max-md:min-h-11"
            :loading="busy === `run:${rowKey(row as CleanupOverviewRow)}`"
            :aria-label="tp('sx.cleanup.run', { name: (row as CleanupOverviewRow).name })"
            @click="run(row as CleanupOverviewRow)"
          >{{ t('cleanup.run') }}</AppButton>
          <AppButton
            variant="ghost"
            size="sm"
            class="max-md:min-h-11"
            :to="repoPath(row as CleanupOverviewRow)"
            :aria-label="tp('sx.cleanup.edit', { name: (row as CleanupOverviewRow).name, repo: (row as CleanupOverviewRow).repository })"
          >{{ t('common.edit') }}</AppButton>
        </div>
      </template>
    </ListTable>

    <CleanupDryRunDialog
      :open="previewing !== null"
      :title="previewing?.name ?? ''"
      :repositories="previewing?.repositories ?? []"
      @close="previewing = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import AppButton from '../../../components/AppButton.vue'
import CleanupDryRunDialog from '../../../components/CleanupDryRunDialog.vue'
import FormatBadge from '../../../components/FormatBadge.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import SegmentedControl from '../../../components/ui/SegmentedControl.vue'
import { dryRunCleanupPolicy, runCleanupPolicy, type CleanupPreview } from '../../../api/cleanup'
import { useLocalList } from '../../../composables/useLocalList'
import { enumParam, strParam } from '../../../composables/useUrlState'
import { loadCleanupOverview, type CleanupOverviewRow } from '../../../lib/cleanupOverview'
import { formatDateTime, timeAgo } from '../../../lib/datetime'
import { useToastStore } from '../../../stores/toast'
import { t, tp } from '../../../i18n/en'

const STATUSES = ['all', 'enabled', 'disabled'] as const
type CleanupStatus = (typeof STATUSES)[number]

const toasts = useToastStore()
const failed = ref<string[]>([])
const busy = ref('')
const previewing = ref<{ name: string; repositories: CleanupPreview[] } | null>(null)

const { list, refresh } = useLocalList({
  filters: { q: strParam(), status: enumParam(STATUSES, 'all') },
  debounce: ['q'],
  sorts: ['policy', 'repository'],
  limit: 25,
  async load() {
    const res = await loadCleanupOverview()
    failed.value = res.failed
    return res.rows
  },
  match(row, f) {
    const q = f.q.trim().toLowerCase()
    if (q && !row.name.toLowerCase().includes(q) && !row.repository.toLowerCase().includes(q)) return false
    return f.status === 'all' || (f.status === 'enabled') === row.enabled
  },
  compare(a, b, sort) {
    return sort === 'repository' ? a.repository.localeCompare(b.repository) || a.name.localeCompare(b.name) : a.name.localeCompare(b.name)
  },
})

const statusOptions = computed(() => STATUSES.map((s) => ({ value: s, label: t(`sx.cleanup.status.${s}`) })))

const columns: ListColumn[] = [
  { key: 'policy', label: t('sx.cleanup.col.policy'), sortable: true },
  { key: 'repository', label: t('sx.cleanup.col.repository'), sortable: true },
  { key: 'rule', label: t('sx.cleanup.col.rule') },
  { key: 'status', label: t('sx.cleanup.col.status') },
  { key: 'lastRun', label: t('sx.cleanup.col.lastRun') },
  { key: 'actions', label: t('sx.cleanup.col.actions'), actions: true },
]

const rowKey = (row: CleanupOverviewRow): string => `${row.repository}:${row.id}`
/** Policies are edited on the repository's Cleanup tab. */
const repoPath = (row: CleanupOverviewRow): string => `/repos/${encodeURIComponent(row.repository)}?tab=cleanup`

async function preview(row: CleanupOverviewRow): Promise<void> {
  busy.value = `preview:${rowKey(row)}`
  try {
    previewing.value = { name: row.name, repositories: [await dryRunCleanupPolicy(row.repository, row.id)] }
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.dryRun.failed'))
  } finally {
    busy.value = ''
  }
}

async function run(row: CleanupOverviewRow): Promise<void> {
  busy.value = `run:${rowKey(row)}`
  try {
    await runCleanupPolicy(row.repository, row.id)
    toasts.success(tp('cleanup.runTriggered', { name: row.name }))
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.runFailed'))
  } finally {
    busy.value = ''
  }
}
</script>

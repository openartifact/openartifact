<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Packages of a repository, one row per package (versions rolled up), with the
  security state of the latest version and when it was last scanned.
-->
<template>
  <div>
    <FilterBar
      :search="list.text.q"
      :search-label="t('rl.packages.search')"
      :placeholder="t('rl.packages.search')"
      :active="list.filtered.value"
      @update:search="(v) => list.setFilter('q', v)"
      @clear="list.clear"
    >
      <button
        type="button"
        :aria-pressed="list.filters.value.flagged"
        class="inline-flex h-9 items-center rounded-lg border px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
        :class="list.filters.value.flagged ? 'border-primary bg-primary-soft text-primary' : 'border-border-strong bg-surface text-foreground hover:bg-surface-hover'"
        data-testid="chip-flagged"
        @click="list.setFilter('flagged', !list.filters.value.flagged)"
      >{{ t('rl.filter.flagged') }}</button>
    </FilterBar>

    <p v-if="repo.repo_type === 'proxy' && list.rows.value.length > 0" class="mb-3 text-xs text-muted">{{ t('repoDetail.proxy.cachedHint') }}</p>

    <ListTable
      :columns="columns"
      :caption="t('rl.packages.caption')"
      :query="list"
      :row-key="(r) => `${r.format}:${r.name}`"
      :empty-title="proxy ? t('repoDetail.proxy.emptyTitle') : t('rl.packages.empty')"
      :empty-hint="proxy ? t('repoDetail.proxy.emptyHint') : t('rl.packages.emptyHint')"
    >
      <template #cell-name="{ row }">
        <router-link :to="artifactPath(repo.name, row.name)" class="block truncate font-mono text-sm font-semibold text-foreground hover:text-primary" :title="row.name">{{ row.name }}</router-link>
      </template>
      <template #cell-latest="{ row }"><span class="font-mono text-xs text-muted">{{ row.latest_version || '—' }}</span></template>
      <template #cell-versions="{ row }"><span class="tabular-nums">{{ row.version_count }}</span></template>
      <template #cell-size="{ row }"><span class="tabular-nums">{{ formatBytesOrDash(row.size_bytes) }}</span></template>
      <template #cell-updated="{ row }"><RelativeTime :value="row.updated_at" class="text-xs text-muted" /></template>
      <template #cell-security="{ row }">
        <span v-if="hasFindings(row)" class="flex flex-wrap items-center gap-x-3 gap-y-0.5">
          <SeverityBadge v-for="s in SEVERITIES.filter((k) => row.security[k] > 0)" :key="s" :severity="s" :count="row.security[s]" />
        </span>
        <span v-else-if="row.scanned_at" class="inline-flex items-center gap-1.5 text-xs font-medium text-success">
          <CheckCircleIcon class="h-3.5 w-3.5" aria-hidden="true" />{{ t('rl.security.clean') }}
        </span>
        <span v-else class="text-xs text-muted">{{ t('rl.security.unscanned') }}</span>
      </template>
      <template #cell-scanned="{ row }">
        <RelativeTime :value="row.scanned_at" :fallback="t('rl.packages.neverScanned')" class="text-xs text-muted" />
      </template>
    </ListTable>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircleIcon } from '@heroicons/vue/20/solid'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import SeverityBadge from '../ui/SeverityBadge.vue'
import { listPackageGroups, type PackageGroup, type Repository } from '../../api/repositories'
import { useListQuery } from '../../composables/useListQuery'
import { boolParam, strParam } from '../../composables/useUrlState'
import { artifactPath } from '../../lib/artifactPath'
import { formatBytesOrDash } from '../../lib/quota'
import { t } from '../../i18n'

const props = defineProps<{ repo: Repository }>()

const SEVERITIES = ['critical', 'high', 'medium', 'low'] as const
const proxy = computed(() => props.repo.repo_type === 'proxy')

const list = useListQuery({
  filters: { q: strParam(), flagged: boolParam() },
  debounce: ['q'],
  limit: 20,
  async fetch(req) {
    const res = await listPackageGroups(props.repo.name, {
      page: req.page,
      limit: req.limit,
      q: req.filters.q || undefined,
      flagged: req.filters.flagged || undefined,
      sort: 'name',
    })
    return { rows: res.packages ?? [], total: res.total ?? 0 }
  },
})

const columns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('packages.column.name'), grow: true },
  { key: 'latest', label: t('rl.packages.latest') },
  { key: 'versions', label: t('rl.packages.versions'), align: 'end' },
  { key: 'size', label: t('packages.column.size'), align: 'end', dropOrder: 2 },
  { key: 'updated', label: t('rl.packages.updated'), dropOrder: 3 },
  { key: 'security', label: t('rl.column.security') },
  { key: 'scanned', label: t('rl.packages.lastScanned'), dropOrder: 1 },
])

const hasFindings = (p: PackageGroup): boolean => SEVERITIES.some((s) => p.security[s] > 0)
</script>

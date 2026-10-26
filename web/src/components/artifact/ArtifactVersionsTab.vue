<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section aria-labelledby="artifact-versions">
    <h2 id="artifact-versions" class="sr-only">{{ t('artifact.tab.versions') }}</h2>
    <FilterBar
      :search="list.text.q"
      :search-label="t('artifact.versions.filter')"
      :placeholder="t('artifact.versions.filter')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear()"
    />
    <ListTable :columns="columns" :query="list" row-key="version" :caption="tp('artifact.versions.caption', { name: ctx.pkgName.value })" :empty-title="t('artifact.versions.empty')">
      <template #cell-version="{ row }">
        <div class="flex flex-wrap items-center gap-1.5">
          <router-link :to="versionTo(row)" class="break-all font-mono text-primary hover:underline">{{ row.version }}</router-link>
          <span v-if="row.version === ctx.entry.value?.version" class="rounded-full border border-border px-2 py-0.5 text-micro text-muted">{{ t('artifact.badge.current') }}</span>
          <span v-if="row.latest" class="rounded-full border border-info/30 bg-info-soft px-2 py-0.5 text-micro text-info">{{ t('artifact.badge.latest') }}</span>
          <span v-if="row.prerelease" class="rounded-full border border-border px-2 py-0.5 text-micro text-muted">{{ t('artifact.badge.prerelease') }}</span>
        </div>
      </template>
      <template #cell-created="{ row }"><RelativeTime :value="row.createdAt" /></template>
      <template #cell-size="{ row }"><span class="font-mono text-xs tabular-nums">{{ row.size > 0 ? formatBytes(row.size) : '—' }}</span></template>
      <template #cell-files="{ row }"><span class="tabular-nums">{{ row.files.length }}</span></template>
      <template #cell-security="{ row }">
        <SeverityBadge v-if="worstSeverity(row.security) !== 'none'" :severity="worstSeverity(row.security)" :count="worstCount(row.security)" />
        <span v-else-if="row.scannedAt" class="text-xs text-muted">{{ t('artifact.versions.clean') }}</span>
        <span v-else class="text-xs text-muted">{{ t('artifact.versions.notScanned') }}</span>
        <span v-if="row.flagged && worstSeverity(row.security) !== 'critical' && worstSeverity(row.security) !== 'high'" class="ml-2 text-xs text-warning">{{ t('artifact.versions.flagged') }}</span>
      </template>
    </ListTable>
  </section>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import SeverityBadge from '../ui/SeverityBadge.vue'
import { useLocalList } from '../../composables/useLocalList'
import { useArtifactContext } from '../../composables/useArtifact'
import { strParam } from '../../composables/useUrlState'
import { artifactPath } from '../../lib/artifactPath'
import { compareVersionStrings, worstCount, worstSeverity, type VersionEntry } from '../../lib/artifactVersions'
import { formatBytes } from '../../lib/quota'
import { t, tp } from '../../i18n/en'

const ctx = useArtifactContext()

const columns: ListColumn[] = [
  { key: 'version', label: t('artifact.versions.column.version'), sortable: true },
  { key: 'created', label: t('artifact.versions.column.published'), sortable: true },
  { key: 'size', label: t('artifact.versions.column.size'), sortable: true },
  { key: 'files', label: t('artifact.versions.column.files') },
  { key: 'security', label: t('artifact.versions.column.security') },
]

const { list, refresh } = useLocalList({
  filters: { q: strParam() },
  debounce: ['q'],
  // Newest first is the listing order; `published` as the default sort keeps it.
  sorts: ['created', 'version', 'size'],
  defaultDir: 'desc',
  load: async (): Promise<VersionEntry[]> => ctx.versions.value,
  match: (row, { q }) => !q || row.version.toLowerCase().includes(q.toLowerCase()),
  compare: (a, b, sort) => {
    if (sort === 'version') return compareVersionStrings(a.version, b.version)
    if (sort === 'size') return a.size - b.size
    return Date.parse(a.createdAt || '0') - Date.parse(b.createdAt || '0')
  },
})

function versionTo(row: VersionEntry): string {
  return artifactPath(ctx.repoName.value, ctx.pkgName.value, row.latest ? undefined : row.version)
}

// A deleted version or a reload changes the rows.
watch(ctx.versions, () => void refresh())
</script>

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
        <div class="flex min-w-0 items-center gap-1.5">
          <span v-if="row.stored === false" class="truncate font-mono text-muted" :title="row.version" data-testid="held-version">{{ row.version }}</span>
          <router-link v-else :to="versionTo(row)" class="truncate font-mono text-primary hover:underline" :title="row.version">{{ row.version }}</router-link>
          <span v-if="row.version === ctx.entry.value?.version" class="rounded-full border border-border px-2 py-0.5 text-micro text-muted">{{ t('artifact.badge.current') }}</span>
          <span v-if="row.latest" class="rounded-full border border-info/30 bg-info-soft px-2 py-0.5 text-micro text-info">{{ t('artifact.badge.latest') }}</span>
          <span v-if="row.prerelease" class="rounded-full border border-border px-2 py-0.5 text-micro text-muted">{{ t('artifact.badge.prerelease') }}</span>
        </div>
      </template>
      <template #cell-created="{ row }"><RelativeTime :value="row.createdAt" :class="row.stored === false ? 'text-muted' : ''" /></template>
      <template #cell-size="{ row }"><span class="font-mono text-xs tabular-nums">{{ row.size > 0 ? formatBytes(row.size) : '—' }}</span></template>
      <template #cell-files="{ row }"><span class="tabular-nums">{{ row.stored === false ? '—' : row.files.length }}</span></template>
      <template #cell-availability="{ row }">
        <span v-if="row.held" class="text-xs text-warning" :title="row.held.reasons[0]?.detail" data-testid="held-status">{{ heldStatus(row.held) }}</span>
      </template>
      <template #cell-security="{ row }">
        <span v-if="row.stored === false" class="text-xs text-muted">—</span>
        <SeverityBadge v-else-if="worstSeverity(row.security) !== 'none'" :severity="worstSeverity(row.security)" :count="worstCount(row.security)" />
        <span v-else-if="row.scannedAt && !row.flagged" class="text-xs text-muted">{{ t('artifact.versions.clean') }}</span>
        <span v-else-if="!row.scannedAt && !row.flagged" class="text-xs text-muted">{{ t('artifact.versions.notScanned') }}</span>
        <span v-if="row.stored !== false && row.flagged && worstSeverity(row.security) !== 'critical' && worstSeverity(row.security) !== 'high'" class="text-xs text-warning" :class="worstSeverity(row.security) === 'none' ? '' : 'ml-2'">{{ t('artifact.versions.flagged') }}</span>
      </template>
    </ListTable>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import SeverityBadge from '../ui/SeverityBadge.vue'
import { useLocalList } from '../../composables/useLocalList'
import { useArtifactContext } from '../../composables/useArtifact'
import { strParam } from '../../composables/useUrlState'
import { artifactPath } from '../../lib/artifactPath'
import type { HeldBackVersion } from '../../api/curation'
import { compareVersionStrings, worstCount, worstSeverity, type VersionEntry } from '../../lib/artifactVersions'
import { heldStatus } from '../../lib/availability'
import { formatBytes } from '../../lib/quota'
import { t, tp } from '../../i18n'

const ctx = useArtifactContext()

/** A listed version, or one the repository's gate holds back (`stored: false`: nothing of it is in the repository). */
type VersionRow = VersionEntry & { stored?: boolean; held?: HeldBackVersion }

// The availability column exists only when the gate holds something back.
const columns = computed<ListColumn[]>(() => [
  { key: 'version', label: t('artifact.versions.column.version'), sortable: true, grow: true },
  { key: 'created', label: t('artifact.versions.column.published'), sortable: true },
  { key: 'size', label: t('artifact.versions.column.size'), sortable: true, dropOrder: 2 },
  { key: 'files', label: t('artifact.versions.column.files'), dropOrder: 1 },
  { key: 'security', label: t('artifact.versions.column.security') },
  ...(ctx.heldBack.value.length > 0 ? [{ key: 'availability', label: t('artifact.versions.column.availability') }] : []),
])

/** The stored versions (marked when the gate holds them back too) and, greyed, the versions only the gate knows. */
function rowsWithHeld(): VersionRow[] {
  const held = new Map(ctx.heldBack.value.map((h) => [h.version, h]))
  const rows: VersionRow[] = ctx.versions.value.map((v) => ({ ...v, held: held.get(v.version) }))
  for (const h of ctx.heldBack.value) {
    if (ctx.versions.value.some((v) => v.version === h.version)) continue
    rows.push({
      version: h.version,
      files: [],
      size: 0,
      createdAt: h.published_at ?? '',
      security: null,
      scannedAt: null,
      flagged: false,
      latest: false,
      prerelease: false,
      stored: false,
      held: h,
    })
  }
  return rows
}

const { list, refresh } = useLocalList({
  filters: { q: strParam() },
  debounce: ['q'],
  // Newest first is the listing order; `published` as the default sort keeps it.
  sorts: ['created', 'version', 'size'],
  defaultDir: 'desc',
  load: async (): Promise<VersionRow[]> => rowsWithHeld(),
  match: (row, { q }) => !q || row.version.toLowerCase().includes(q.toLowerCase()),
  compare: (a, b, sort) => {
    if (sort === 'version') return compareVersionStrings(a.version, b.version)
    if (sort === 'size') return a.size - b.size
    return Date.parse(a.createdAt || '0') - Date.parse(b.createdAt || '0')
  },
})

function versionTo(row: VersionRow): string {
  return artifactPath(ctx.repoName.value, ctx.pkgName.value, row.latest ? undefined : row.version)
}

// A deleted version or a reload changes the rows.
watch([ctx.versions, ctx.heldBack], () => void refresh())
</script>

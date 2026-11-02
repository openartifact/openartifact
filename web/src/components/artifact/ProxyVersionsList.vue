<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The versions of a proxied package: everything the upstream metadata this repository has cached names, newest first,
  each Available (clients can install it) or Held (the gate keeps it from them, with when it opens or why), and Cached
  when a file of it is already stored here. The install command of the selected version (the newest available one
  until another is chosen) is shown above the table; every row can copy its own.
-->
<template>
  <section class="space-y-4" aria-labelledby="proxy-versions-title" data-testid="proxy-versions">
    <div>
      <h2 id="proxy-versions-title" class="text-sm font-semibold text-foreground">{{ t('artifact.tab.versions') }}</h2>
      <p class="mt-1 text-xs text-muted">{{ t('artifact.proxyVersions.hint') }}</p>
    </div>

    <ArtifactInstall
      v-if="selected"
      :key="selected.version"
      :format="format"
      :repo="repo"
      repo-type="proxy"
      :pkg="pkg"
      :version="selected.version"
      :quarantined="false"
      :critical="0"
    />

    <div>
      <FilterBar
        :active="list.filtered.value"
        :placeholder="filterLabel"
        :search="list.text.q"
        :search-label="filterLabel"
        @clear="list.clear()"
        @update:search="list.setFilter('q', $event)"
      />
      <ListTable :columns="columns" :query="list" row-key="version" :caption="tp('artifact.versions.caption', { name: pkg })" :empty-title="t('artifact.versions.empty')">
        <template #cell-version="{ row }">
          <span class="block truncate font-mono" :class="row.status === 'held' ? 'text-muted' : 'text-foreground'" :title="row.version" data-testid="proxy-version">{{ row.version }}</span>
        </template>
        <template #cell-status="{ row }">
          <div class="flex flex-wrap items-center gap-1.5">
            <span v-if="row.status === 'held'" class="rounded-full border border-warning/30 bg-warning-soft px-2 py-0.5 text-micro text-warning" data-testid="badge-held">{{ t('artifact.proxyVersions.held') }}</span>
            <span v-else class="rounded-full border border-success/40 bg-success-soft px-2 py-0.5 text-micro text-success" data-testid="badge-available">{{ t('artifact.proxyVersions.available') }}</span>
            <span
              v-if="row.cached"
              class="rounded-full border border-info/30 bg-info-soft px-2 py-0.5 text-micro text-info"
              :title="t('artifact.proxyVersions.cachedHint')"
              data-testid="badge-cached"
            >{{ t('artifact.proxyVersions.cached') }}</span>
            <span v-if="row.status === 'held'" class="text-xs text-warning" :title="row.reasons[0]?.detail" data-testid="held-status">{{ heldStatus(row) }}</span>
          </div>
        </template>
        <template #cell-published="{ row }"><RelativeTime :value="row.published_at ?? ''" /></template>
        <template #cell-actions="{ row }">
          <RowActions
            :actions="[{ key: 'select', label: t('artifact.proxyVersions.showInstall'), icon: CommandLineIcon, disabled: row.status === 'held', pressed: selected?.version === row.version }]"
            :subject="row.version"
            @action="selectedVersion = row.version"
          >
            <CopyButton v-if="row.status !== 'held'" :text="installText(row)" :label="tp('artifact.proxyVersions.copyInstall', { version: row.version })" />
          </RowActions>
        </template>
      </ListTable>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CommandLineIcon } from '@heroicons/vue/20/solid'
import ArtifactInstall from './ArtifactInstall.vue'
import CopyButton from '../ui/CopyButton.vue'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import RowActions from '../ui/RowActions.vue'
import { useLocalList } from '../../composables/useLocalList'
import { strParam } from '../../composables/useUrlState'
import type { ProxyVersion } from '../../api/curation'
import { compareVersionStrings } from '../../lib/artifactVersions'
import { heldStatus } from '../../lib/availability'
import { pullCommand } from '../../lib/installCommands'
import { t, tp } from '../../i18n'

const props = defineProps<{
  format: string
  repo: string
  pkg: string
  /** Newest first, as the server lists them. */
  versions: ProxyVersion[]
}>()

const filterLabel = computed(() => t('artifact.versions.filter'))

const columns = computed<ListColumn[]>(() => [
  { key: 'version', label: t('artifact.versions.column.version'), sortable: true, grow: true },
  { key: 'status', label: t('artifact.proxyVersions.column.status') },
  { key: 'published', label: t('artifact.versions.column.published'), sortable: true, dropOrder: 1 },
  { key: 'actions', label: t('common.actions'), actions: true },
])

/** An undated version sorts as the oldest. */
const publishedMs = (v: ProxyVersion): number => (v.published_at ? Date.parse(v.published_at) : 0)

const { list, refresh } = useLocalList({
  filters: { q: strParam() },
  debounce: ['q'],
  sorts: ['published', 'version'],
  defaultDir: 'desc',
  load: async (): Promise<ProxyVersion[]> => props.versions,
  match: (row, { q }) => !q || row.version.toLowerCase().includes(q.toLowerCase()),
  compare: (a, b, sort) => {
    if (sort === 'version') return compareVersionStrings(a.version, b.version)
    return publishedMs(a) - publishedMs(b) || compareVersionStrings(a.version, b.version)
  },
})
watch(() => props.versions, () => void refresh())

// The newest version a client can install, until the reader picks one.
const selectedVersion = ref('')
const selected = computed(() => {
  const chosen = props.versions.find((v) => v.version === selectedVersion.value && v.status !== 'held')
  return chosen ?? props.versions.find((v) => v.status !== 'held')
})

function installText(row: ProxyVersion): string {
  return pullCommand(props.format, window.location.origin, props.repo, props.pkg, row.version, 'proxy')
}
</script>

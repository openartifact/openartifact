<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Results of the Packages search in either mode. Name mode groups hits by package (one row per
  repository + name, versions expand in place); checksum mode lists the files whose digest
  matched with the package, version and repository they belong to.
-->
<template>
  <div>
    <p class="mb-2 text-xs text-muted" role="status" data-testid="search-count">
      {{ tp(isChecksum ? 'search.count.checksum' : 'search.count.packages', { total: String(list.total.value) }) }}
    </p>
    <ListTable
      :columns="columns"
      :caption="isChecksum ? t('search.caption.checksum') : t('search.caption.packages')"
      :rows="list.rows.value"
      :loading="list.loading.value"
      :error="list.error.value"
      :total="list.total.value"
      :page="list.page.value"
      :limit="list.limit"
      :filtered="scoped"
      :empty-title="emptyTitle"
      :empty-hint="emptyHint"
      @update:page="list.setPage"
      @retry="list.reload"
      @clear="clearScope"
    >
      <template #cell-name="{ row }">
        <div class="flex min-w-0 items-center gap-2">
          <FormatBadge :format="row.format" size="sm" />
          <router-link :to="row.href" class="min-w-0 break-all font-medium hover:underline">{{ row.name }}</router-link>
        </div>
        <VersionList v-if="isPackage(row) && isOpen(row.id)" :repo="row.repository" :name="row.name" :format="row.format" />
      </template>
      <template #cell-latest="{ row }">
        <span v-if="isPackage(row)" class="font-mono text-xs">{{ row.latestVersion }}</span>
      </template>
      <template #cell-versions="{ row }">
        <span v-if="isPackage(row)" class="inline-flex items-center gap-1 whitespace-nowrap">
          {{ row.versionCount === 1 ? t('search.versionCount.one') : tp('search.versionCount', { count: String(row.versionCount) }) }}
          <button
            v-if="row.versionCount > 1"
            type="button"
            class="inline-flex h-6 w-6 items-center justify-center rounded text-muted hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11 max-md:w-11"
            :aria-expanded="isOpen(row.id)"
            :aria-label="tp(isOpen(row.id) ? 'search.hideVersions' : 'search.showVersions', { name: row.name })"
            @click="toggle(row.id)"
          >
            <ChevronDownIcon class="h-4 w-4 transition-transform" :class="{ 'rotate-180': isOpen(row.id) }" aria-hidden="true" />
          </button>
        </span>
      </template>
      <template #cell-repository="{ row }">
        <router-link :to="row.repositoryHref" class="hover:underline">{{ row.repository }}</router-link>
      </template>
      <template #cell-file="{ row }">
        <template v-if="!isPackage(row)">
          <span class="font-mono text-xs">{{ row.filename }}</span>
          <span class="block break-all font-mono text-xs text-muted">{{ row.checksum }}</span>
        </template>
      </template>
      <template #cell-version="{ row }">
        <router-link v-if="!isPackage(row)" :to="row.href" class="font-mono text-xs hover:underline">{{ row.version }}</router-link>
      </template>
      <template #cell-algorithm="{ row }">
        <span v-if="!isPackage(row)" class="font-mono text-xs">{{ row.algorithm }}</span>
      </template>
    </ListTable>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronDownIcon } from '@heroicons/vue/20/solid'
import FormatBadge from '../FormatBadge.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import type { PackageRow, SearchRow } from '../../lib/packageSearch'
import VersionList from './VersionList.vue'
import type { useSearch } from '../../composables/useSearch'
import { t, tp } from '../../i18n/en'

const props = defineProps<{
  list: ReturnType<typeof useSearch>['list']
  mode: 'name' | 'checksum'
}>()

const isChecksum = computed(() => props.mode === 'checksum')

const PACKAGE_COLUMNS: ListColumn[] = [
  { key: 'name', label: t('search.column.package') },
  { key: 'latest', label: t('search.column.latest') },
  { key: 'versions', label: t('search.column.versions') },
  { key: 'repository', label: t('search.column.repository') },
]
const CHECKSUM_COLUMNS: ListColumn[] = [
  { key: 'name', label: t('search.column.package') },
  { key: 'file', label: t('search.column.file') },
  { key: 'version', label: t('search.column.version') },
  { key: 'repository', label: t('search.column.repository') },
  { key: 'algorithm', label: t('search.column.algorithm') },
]
const columns = computed(() => (isChecksum.value ? CHECKSUM_COLUMNS : PACKAGE_COLUMNS))

/** Format, repository or flagged narrow the search: an empty result then offers to clear them. */
const scoped = computed(() => {
  const f = props.list.filters.value
  return f.format !== '' || f.repo !== '' || f.flagged
})
const emptyTitle = computed(() =>
  isChecksum.value ? t('search.empty.checksum.title') : tp('search.empty.title', { q: props.list.filters.value.q.trim() }),
)
const emptyHint = computed(() => (isChecksum.value ? t('search.empty.checksum.hint') : t('search.empty.hint')))

function clearScope(): void {
  props.list.setFilters({ format: '', repo: '', flagged: false })
}

const isPackage = (r: SearchRow): r is PackageRow => 'latestVersion' in r

const open = ref(new Set<string>())
const isOpen = (id: string): boolean => open.value.has(id)
function toggle(id: string): void {
  const next = new Set(open.value)
  if (!next.delete(id)) next.add(id)
  open.value = next
}
</script>

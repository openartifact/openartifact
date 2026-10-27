<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section aria-labelledby="artifact-deps">
    <h2 id="artifact-deps" class="sr-only">{{ t('artifact.tab.dependencies') }}</h2>
    <p v-if="ctx.detailError.value" role="alert" class="text-sm text-danger">{{ ctx.detailError.value }}</p>
    <template v-else>
      <FilterBar
        :search="list.text.q"
        :search-label="t('artifact.deps.filter')"
        :placeholder="t('artifact.deps.filter')"
        :active="list.filtered.value"
        @update:search="list.setFilter('q', $event)"
        @clear="list.clear()"
      >
        <select
          v-if="kinds.length > 1"
          :value="list.filters.value.kind"
          :aria-label="t('artifact.deps.kind')"
          class="h-9 rounded-lg border border-border-strong bg-surface px-2 text-sm text-foreground focus:ring-2 focus:ring-primary max-md:h-11"
          @change="list.setFilter('kind', ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('artifact.deps.allKinds') }}</option>
          <option v-for="k in kinds" :key="k" :value="k">{{ k }}</option>
        </select>
      </FilterBar>
      <ListTable
        :columns="columns"
        :query="list"
        row-key="key"
        :caption="tp('artifact.deps.caption', { name: ctx.pkgName.value, version: ctx.entry.value?.version ?? '' })"
        :empty-title="t('artifact.deps.empty')"
        :empty-hint="t('artifact.deps.emptyHint')"
      >
        <template #cell-name="{ row }"><span class="block truncate font-mono text-xs" :title="row.name">{{ row.name }}</span></template>
        <template #cell-version="{ row }"><span class="break-all font-mono text-xs">{{ row.version || '—' }}</span></template>
        <template #cell-kind="{ row }"><span class="text-xs text-muted">{{ row.kind || '—' }}</span></template>
      </ListTable>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import { useArtifactContext } from '../../composables/useArtifact'
import { useLocalList } from '../../composables/useLocalList'
import { strParam } from '../../composables/useUrlState'
import { t, tp } from '../../i18n'
import { compareText } from '../../lib/intl'

const ctx = useArtifactContext()

const columns: ListColumn[] = [
  { key: 'name', label: t('artifact.deps.column.name'), sortable: true, grow: true },
  { key: 'version', label: t('artifact.deps.column.range') },
  { key: 'kind', label: t('artifact.deps.column.kind'), dropOrder: 1 },
]

// A dependency can be listed once per kind: the row key joins both.
interface Row {
  key: string
  name: string
  version: string
  kind: string
}

const { list, all, refresh } = useLocalList({
  filters: { q: strParam(), kind: strParam() },
  debounce: ['q'],
  sorts: ['name'],
  load: async (): Promise<Row[]> =>
    (ctx.detail.value?.dependencies ?? []).map((d, i) => ({ key: `${i}:${d.name}`, name: d.name, version: d.version ?? '', kind: d.kind ?? '' })),
  match: (row, { q, kind }) => (!kind || row.kind === kind) && (!q || row.name.toLowerCase().includes(q.toLowerCase())),
  compare: (a, b) => compareText(a.name, b.name),
})

const kinds = computed(() => [...new Set(all.value.map((r) => r.kind).filter(Boolean))].sort())

watch(ctx.detail, () => void refresh())
</script>

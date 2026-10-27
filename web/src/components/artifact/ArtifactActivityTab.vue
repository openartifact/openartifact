<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Audit events that recorded this version, newest first. The server shows them to maintainers and admins only. -->
<template>
  <section aria-labelledby="artifact-activity">
    <h2 id="artifact-activity" class="sr-only">{{ t('artifact.tab.activity') }}</h2>
    <FilterBar
      :search="list.text.action"
      :search-label="t('artifact.activity.filter')"
      :placeholder="t('artifact.activity.filter')"
      :active="list.filtered.value"
      @update:search="list.setFilter('action', $event)"
      @clear="list.clear()"
    />
    <ListTable
      :columns="columns"
      :query="list"
      row-key="id"
      :caption="tp('artifact.activity.caption', { name: ctx.pkgName.value, version: ctx.entry.value?.version ?? '' })"
      :empty-title="t('artifact.activity.empty')"
      :empty-hint="t('artifact.activity.emptyHint')"
    >
      <template #cell-timestamp="{ row }"><RelativeTime :value="row.timestamp" /></template>
      <template #cell-actor="{ row }"><span class="text-sm">{{ row.actor_name || '—' }}</span></template>
      <template #cell-action="{ row }"><span class="text-sm">{{ actionLabel(row.action) }}</span></template>
      <template #cell-message="{ row }"><span class="block truncate text-sm text-muted" :title="row.message">{{ row.message || '—' }}</span></template>
    </ListTable>
  </section>
</template>

<script setup lang="ts">
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import { listArtifactActivity } from '../../api/artifact'
import { useArtifactContext } from '../../composables/useArtifact'
import { useListQuery } from '../../composables/useListQuery'
import { strParam } from '../../composables/useUrlState'
import { actionLabel } from '../../lib/activity'
import { t, tp } from '../../i18n'

const ctx = useArtifactContext()

const columns: ListColumn[] = [
  { key: 'timestamp', label: t('artifact.activity.column.when') },
  { key: 'actor', label: t('artifact.activity.column.actor'), dropOrder: 1 },
  { key: 'action', label: t('artifact.activity.column.action') },
  { key: 'message', label: t('artifact.activity.column.details'), grow: true },
]

const list = useListQuery({
  filters: { action: strParam() },
  debounce: ['action'],
  paging: 'cursor',
  limit: 25,
  fetch: async ({ filters, cursor }) => {
    const version = ctx.entry.value?.version ?? ''
    const page = await listArtifactActivity(ctx.repoName.value, ctx.pkgName.value, version, { cursor, action: filters.action, limit: 25 })
    return { rows: page.entries, hasMore: page.hasMore, nextCursor: page.nextCursor ?? undefined }
  },
})
</script>

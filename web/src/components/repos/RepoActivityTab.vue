<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Audit events of one repository for its maintainers: who did what to which package, newest first, "Load more" keyset paging. -->
<template>
  <ListTable
    :columns="columns"
    :caption="t('rl.activity.caption')"
    :query="list"
    :row-key="(r) => r.id"
    :empty-title="t('rl.activity.empty')"
    :empty-hint="t('rl.activity.emptyHint')"
  >
    <template #cell-when="{ row }"><RelativeTime :value="row.timestamp" class="text-xs text-muted" /></template>
    <template #cell-actor="{ row }"><span class="text-sm">{{ row.actor_name || row.actor || '—' }}</span></template>
    <template #cell-action="{ row }"><span class="text-sm">{{ actionLabel(row.action) }}</span></template>
    <template #cell-subject="{ row }">
      <router-link v-if="subjectLink(row)" :to="subjectLink(row)!" class="font-mono text-xs text-foreground hover:text-primary">{{ subjectText(row) }}</router-link>
      <span v-else class="text-sm text-muted">{{ subjectText(row) || row.message || '—' }}</span>
    </template>
  </ListTable>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import { listRepoActivity, type RepoActivityEntry } from '../../api/repositories'
import { useListQuery } from '../../composables/useListQuery'
import { actionLabel } from '../../lib/activity'
import { artifactPath } from '../../lib/artifactPath'
import { t } from '../../i18n/en'

const props = defineProps<{ repo: string }>()

const list = useListQuery({
  filters: {},
  paging: 'cursor',
  limit: 25,
  async fetch(req) {
    const res = await listRepoActivity(props.repo, { cursor: req.cursor, limit: req.limit })
    return { rows: res.items, hasMore: res.hasMore, nextCursor: res.nextCursor }
  },
})

const columns = computed<ListColumn[]>(() => [
  { key: 'when', label: t('rl.activity.when') },
  { key: 'actor', label: t('rl.activity.actor') },
  { key: 'action', label: t('rl.activity.action') },
  { key: 'subject', label: t('rl.activity.subject') },
])

function subjectText(e: RepoActivityEntry): string {
  const s = e.subject
  if (!s?.name) return ''
  return s.version ? `${s.name} ${s.version}` : s.name
}

// Packages link to their artifact page; other subjects are plain text.
function subjectLink(e: RepoActivityEntry): string | null {
  const s = e.subject
  return s?.kind === 'package' && s.name ? artifactPath(props.repo, s.name, s.version) : null
}
</script>

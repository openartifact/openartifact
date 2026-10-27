<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > System > Audit log: who did what, newest first. Times are UTC; HTTP request rows are hidden unless asked for. -->
<template>
  <div>
    <SectionActions>
      <AuditExportButtons :download="exportAudit" />
    </SectionActions>

    <FilterBar
      :search="list.text.actor"
      :search-label="t('sx.audit.filter.actor')"
      :placeholder="t('sx.audit.filter.actor')"
      :active="list.filtered.value"
      @update:search="list.setFilter('actor', $event)"
      @clear="list.clear"
    >
      <input
        type="search"
        :value="list.text.action"
        :aria-label="t('sx.audit.filter.action')"
        :placeholder="t('sx.audit.filter.action')"
        class="h-9 w-full min-w-0 rounded-lg border border-border-strong bg-surface px-3 text-sm text-foreground placeholder:text-muted focus:ring-2 focus:ring-primary sm:w-56 max-md:h-11"
        data-testid="audit-action-filter"
        @input="list.setFilter('action', ($event.target as HTMLInputElement).value)"
      />
      <button
        type="button"
        :aria-pressed="hideHttp"
        class="inline-flex h-9 items-center gap-1.5 rounded-lg border px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
        :class="hideHttp ? 'border-primary bg-primary-soft text-primary' : 'border-border-strong bg-surface text-muted hover:text-foreground'"
        data-testid="audit-http-toggle"
        @click="list.setFilter('http', hideHttp)"
      >
        <CheckIcon v-if="hideHttp" class="h-4 w-4" aria-hidden="true" />
        {{ t('sx.audit.http.hide') }}
      </button>
    </FilterBar>

    <p v-if="hiddenCount > 0 && hideHttp" class="mb-3 text-sm text-muted" role="status" data-testid="audit-hidden">
      {{ tp('sx.audit.http.hidden', { n: String(hiddenCount) }) }}
      <button
        type="button"
        class="rounded text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:min-h-11"
        @click="list.setFilter('http', true)"
      >{{ t('sx.audit.http.show') }}</button>
    </p>

    <ListTable
      :columns="columns"
      :caption="t('sx.audit.table')"
      :query="list"
      :row-key="entryKey"
      :empty-title="t('sx.audit.empty')"
      :empty-hint="t('sx.audit.emptyHint')"
    >
      <template #cell-time="{ row }">
        <time :datetime="(row as AuditEntry).timestamp" class="whitespace-nowrap font-mono text-xs text-muted" data-testid="audit-time">{{ formatUtc((row as AuditEntry).timestamp) }}</time>
      </template>

      <template #cell-actor="{ row }">
        <span class="block max-w-48 truncate font-medium text-foreground" :title="(row as AuditEntry).actor || undefined">{{ actorLabel(row as AuditEntry) }}</span>
      </template>

      <template #cell-action="{ row }">
        <span :class="['inline-flex items-center rounded-full px-2 py-0.5 font-mono text-xs font-medium', getActionPillClass((row as AuditEntry).action)]">
          {{ (row as AuditEntry).action || '—' }}
        </span>
      </template>

      <template #cell-subject="{ row }">
        <template v-for="s in [auditSubjectView(row as AuditEntry)]" :key="(row as AuditEntry).id">
          <div v-if="s" class="min-w-0 truncate" :title="s.label">
            <RouterLink
              v-if="s.to"
              :to="s.to"
              class="break-all font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            >{{ s.label }}</RouterLink>
            <span v-else class="break-all text-foreground">{{ s.label }}</span>
            <p class="text-xs text-muted">{{ s.kind }}</p>
          </div>
          <span v-else class="break-all text-sm text-muted">{{ auditSubject(row as AuditEntry) || '—' }}</span>
        </template>
      </template>

      <template #cell-actions="{ row }">
        <RowActions
          :actions="[{ key: 'details', label: t('adm.audit.details'), icon: EyeIcon }]"
          :subject="`${(row as AuditEntry).action ?? ''}, ${formatUtc((row as AuditEntry).timestamp)} UTC`"
          @action="opened = row as AuditEntry"
        />
      </template>
    </ListTable>

    <Drawer :open="opened !== null" :eyebrow="t('sx.audit.drawer.eyebrow')" :title="opened?.action ?? ''" @close="opened = null">
      <dl v-if="opened" class="space-y-4 text-sm" data-testid="audit-detail">
        <div>
          <dt class="text-muted">{{ t('sx.audit.field.time') }}</dt>
          <dd class="font-mono text-foreground">{{ formatUtc(opened.timestamp) }} · <RelativeTime :value="opened.timestamp" /></dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('sx.audit.field.actor') }}</dt>
          <dd class="text-foreground">
            {{ actorLabel(opened) }}
            <span v-if="opened.actor && opened.actor !== opened.actor_name" class="font-mono text-xs text-muted">({{ opened.actor }})</span>
          </dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('sx.audit.field.subject') }}</dt>
          <dd class="break-all text-foreground">
            <template v-for="s in [auditSubjectView(opened)]" :key="opened.id">
              <template v-if="s">
                <RouterLink v-if="s.to" :to="s.to" class="text-primary hover:underline" @click="opened = null">{{ s.label }}</RouterLink>
                <template v-else>{{ s.label }}</template>
                <span class="text-xs text-muted"> · {{ s.kind }}</span>
              </template>
              <template v-else>{{ auditSubject(opened) || t('sx.audit.subject.none') }}</template>
            </template>
          </dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('sx.audit.field.ip') }}</dt>
          <dd class="font-mono text-foreground">{{ opened.ip || t('sx.audit.field.noIp') }}</dd>
        </div>
        <div v-if="opened.id">
          <dt class="text-muted">{{ t('sx.audit.field.id') }}</dt>
          <dd class="break-all font-mono text-xs text-foreground">{{ opened.id }}</dd>
        </div>
        <div v-if="opened.details">
          <dt class="text-muted">{{ t('sx.audit.field.details') }}</dt>
          <dd><pre class="mt-1 overflow-x-auto rounded-lg bg-surface-hover p-3 font-mono text-xs text-foreground">{{ JSON.stringify(opened.details, null, 2) }}</pre></dd>
        </div>
      </dl>
      <template #footer>
        <AppButton v-if="opened?.actor" variant="secondary" data-testid="audit-by-actor" @click="filterByActor(opened!)">
          {{ tp('sx.audit.byActor', { actor: actorLabel(opened) }) }}
        </AppButton>
        <AppButton variant="ghost" @click="opened = null">{{ t('common.close') }}</AppButton>
      </template>
    </Drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { CheckIcon, EyeIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../../components/AppButton.vue'
import AuditExportButtons from '../../../components/AuditExportButtons.vue'
import SectionActions from '../../../components/SectionActions.vue'
import Drawer from '../../../components/ui/Drawer.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import RelativeTime from '../../../components/ui/RelativeTime.vue'
import RowActions from '../../../components/ui/RowActions.vue'
import { downloadAuditExport, listAudit, type AuditEntry, type ExportFormat } from '../../../api/audit'
import { useListQuery } from '../../../composables/useListQuery'
import { boolParam, strParam } from '../../../composables/useUrlState'
import { auditSubject, auditSubjectView, isHttpNoise } from '../../../lib/auditSubject'
import { formatUtc } from '../../../lib/datetime'
import { t, tp } from '../../../i18n'

/** Entries asked of the server per request. */
const CHUNK = 50
/** Requests one "load more" may chain to find visible rows among hidden ones. */
const MAX_CHUNKS = 5

const opened = ref<AuditEntry | null>(null)
const hiddenCount = ref(0)

const list = useListQuery({
  filters: { actor: strParam(), action: strParam(), http: boolParam(false) },
  debounce: ['actor', 'action'],
  paging: 'cursor',
  limit: 25,
  async fetch(req) {
    // The server has no filter for the per-request rows, so they are dropped here; a chunk made only of them
    // would look like an empty page, hence the loop.
    const hide = !req.filters.http
    if (req.cursor === undefined) hiddenCount.value = 0
    const rows: AuditEntry[] = []
    let cursor = req.cursor
    let hasMore = true
    for (let i = 0; i < MAX_CHUNKS && hasMore && rows.length < req.limit; i++) {
      const page = await listAudit({ cursor, limit: CHUNK, actor: req.filters.actor.trim(), action: req.filters.action.trim() })
      for (const entry of page.items) {
        if (hide && isHttpNoise(entry)) hiddenCount.value++
        else rows.push(entry)
      }
      hasMore = page.hasMore && page.nextCursor !== null
      cursor = page.nextCursor ?? undefined
    }
    return { rows, hasMore, nextCursor: cursor }
  },
})

const hideHttp = computed(() => !list.filters.value.http)

const columns: ListColumn[] = [
  { key: 'time', label: t('sx.audit.col.time') },
  { key: 'actor', label: t('sx.audit.col.actor'), dropOrder: 2 },
  { key: 'action', label: t('sx.audit.col.action') },
  { key: 'subject', label: t('sx.audit.col.subject'), grow: true },
  { key: 'actions', label: t('sx.audit.col.actions'), actions: true },
]

const entryKey = (e: AuditEntry): string => e.id ?? `${e.timestamp}:${e.action}:${e.actor}`

function actorLabel(e: AuditEntry): string {
  return e.actor_name || e.actor || t('sx.audit.actor.unknown')
}

function filterByActor(e: AuditEntry): void {
  opened.value = null
  list.setFilter('actor', e.actor ?? '')
}

// The export covers the whole log narrowed by the current filters, not the loaded rows.
function exportAudit(format: ExportFormat): Promise<void> {
  return downloadAuditExport(format, { actor: list.filters.value.actor.trim(), action: list.filters.value.action.trim() })
}

function getActionPillClass(entryAction?: string): string {
  if (!entryAction) return 'bg-surface-hover text-muted'
  const a = entryAction.toLowerCase()
  if (a.includes('creat') || a.includes('upload') || a.includes('publish')) return 'bg-success-soft text-success'
  if (a.includes('delet') || a.includes('revok') || a.includes('fail')) return 'bg-danger-soft text-danger'
  if (a.includes('updat')) return 'bg-warning-soft text-warning'
  return 'bg-primary-soft text-primary'
}
</script>

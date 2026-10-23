<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('admin.audit.title')" :description="t('adm.audit.description')">
      <template #actions>
        <AuditExportButtons :download="exportAudit" />
      </template>
    </PageHeader>

    <!-- Filter Panel (the server filters by actor and action) -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
      <div>
        <label for="audit-actor" class="block text-caption text-muted font-semibold mb-1.5">{{ t('admin.audit.filter.actor') }}</label>
        <input
          id="audit-actor"
          v-model="actor"
          type="text"
          :placeholder="t('adm.audit.filterActor')"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
        />
      </div>
      <div>
        <label for="audit-action" class="block text-caption text-muted font-semibold mb-1.5">{{ t('admin.audit.filter.action') }}</label>
        <input
          id="audit-action"
          v-model="action"
          type="text"
          :placeholder="t('adm.audit.filterAction')"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
        />
      </div>
    </div>

    <!-- TableCard -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.audit.entries') }}</h3>
      </div>
      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="entries.length === 0" class="px-4 py-8 text-center text-muted">{{ t('adm.audit.empty') }}</div>
      <div v-else class="relative overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-caption font-semibold text-muted bg-surface-hover/30">
              <th scope="col" class="text-left px-4 py-2.5 font-medium w-40">{{ t('admin.audit.column.timestamp') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium w-32">{{ t('admin.audit.column.actor') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium w-28">{{ t('admin.audit.column.action') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium flex-1">{{ t('admin.audit.column.target') }}</th>
              <th scope="col" class="text-center px-4 py-2.5 font-medium w-10"><span class="sr-only">{{ t('adm.audit.details') }}</span></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <template v-for="entry in entries" :key="entry.id">
              <tr class="hover:bg-surface-hover/40 transition-colors">
                <td class="px-4 py-3">
                  <span class="font-mono text-xs text-muted">{{ formatTimestamp(entry.timestamp) }}</span>
                </td>
                <td class="px-4 py-3">
                  <div class="flex items-center gap-2">
                    <div class="w-6 h-6 rounded-full bg-primary/20 text-primary text-micro font-bold flex items-center justify-center flex-shrink-0" aria-hidden="true">
                      {{ (entry.actor || '?')[0].toUpperCase() }}
                    </div>
                    <span class="font-mono text-ui font-medium truncate">{{ entry.actor || '—' }}</span>
                  </div>
                </td>
                <td class="px-4 py-3">
                  <span :class="['inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium', getActionPillClass(entry.action)]">
                    {{ entry.action || '—' }}
                  </span>
                </td>
                <td class="px-4 py-3">
                  <span class="text-sm text-muted truncate max-w-[200px] block" :title="entry.target">{{ entry.target || '—' }}</span>
                </td>
                <td class="px-4 py-3 text-center">
                  <button
                    type="button"
                    :aria-expanded="!!entry.id && expanded.has(entry.id)"
                    :aria-label="t('adm.audit.details')"
                    @click="toggleExpand(entry.id)"
                    :class="[
                      'p-1 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-all',
                      entry.id && expanded.has(entry.id) ? 'rotate-90' : '',
                    ]"
                  >
                    <ChevronRightIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                </td>
              </tr>
              <tr v-if="entry.id && expanded.has(entry.id)" class="bg-surface-hover/20">
                <td colspan="5" class="px-4 py-3">
                  <div class="space-y-2">
                    <div v-if="entry.ip" class="flex gap-2">
                      <span class="text-caption text-muted font-semibold min-w-fit">{{ t('adm.audit.ip') }}:</span>
                      <span class="text-caption text-foreground font-mono">{{ entry.ip }}</span>
                    </div>
                    <div v-if="entry.details" class="flex flex-col gap-1">
                      <span class="text-caption text-muted font-semibold">{{ t('adm.audit.details') }}:</span>
                      <pre class="font-mono text-caption bg-surface-hover rounded-lg p-3 overflow-x-auto text-foreground/80">{{ JSON.stringify(entry.details, null, 2) }}</pre>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Pagination -->
    <div v-if="entries.length || pageIndex > 0" class="flex items-center justify-between">
      <span class="text-sm text-muted">{{ t('adm.audit.page') }} {{ pageIndex + 1 }}</span>
      <div class="flex items-center gap-2">
        <AppButton variant="secondary" size="sm" @click="goTo(pageIndex - 1)" :disabled="pageIndex <= 0">
          {{ t('adm.audit.previous') }}
        </AppButton>
        <AppButton variant="secondary" size="sm" @click="goTo(pageIndex + 1)" :disabled="!hasMore">
          {{ t('adm.audit.next') }}
        </AppButton>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../components/AppButton.vue'
import AuditExportButtons from '../../components/AuditExportButtons.vue'
import PageHeader from '../../components/PageHeader.vue'
import { formatTimestamp } from '../../lib/datetime'
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { downloadAuditExport, listAudit, type AuditEntry, type ExportFormat } from '../../api/audit'
import { ChevronRightIcon } from '@heroicons/vue/24/outline'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const PAGE_SIZE = 25

const toasts = useToastStore()
const entries = ref<AuditEntry[]>([])
const loading = ref(true)
const loadError = ref('')
const expanded = ref<Set<string>>(new Set())

// Keyset paging: the server returns no total, only whether more entries follow
// and the cursor of the next page. pageCursors[i] is the cursor that loads page
// i ('' is the newest entry), so going back is a lookup.
const pageCursors = ref<string[]>([''])
const pageIndex = ref(0)
const hasMore = ref(false)
const actor = ref('')
const action = ref('')

// The export covers the whole log narrowed by the current filters, not the
// loaded page.
function exportAudit(format: ExportFormat): Promise<void> {
  return downloadAuditExport(format, { actor: actor.value.trim(), action: action.value.trim() })
}

function toggleExpand(id: string | undefined): void {
  if (id == null) return
  const s = new Set(expanded.value)
  if (!s.delete(id)) s.add(id)
  expanded.value = s
}


function getActionPillClass(entryAction?: string): string {
  if (!entryAction) return 'bg-surface-hover/40 text-muted'
  const a = entryAction.toLowerCase()
  if (a.includes('create')) return 'bg-success-soft/40 text-success'
  if (a.includes('delete')) return 'bg-danger-soft/40 text-danger'
  if (a.includes('update')) return 'bg-warning-soft/40 text-warning'
  return 'bg-primary-soft/40 text-primary'
}

// Only the latest request may write state, so a slow earlier page cannot
// overwrite a newer one.
let requestSeq = 0

async function load(): Promise<void> {
  const seq = ++requestSeq
  loading.value = true
  loadError.value = ''
  try {
    const res = await listAudit({
      cursor: pageCursors.value[pageIndex.value],
      limit: PAGE_SIZE,
      actor: actor.value.trim(),
      action: action.value.trim(),
    })
    if (seq !== requestSeq) return
    entries.value = res.items
    hasMore.value = res.hasMore
    // Remember where the following page starts so Next needs no extra state.
    pageCursors.value = pageCursors.value.slice(0, pageIndex.value + 1)
    if (res.nextCursor) pageCursors.value.push(res.nextCursor)
  } catch (err) {
    if (seq !== requestSeq) return
    loadError.value = err instanceof Error ? err.message : t('adm.audit.loadFailed')
    toasts.error(loadError.value)
  } finally {
    if (seq === requestSeq) loading.value = false
  }
}

let debounceHandle: ReturnType<typeof setTimeout> | null = null

watch([actor, action], () => {
  if (debounceHandle) clearTimeout(debounceHandle)
  debounceHandle = setTimeout(() => {
    // Changing a filter returns to the newest entries.
    pageCursors.value = ['']
    pageIndex.value = 0
    void load()
  }, 300)
})

function goTo(index: number): void {
  if (index < 0 || index >= pageCursors.value.length) return
  pageIndex.value = index
  void load()
}

onMounted(load)
onUnmounted(() => {
  if (debounceHandle) clearTimeout(debounceHandle)
})
</script>

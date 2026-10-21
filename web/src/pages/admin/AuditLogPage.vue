<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <div class="flex items-center gap-2 text-[11px] text-muted font-mono">
          <span>{{ t('nav.admin') }}</span>
          <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
          <span class="text-foreground/70">{{ t('nav.audit') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('admin.audit.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('adm.audit.description') }}</p>
      </div>
    </div>

    <!-- Filter Panel (the server filters by actor and action) -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
      <div>
        <label for="audit-actor" class="block text-[11px] text-muted font-semibold mb-1.5">{{ t('admin.audit.filter.actor') }}</label>
        <input
          id="audit-actor"
          v-model="actor"
          type="text"
          :placeholder="t('adm.audit.filterActor')"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
      </div>
      <div>
        <label for="audit-action" class="block text-[11px] text-muted font-semibold mb-1.5">{{ t('admin.audit.filter.action') }}</label>
        <input
          id="audit-action"
          v-model="action"
          type="text"
          :placeholder="t('adm.audit.filterAction')"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
      </div>
    </div>

    <!-- TableCard -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.audit.entries') }}</h3>
        <span class="text-[11px] font-mono text-muted">{{ total }} {{ t('adm.common.total') }}</span>
      </div>
      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="entries.length === 0" class="px-4 py-8 text-center text-muted">{{ t('adm.audit.empty') }}</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-[11px] font-semibold text-muted bg-surface-hover/30">
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
                  <span class="font-mono text-[12px] text-muted">{{ formatTimestamp(entry.timestamp) }}</span>
                </td>
                <td class="px-4 py-3">
                  <div class="flex items-center gap-2">
                    <div class="w-6 h-6 rounded-full bg-primary/20 text-primary text-[10px] font-bold flex items-center justify-center flex-shrink-0" aria-hidden="true">
                      {{ (entry.actor || '?')[0].toUpperCase() }}
                    </div>
                    <span class="font-mono text-[13px] font-medium truncate">{{ entry.actor || '—' }}</span>
                  </div>
                </td>
                <td class="px-4 py-3">
                  <span :class="['inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] font-medium', getActionPillClass(entry.action)]">
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
                      <span class="text-[11px] text-muted font-semibold min-w-fit">{{ t('adm.audit.ip') }}:</span>
                      <span class="text-[11px] text-foreground font-mono">{{ entry.ip }}</span>
                    </div>
                    <div v-if="entry.details" class="flex flex-col gap-1">
                      <span class="text-[11px] text-muted font-semibold">{{ t('adm.audit.details') }}:</span>
                      <pre class="font-mono text-[11px] bg-surface-hover rounded-lg p-3 overflow-x-auto text-foreground/80">{{ JSON.stringify(entry.details, null, 2) }}</pre>
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
    <div v-if="entries.length" class="flex items-center justify-between">
      <span class="text-sm text-muted">{{ t('adm.audit.page') }} {{ page }} / {{ pageCount }}</span>
      <div class="flex items-center gap-2">
        <button
          type="button"
          @click="page--"
          :disabled="page <= 1"
          class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ t('adm.audit.previous') }}
        </button>
        <button
          type="button"
          @click="page++"
          :disabled="page >= pageCount"
          class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ t('adm.audit.next') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { FORMAT_LOCALE } from '../../lib/datetime'
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { listAudit, type AuditEntry } from '../../api/audit'
import { ChevronRightIcon } from '@heroicons/vue/24/outline'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const PAGE_SIZE = 25

const toasts = useToastStore()
const entries = ref<AuditEntry[]>([])
const total = ref(0)
const loading = ref(true)
const loadError = ref('')
const expanded = ref<Set<string>>(new Set())

const page = ref(1)
const actor = ref('')
const action = ref('')

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

function toggleExpand(id: string | undefined): void {
  if (id == null) return
  const s = new Set(expanded.value)
  if (!s.delete(id)) s.add(id)
  expanded.value = s
}

function formatTimestamp(ts: string | undefined): string {
  if (!ts) return '—'
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  return d.toLocaleString(FORMAT_LOCALE, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
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
      page: page.value,
      limit: PAGE_SIZE,
      actor: actor.value.trim(),
      action: action.value.trim(),
    })
    if (seq !== requestSeq) return
    entries.value = res.items
    total.value = res.total
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
    // Changing a filter returns to page 1; the page watcher performs the load
    // when the page actually changes.
    if (page.value !== 1) page.value = 1
    else void load()
  }, 300)
})

watch(page, () => void load())

onMounted(load)
onUnmounted(() => {
  if (debounceHandle) clearTimeout(debounceHandle)
})
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-end justify-between gap-4 flex-wrap">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">{{ t('dashboard.welcome') }}</h1>
        <p class="mt-1 text-sm text-muted">
          {{ t('dashboard.summary').replace('{repos}', String(total)).replace('{formats}', String(uniqueFormats.length)) }}
        </p>
      </div>
      <div class="flex gap-3">
        <button
          class="inline-flex items-center gap-1.5 px-4 py-2 text-sm font-medium text-foreground bg-surface border border-border rounded-lg hover:bg-surface-hover transition-colors disabled:opacity-50"
          :disabled="syncing"
          @click="syncNow"
        >
          <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': syncing }" aria-hidden="true" />
          {{ t('dashboard.refresh') }}
        </button>
        <router-link
          v-if="session.isAdmin"
          to="/repos/new"
          class="px-4 py-2 text-sm font-medium text-primary-contrast bg-primary rounded-lg hover:bg-primary/90 transition-colors inline-flex items-center gap-1.5"
        >
          {{ t('repos.new') }}
        </router-link>
      </div>
    </div>

    <!-- KPI Cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
      <div
        v-for="(kpi, i) in kpis"
        :key="i"
        class="group relative overflow-hidden rounded-xl border border-border bg-surface p-4 transition-all duration-200 hover:-translate-y-0.5 hover:shadow-md hover:border-primary/30"
      >
        <div class="flex items-start justify-between">
          <span class="text-[11px] uppercase tracking-[0.14em] font-semibold text-muted">{{ kpi.label }}</span>
        </div>
        <p class="mt-3 text-3xl font-bold tracking-tight tabular-nums">{{ kpi.value }}</p>
        <div class="absolute -bottom-1 -right-1 w-24 h-24 rounded-full bg-primary/5 group-hover:bg-primary/10 transition-colors" />
      </div>
    </div>

    <!-- Main Grid: Storage + Top Repositories -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
      <!-- Storage by Format -->
      <div class="lg:col-span-1 rounded-xl border border-border bg-surface p-5">
        <h2 class="text-[13px] font-semibold uppercase tracking-[0.14em] text-muted mb-4">{{ t('dashboard.byFormat') }}</h2>
        <div class="flex items-center gap-5">
          <svg viewBox="0 0 200 200" class="w-40 h-40 shrink-0" role="img" :aria-label="t('dashboard.byFormat')">
            <!-- Donut slices -->
            <circle
              v-for="(slice, i) in donutSlices"
              :key="i"
              cx="100"
              cy="100"
              r="70"
              fill="none"
              :stroke="slice.color"
              stroke-width="20"
              :stroke-dasharray="slice.dashArray"
              :stroke-dashoffset="slice.dashOffset"
              transform="rotate(-90 100 100)"
            />
            <!-- Center text -->
            <text x="100" y="100" text-anchor="middle" dy="0.3em" class="text-lg font-bold fill-foreground">
              {{ total }}
            </text>
          </svg>
          <!-- Legend -->
          <div class="flex-1 grid grid-cols-1 gap-1.5 text-xs">
            <div v-for="item in uniqueFormats" :key="item.format" class="flex items-center justify-between gap-2">
              <div class="flex items-center gap-2 min-w-0">
                <FormatBadge :format="item.format" size="sm" />
              </div>
              <span class="font-mono text-muted tabular-nums">{{ item.count }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Top Repositories -->
      <div class="lg:col-span-2 rounded-xl border border-border bg-surface p-5">
        <div class="flex items-center justify-between mb-4">
          <h2 class="text-[13px] font-semibold uppercase tracking-[0.14em] text-muted">{{ t('dashboard.recent') }}</h2>
          <router-link to="/repos" class="text-xs text-primary hover:underline inline-flex items-center gap-0.5">
            {{ t('dashboard.viewAll') }}
            <ArrowRightIcon class="w-3 h-3" aria-hidden="true" />
          </router-link>
        </div>
        <div v-if="loading" class="text-sm text-muted py-6">{{ t('common.loading') }}</div>
        <div v-else-if="loadError" class="py-6 text-sm text-danger" role="alert">{{ loadError }}</div>
        <div v-else-if="repos.length === 0">
          <EmptyState :title="t('dashboard.emptyTitle')" :description="t('repos.emptyHint')" />
        </div>
        <div v-else class="space-y-1">
          <router-link
            v-for="repo in recentRepos"
            :key="repo.id"
            :to="`/repos/${encodeURIComponent(repo.name)}`"
            class="w-full flex items-center gap-3 px-2 py-3 -mx-2 rounded-md hover:bg-surface-hover transition-colors"
          >
            <FormatBadge :format="repo.format" />
            <span class="font-medium text-sm truncate flex-1">{{ repo.name }}</span>
            <span class="text-xs text-muted">{{ repo.repo_type || 'hosted' }}</span>
            <span class="text-xs text-muted">{{ repo.public ? t('repos.public') : t('repos.private') }}</span>
          </router-link>
        </div>
      </div>
    </div>

    <!-- Recent Activity (admin only) -->
    <div v-if="session.isAdmin" class="rounded-xl border border-border bg-surface p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-[13px] font-semibold uppercase tracking-[0.14em] text-muted">{{ t('dashboard.activity') }}</h2>
        <router-link to="/admin/audit" class="text-xs text-primary hover:underline inline-flex items-center gap-0.5">
          {{ t('dashboard.viewAll') }}
          <ArrowRightIcon class="w-3 h-3" aria-hidden="true" />
        </router-link>
      </div>

      <div v-if="auditLoading" class="space-y-3">
        <div v-for="i in 5" :key="i" class="flex items-start gap-3">
          <div class="w-7 h-7 rounded-full skel shrink-0" />
          <div class="flex-1 space-y-1.5">
            <div class="h-3 w-3/4 rounded skel" />
            <div class="h-2.5 w-1/2 rounded skel" />
          </div>
        </div>
      </div>

      <div v-else-if="auditEntries.length === 0" class="py-6 text-center text-sm text-muted">
        {{ t('dashboard.noActivity') }}
      </div>

      <div v-else class="relative">
        <!-- Timeline line -->
        <div class="absolute left-3.5 top-3 bottom-3 w-px bg-border" />

        <div class="space-y-4">
          <div
            v-for="entry in auditEntries"
            :key="entry.id"
            class="relative flex items-start gap-4 pl-10"
          >
            <!-- Dot -->
            <div
              class="absolute left-2 top-1.5 w-3 h-3 rounded-full border-2 border-surface shrink-0"
              :class="auditDotClass(entry.result)"
            />

            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2 flex-wrap">
                <span class="text-[12px] font-semibold text-foreground font-mono">{{ entry.actor }}</span>
                <span
                  class="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-mono font-medium"
                  :class="auditActionClass(entry.action)"
                >
                  {{ entry.action }}
                </span>
                <span v-if="entry.target" class="text-[12px] text-foreground/70 font-mono truncate max-w-[200px]">
                  {{ entry.target }}
                </span>
              </div>
              <p class="mt-0.5 text-[11px] text-muted font-mono">
                {{ timeAgo(entry.timestamp) }}
                <span v-if="entry.result && entry.result !== 'ok'" class="text-danger ml-1">· {{ entry.result }}</span>
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ArrowRightIcon, ArrowPathIcon } from '@heroicons/vue/24/outline'
import { listAllRepositories } from '../api/repositories'
import { listAudit } from '../api/audit'
import type { Repository } from '../api/repositories'
import type { AuditEntry } from '../api/audit'
import { useToastStore } from '../stores/toast'
import { useSessionStore } from '../stores/session'
import FormatBadge from '../components/FormatBadge.vue'
import EmptyState from '../components/EmptyState.vue'
import { formatColor } from '../lib/formats'
import { t } from '../i18n/en'

const toasts = useToastStore()
const session = useSessionStore()

const repos = ref<Repository[]>([])
const loadError = ref('')
const loading = ref(true)
const syncing = ref(false)
const auditEntries = ref<AuditEntry[]>([])
const auditLoading = ref(false)

const total = computed(() => repos.value.length)

const uniqueFormats = computed(() => {
  const counts = new Map<string, number>()
  repos.value.forEach(r => {
    counts.set(r.format, (counts.get(r.format) || 0) + 1)
  })
  return Array.from(counts.entries()).map(([format, count]) => ({ format, count }))
})

const kpis = computed(() => [
  { label: t('dashboard.kpi.repos'), value: total.value },
  { label: t('dashboard.kpi.formats'), value: uniqueFormats.value.length },
  { label: t('dashboard.kpi.public'), value: repos.value.filter((r) => r.public).length },
  { label: t('dashboard.kpi.proxyGroup'), value: repos.value.filter((r) => r.repo_type === 'proxy' || r.repo_type === 'group').length },
])

const recentRepos = computed(() =>
  [...repos.value]
    .sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''))
    .slice(0, 8),
)

const donutSlices = computed(() => {
  const slices: { color: string; dashArray: string; dashOffset: number }[] = []
  const circumference = 2 * Math.PI * 70
  const sum = total.value || 1
  let offset = 0

  uniqueFormats.value.forEach(({ format, count }) => {
    const arc = (count / sum) * circumference
    slices.push({
      color: formatColor(format),
      dashArray: `${arc.toFixed(2)} ${(circumference - arc).toFixed(2)}`,
      dashOffset: -offset,
    })
    offset += arc
  })

  return slices
})

function timeAgo(iso?: string): string {
  if (!iso) return '—'
  const diff = Date.now() - new Date(iso).getTime()
  const minutes = Math.floor(diff / 60_000)
  if (minutes < 1) return t('dashboard.justNow')
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

function auditDotClass(result?: string): string {
  if (!result || result === 'ok' || result === 'success') return 'bg-success'
  if (result === 'error' || result === 'failed') return 'bg-danger'
  return 'bg-muted'
}

function auditActionClass(action?: string): string {
  if (!action) return 'bg-surface-hover text-muted'
  if (action.includes('delete') || action.includes('remove')) return 'bg-danger-soft text-danger'
  if (action.includes('create') || action.includes('add')) return 'bg-success-soft text-success'
  if (action.includes('update') || action.includes('edit')) return 'bg-primary-soft text-primary'
  return 'bg-surface-hover text-muted'
}

async function loadRepos(): Promise<void> {
  loadError.value = ''
  try {
    repos.value = await listAllRepositories()
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('repos.loadFailed')
  }
}

async function syncNow(): Promise<void> {
  syncing.value = true
  try {
    await loadRepos()
    if (!loadError.value) toasts.success(t('dashboard.refreshed'))
    void loadAudit()
  } finally {
    syncing.value = false
  }
}

async function loadAudit(): Promise<void> {
  if (!session.isAdmin) return
  auditLoading.value = true
  try {
    const res = await listAudit({ limit: 10 })
    auditEntries.value = res.items
  } catch {
    auditEntries.value = []
  } finally {
    auditLoading.value = false
  }
}

onMounted(async () => {
  try {
    await loadRepos()
  } finally {
    loading.value = false
  }
  void loadAudit()
})
</script>

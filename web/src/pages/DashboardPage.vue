<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-4">
    <!-- Header -->
    <PageHeader :title="t('dashboard.title')">
      <template #actions>
        <AppButton variant="secondary" :loading="syncing" @click="syncNow">
          <ArrowPathIcon v-if="!syncing" class="w-4 h-4" aria-hidden="true" />
          {{ t('dashboard.refresh') }}
        </AppButton>
        <AppButton v-if="session.canCreateRepo" to="/repos/new">
          {{ t('repos.new') }}
        </AppButton>
      </template>
    </PageHeader>

    <!-- KPI strip -->
    <section
      class="rounded-lg border border-border bg-border overflow-hidden grid grid-cols-2 lg:grid-cols-5 gap-px"
      :aria-label="t('dashboard.title')"
    >
      <div v-for="kpi in kpis" :key="kpi.label" class="px-5 py-3.5 bg-surface last:col-span-2 lg:last:col-span-1">
        <div class="text-xs text-muted">{{ kpi.label }}</div>
        <div class="mt-1 text-2xl leading-none font-semibold tabular-nums">{{ kpi.value }}</div>
      </div>
    </section>

    <div class="grid grid-cols-1 xl:grid-cols-[1fr_400px] gap-4">
      <div class="space-y-4 min-w-0">
        <!-- Repositories by format -->
        <section class="rounded-lg border border-border bg-surface">
          <div class="px-5 h-11 flex items-center border-b border-border">
            <h2 class="text-ui font-semibold">{{ t('dashboard.byFormat') }}</h2>
          </div>
          <div v-if="loading" class="px-5 py-4 text-sm text-muted">{{ t('common.loading') }}</div>
          <div v-else-if="formatCounts.length === 0" class="px-5 py-4 text-sm text-muted">
            {{ t('dashboard.emptyTitle') }}
          </div>
          <div v-else class="px-5 py-3.5 space-y-2.5">
            <div
              v-for="(item, i) in formatCounts"
              :key="item.format"
              class="grid grid-cols-[110px_1fr_32px] items-center gap-3"
            >
              <FormatBadge :format="item.format" size="sm" />
              <div class="h-2 rounded-full bg-surface-hover overflow-hidden">
                <div
                  class="h-full rounded-full"
                  :style="{ width: `${(item.count / maxCount) * 100}%`, backgroundColor: chartColor(i) }"
                />
              </div>
              <span class="text-xs font-mono text-muted text-right tabular-nums">{{ item.count }}</span>
            </div>
          </div>
        </section>

        <!-- Recent repositories -->
        <section class="rounded-lg border border-border bg-surface overflow-hidden">
          <div class="px-5 h-11 flex items-center justify-between border-b border-border">
            <h2 class="text-ui font-semibold">{{ t('dashboard.recent') }}</h2>
            <router-link to="/repos" class="text-xs text-primary hover:underline">{{ t('dashboard.viewAll') }}</router-link>
          </div>
          <div v-if="loading" class="px-5 py-4 text-sm text-muted">{{ t('common.loading') }}</div>
          <div v-else-if="loadError" class="px-5 py-4 text-sm text-danger" role="alert">{{ loadError }}</div>
          <EmptyState v-else-if="repos.length === 0" :title="t('dashboard.emptyTitle')" :description="t('repos.emptyHint')" />
          <table v-else class="w-full text-sm">
            <thead>
              <tr class="text-xs text-muted border-b border-border">
                <th scope="col" class="text-left font-medium px-5 py-2">{{ t('repos.column.name') }}</th>
                <th scope="col" class="text-left font-medium px-5 py-2">{{ t('repos.column.format') }}</th>
                <th scope="col" class="text-left font-medium px-5 py-2">{{ t('repos.column.type') }}</th>
                <th scope="col" class="text-left font-medium px-5 py-2">{{ t('repos.column.visibility') }}</th>
                <th scope="col" class="text-right font-medium px-5 py-2">{{ t('dashboard.column.created') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="repo in recentRepos"
                :key="repo.id"
                class="h-10 border-b border-border last:border-0 hover:bg-surface-hover transition-colors"
              >
                <td class="px-5 font-mono">
                  <router-link :to="`/repos/${encodeURIComponent(repo.name)}`" class="hover:underline">{{ repo.name }}</router-link>
                </td>
                <td class="px-5"><FormatBadge :format="repo.format" size="sm" /></td>
                <td class="px-5 text-muted">{{ repo.repo_type || 'hosted' }}</td>
                <td class="px-5 text-muted">{{ repo.public ? t('repos.public') : t('repos.private') }}</td>
                <td class="px-5 text-right text-muted tabular-nums">{{ formatDate(repo.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </section>
      </div>

      <!-- Recent activity (admin only) -->
      <section v-if="session.isAdmin" class="rounded-lg border border-border bg-surface self-start">
        <div class="px-5 h-11 flex items-center justify-between border-b border-border">
          <h2 class="text-ui font-semibold">{{ t('dashboard.activity') }}</h2>
          <router-link to="/admin/audit" class="text-xs text-primary hover:underline">{{ t('dashboard.viewAll') }}</router-link>
        </div>
        <div v-if="auditLoading" class="px-5 py-3 space-y-3">
          <div v-for="i in 5" :key="i" class="h-8 rounded skel" />
        </div>
        <div v-else-if="auditEntries.length === 0" class="px-5 py-6 text-center text-sm text-muted">
          {{ t('dashboard.noActivity') }}
        </div>
        <ul v-else class="divide-y divide-border">
          <li v-for="entry in auditEntries" :key="entry.id" class="px-5 py-3 hover:bg-surface-hover transition-colors">
            <div class="flex justify-between gap-3 text-sm">
              <span class="min-w-0 truncate">
                <span class="text-foreground">{{ entry.actor }}</span>
                <span class="ml-1 text-muted">{{ actionLabel(entry.action) }}</span>
              </span>
              <span class="shrink-0 text-xs text-muted tabular-nums">{{ timeAgo(entry.timestamp) }}</span>
            </div>
            <div v-if="entryTarget(entry)" class="mt-0.5 text-xs font-mono text-muted truncate">{{ entryTarget(entry) }}</div>
          </li>
        </ul>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import AppButton from '../components/AppButton.vue'
import PageHeader from '../components/PageHeader.vue'
import { ref, computed, onMounted } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import { listAllRepositories } from '../api/repositories'
import { listAudit } from '../api/audit'
import type { Repository } from '../api/repositories'
import type { AuditEntry } from '../api/audit'
import { useToastStore } from '../stores/toast'
import { useSessionStore } from '../stores/session'
import FormatBadge from '../components/FormatBadge.vue'
import EmptyState from '../components/EmptyState.vue'
import { chartColor } from '../lib/formats'
import { formatDate } from '../lib/datetime'
import { t } from '../i18n/en'

const toasts = useToastStore()
const session = useSessionStore()

const repos = ref<Repository[]>([])
const loadError = ref('')
const loading = ref(true)
const syncing = ref(false)
const auditEntries = ref<AuditEntry[]>([])
const auditLoading = ref(false)

const formatCounts = computed(() => {
  const counts = new Map<string, number>()
  repos.value.forEach((r) => counts.set(r.format, (counts.get(r.format) || 0) + 1))
  return Array.from(counts, ([format, count]) => ({ format, count })).sort((a, b) => b.count - a.count)
})

const maxCount = computed(() => formatCounts.value[0]?.count || 1)

const kpis = computed(() => [
  { label: t('dashboard.kpi.repos'), value: repos.value.length },
  { label: t('dashboard.kpi.formats'), value: formatCounts.value.length },
  { label: t('dashboard.kpi.public'), value: repos.value.filter((r) => r.public).length },
  { label: t('dashboard.kpi.private'), value: repos.value.filter((r) => !r.public).length },
  {
    label: t('dashboard.kpi.proxyGroup'),
    value: repos.value.filter((r) => r.repo_type === 'proxy' || r.repo_type === 'group').length,
  },
])

const recentRepos = computed(() =>
  [...repos.value]
    .sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''))
    .slice(0, 8),
)

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

function actionLabel(action?: string): string {
  return (action ?? '').replace(/[._]/g, ' ')
}

function entryTarget(entry: AuditEntry): string {
  if (entry.target) return entry.target
  const path = entry.details?.path
  return typeof path === 'string' ? path : ''
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
    // Unmapped requests are audited as `http.request`; they belong in the
    // audit log, not in the dashboard's activity summary.
    const res = await listAudit({ limit: 50 })
    auditEntries.value = res.items.filter((e) => e.action !== 'http.request').slice(0, 10)
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

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header -->
    <div class="flex items-start justify-between gap-4 flex-wrap">
      <div>
        <div class="flex items-center gap-1.5 text-[11px] text-muted font-mono">
          <span>{{ t('sec.common.breadcrumb') }}</span>
          <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
          <span class="text-foreground/70">{{ t('sec.license.breadcrumb') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('sec.license.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('sec.license.description') }}</p>
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50"
        :disabled="!selectedRepo"
        @click="openDrawer"
      >
        <ArrowPathIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('sec.license.trigger') }}
      </button>
    </div>

    <!-- Repo selector -->
    <div class="flex items-center gap-3">
      <select
        v-model="selectedRepo"
        :aria-label="t('sec.common.selectRepo')"
        class="h-9 px-3 rounded-lg bg-surface border border-border text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
      >
        <option value="">{{ t('sec.common.selectRepo') }}</option>
        <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }}</option>
      </select>
      <button
        type="button"
        :aria-label="t('common.refresh')"
        class="flex items-center justify-center w-9 h-9 rounded-lg border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50"
        :disabled="loadingScans || !selectedRepo"
        @click="loadScans"
      >
        <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': loadingScans }" aria-hidden="true" />
      </button>
    </div>

    <!-- Stat tiles (derived from the loaded scans) -->
    <div class="grid grid-cols-3 gap-4">
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-[10px] font-semibold text-muted">{{ t('sec.license.totalScans') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums">{{ scans.length }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-[10px] font-semibold text-muted">{{ t('sec.license.distinct') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-success">{{ distinctLicenses }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-[10px] font-semibold text-muted">{{ t('sec.license.noLicense') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-warning">{{ withoutLicense }}</div>
      </div>
    </div>

    <!-- Filter toolbar -->
    <div class="flex items-center gap-3 flex-wrap">
      <div class="relative flex-1 max-w-sm">
        <MagnifyingGlassIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" aria-hidden="true" />
        <input
          v-model="searchQuery"
          type="search"
          :aria-label="t('sec.license.search')"
          :placeholder="t('sec.license.search')"
          class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
      </div>
      <div role="group" :aria-label="t('sec.common.filterStatus')" class="flex items-center gap-1 bg-surface-hover/60 rounded-lg p-0.5">
        <button
          v-for="r in statusOptions"
          :key="r.value"
          type="button"
          :aria-pressed="statusFilter === r.value"
          class="px-3 py-1.5 rounded-md text-xs font-medium transition-colors"
          :class="statusFilter === r.value ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
          @click="statusFilter = r.value"
        >{{ r.label }}</button>
      </div>
    </div>

    <!-- Scans table -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('sec.license.results') }}</h3>
        <span class="text-[11px] font-mono text-muted">{{ filteredScans.length }} {{ t('sec.common.scans') }}</span>
      </div>

      <div v-if="loadingScans" class="py-12 flex flex-col items-center gap-3 text-muted" role="status">
        <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
        <span class="text-sm">{{ t('sec.common.loadingScans') }}</span>
      </div>

      <div v-else-if="loadError" class="py-12 text-center" role="alert">
        <p class="text-sm text-danger">{{ loadError }}</p>
        <button type="button" class="mt-3 px-3 py-1.5 rounded-lg text-sm border border-border hover:bg-surface-hover" @click="loadScans">
          {{ t('sec.common.retry') }}
        </button>
      </div>

      <EmptyState
        v-else-if="!selectedRepo"
        :title="t('sec.common.selectRepoTitle')"
        :description="t('sec.license.selectRepoDesc')"
      />

      <EmptyState
        v-else-if="filteredScans.length === 0"
        :title="t('sec.license.emptyTitle')"
        :description="t('sec.license.emptyDesc')"
      />

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="border-b border-border bg-surface-hover/40">
            <tr>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.artifact') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.license.licenses') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.status') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.scanned') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="scan in filteredScans"
              :key="scan.id"
              class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0"
            >
              <td class="px-4 py-3 font-mono text-[13px] text-foreground">
                <span class="inline-flex items-center gap-2">
                  <FormatBadge v-if="formatOf(scan.artifact_id)" :format="formatOf(scan.artifact_id)" size="sm" />
                  {{ packageLabel(scan.artifact_id) }}
                </span>
              </td>
              <td class="px-4 py-3">
                <div v-if="scan.licenses?.length" class="flex flex-wrap gap-1">
                  <span
                    v-for="lic in scan.licenses"
                    :key="lic"
                    class="inline-flex items-center px-2 py-0.5 rounded-md text-[11px] font-mono border border-border text-foreground/80"
                  >{{ lic }}</span>
                </div>
                <span v-else class="text-xs text-muted">{{ t('sec.license.none') }}</span>
              </td>
              <td class="px-4 py-3">
                <span :class="scanStatusClass(scan.scan_status)" :title="scan.scan_error ?? undefined">{{ statusLabel(scan.scan_status) }}</span>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDate(scan.scanned_at || scan.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Trigger drawer -->
    <AdminDialog
      :open="showDrawer"
      drawer
      max-width="max-w-md"
      :title="t('sec.license.drawerTitle')"
      :description="t('sec.license.drawerHint')"
      @close="showDrawer = false"
    >
      <div>
        <label for="license-pkg" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.common.package') }}</label>
        <select
          id="license-pkg"
          v-model="artifactId"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
        >
          <option value="" disabled>{{ t('sec.common.selectPackage') }}</option>
          <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version }}</option>
        </select>
        <p v-if="!packages.length" class="text-xs text-muted mt-1">{{ t('sec.common.noPackages') }}</p>
      </div>
      <template #footer>
        <button type="button" class="px-4 py-2 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors" @click="showDrawer = false">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="flex items-center gap-2 px-4 py-2 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50"
          :disabled="triggering || !artifactId"
          @click="triggerScan"
        >
          <ArrowPathIcon class="w-3.5 h-3.5" :class="{ 'animate-spin': triggering }" aria-hidden="true" />
          {{ t('sec.license.start') }}
        </button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { ChevronRightIcon, ArrowPathIcon, MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import { useToastStore } from '../../stores/toast'
import { useRepoPackages } from '../../composables/useRepoPackages'
import { listLicenseScans, triggerLicenseScan, type LicenseScan } from '../../api/security'
import { t, tDynamic } from '../../i18n/en'

const toasts = useToastStore()
const { repos, selectedRepo, packages, packageLabel, findPackage } = useRepoPackages()

const scans = ref<LicenseScan[]>([])
const loadingScans = ref(false)
const loadError = ref('')
const triggering = ref(false)
const showDrawer = ref(false)
const artifactId = ref('')
const searchQuery = ref('')
const statusFilter = ref('all')
let loadSeq = 0

const statusOptions = computed(() => [
  { label: t('sec.common.all'), value: 'all' },
  { label: t('sec.license.status.pending'), value: 'pending' },
  { label: t('sec.license.status.completed'), value: 'completed' },
  { label: t('sec.license.status.failed'), value: 'failed' },
])

const distinctLicenses = computed(() => new Set(scans.value.flatMap((s) => s.licenses ?? [])).size)
const withoutLicense = computed(
  () => scans.value.filter((s) => s.scan_status === 'completed' && !s.licenses?.length).length,
)

const filteredScans = computed(() => {
  let result = scans.value
  const q = searchQuery.value.trim().toLowerCase()
  if (q) {
    result = result.filter(
      (s) =>
        packageLabel(s.artifact_id).toLowerCase().includes(q) ||
        (s.licenses ?? []).some((l) => l.toLowerCase().includes(q)),
    )
  }
  if (statusFilter.value !== 'all') result = result.filter((s) => s.scan_status === statusFilter.value)
  return result
})

function formatOf(id: string): string {
  return findPackage(id)?.format ?? ''
}

function scanStatusClass(status: string): string {
  const base = 'inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[11px] font-semibold '
  if (status === 'failed') return base + 'text-danger bg-danger-soft/60'
  if (status === 'completed') return base + 'text-success bg-success-soft/60'
  return base + 'text-warning bg-warning-soft/60'
}

function statusLabel(status: string): string {
  return ['pending', 'completed', 'failed'].includes(status) ? tDynamic(`sec.license.status.${status}`) : status
}

function formatDate(ts?: string | null): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

async function loadScans(): Promise<void> {
  const repo = selectedRepo.value
  if (!repo) {
    scans.value = []
    loadError.value = ''
    return
  }
  const seq = ++loadSeq
  loadingScans.value = true
  try {
    const result = await listLicenseScans(repo)
    if (seq !== loadSeq) return
    scans.value = result
    loadError.value = ''
  } catch (err) {
    if (seq !== loadSeq) return
    scans.value = []
    loadError.value = err instanceof Error ? err.message : t('sec.license.loadFailed')
  } finally {
    if (seq === loadSeq) loadingScans.value = false
  }
}

function openDrawer(): void {
  artifactId.value = ''
  showDrawer.value = true
}

async function triggerScan(): Promise<void> {
  if (!selectedRepo.value || !artifactId.value || triggering.value) return
  triggering.value = true
  try {
    await triggerLicenseScan(selectedRepo.value, artifactId.value)
    toasts.success(t('sec.license.started'))
    showDrawer.value = false
    await loadScans()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.license.startFailed'))
  } finally {
    triggering.value = false
  }
}

watch(selectedRepo, () => void loadScans())
</script>

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
          <span class="text-foreground/70">{{ t('sec.vuln.breadcrumb') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('sec.vuln.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('sec.vuln.description') }}</p>
      </div>
      <div class="flex items-center gap-3 flex-wrap">
        <div
          role="radiogroup"
          :aria-label="t('sec.vuln.scannerLabel')"
          class="flex items-center gap-1 bg-surface-hover/60 rounded-lg p-0.5 border border-border"
        >
          <button
            v-for="s in scanners"
            :key="s.value"
            type="button"
            role="radio"
            :aria-checked="activeScanner === s.value"
            class="px-3 py-1.5 rounded-md text-xs font-medium transition-colors"
            :class="activeScanner === s.value ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
            @click="activeScanner = s.value"
          >{{ s.label }}</button>
        </div>
        <button
          type="button"
          class="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50"
          :disabled="!selectedRepo"
          @click="openDrawer"
        >
          <ShieldCheckIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('sec.vuln.scanPackage') }}
        </button>
      </div>
    </div>

    <!-- Repo selector + refresh -->
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
        @click="loadScans()"
      >
        <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': loadingScans }" aria-hidden="true" />
      </button>
    </div>

    <!-- Stats tiles -->
    <div class="grid grid-cols-2 sm:grid-cols-4 gap-4">
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-[10px] font-semibold text-muted">{{ t('sec.vuln.totalScans') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums">{{ scans.length }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-[10px] font-semibold text-muted">{{ t('sec.vuln.critical') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-danger">{{ totalCritical }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-[10px] font-semibold text-muted">{{ t('sec.vuln.high') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-warning">{{ totalHigh }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-[10px] font-semibold text-muted">{{ t('sec.vuln.medium') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-[#f59e0b]">{{ totalMedium }}</div>
      </div>
    </div>

    <!-- Severity distribution bar -->
    <div class="bg-surface rounded-xl border border-border p-5">
      <div class="flex items-center justify-between mb-3">
        <h3 class="text-sm font-semibold">{{ t('sec.vuln.distribution') }}</h3>
        <span class="text-xs text-muted font-mono">{{ totalVulns }} {{ t('sec.vuln.totalVulns') }}</span>
      </div>
      <div class="flex h-2.5 rounded-full overflow-hidden bg-surface-hover" aria-hidden="true">
        <template v-if="totalVulns > 0">
          <div class="h-full bg-danger transition-all" :style="{ width: `${(totalCritical / totalVulns) * 100}%` }" />
          <div class="h-full bg-warning transition-all" :style="{ width: `${(totalHigh / totalVulns) * 100}%` }" />
          <div class="h-full bg-[#f59e0b] transition-all" :style="{ width: `${(totalMedium / totalVulns) * 100}%` }" />
          <div class="h-full bg-muted/40 transition-all" :style="{ width: `${(totalLow / totalVulns) * 100}%` }" />
        </template>
      </div>
      <div class="flex items-center gap-4 mt-3 text-xs text-muted flex-wrap">
        <span class="flex items-center gap-1.5"><span class="w-2 h-2 rounded-full bg-danger" /> {{ t('sec.vuln.critical') }}: {{ totalCritical }}</span>
        <span class="flex items-center gap-1.5"><span class="w-2 h-2 rounded-full bg-warning" /> {{ t('sec.vuln.high') }}: {{ totalHigh }}</span>
        <span class="flex items-center gap-1.5"><span class="w-2 h-2 rounded-full bg-[#f59e0b]" /> {{ t('sec.vuln.medium') }}: {{ totalMedium }}</span>
        <span class="flex items-center gap-1.5"><span class="w-2 h-2 rounded-full bg-muted/40" /> {{ t('sec.vuln.low') }}: {{ totalLow }}</span>
      </div>
    </div>

    <!-- Filter toolbar -->
    <div class="flex items-center gap-3 flex-wrap">
      <div class="relative flex-1 max-w-sm">
        <MagnifyingGlassIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" aria-hidden="true" />
        <input
          v-model="searchQuery"
          type="search"
          :aria-label="t('sec.vuln.searchPackages')"
          :placeholder="t('sec.vuln.searchPackages')"
          class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
      </div>
      <div role="group" :aria-label="t('sec.vuln.filterSeverity')" class="flex items-center gap-1 bg-surface-hover/60 rounded-lg p-0.5">
        <button
          v-for="s in severityOptions"
          :key="s.value"
          type="button"
          :aria-pressed="severityFilter === s.value"
          class="px-3 py-1.5 rounded-md text-xs font-medium transition-colors"
          :class="severityFilter === s.value ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
          @click="severityFilter = s.value"
        >{{ s.label }}</button>
      </div>
      <div role="group" :aria-label="t('sec.vuln.filterStatus')" class="flex items-center gap-1 bg-surface-hover/60 rounded-lg p-0.5">
        <button
          v-for="s in statusOptions"
          :key="s.value"
          type="button"
          :aria-pressed="statusFilter === s.value"
          class="px-3 py-1.5 rounded-md text-xs font-medium transition-colors"
          :class="statusFilter === s.value ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
          @click="statusFilter = s.value"
        >{{ s.label }}</button>
      </div>
    </div>

    <!-- Scans table -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('sec.vuln.results') }}</h3>
        <span class="text-[11px] font-mono text-muted">{{ filteredScans.length }} {{ t('sec.common.scans') }}</span>
      </div>

      <div v-if="loadingScans" class="py-12 flex flex-col items-center gap-3 text-muted" role="status">
        <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
        <span class="text-sm">{{ t('sec.common.loadingScans') }}</span>
      </div>

      <div v-else-if="loadError" class="py-12 text-center" role="alert">
        <p class="text-sm text-danger">{{ loadError }}</p>
        <button type="button" class="mt-3 px-3 py-1.5 rounded-lg text-sm border border-border hover:bg-surface-hover" @click="loadScans()">
          {{ t('sec.common.retry') }}
        </button>
      </div>

      <EmptyState
        v-else-if="!selectedRepo"
        :title="t('sec.common.selectRepoTitle')"
        :description="t('sec.vuln.selectRepoDesc')"
      />

      <EmptyState
        v-else-if="filteredScans.length === 0"
        :title="t('sec.vuln.emptyTitle')"
        :description="t('sec.vuln.emptyDesc')"
      />

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="border-b border-border bg-surface-hover/40">
            <tr>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.package') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.version') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.ecosystem') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.scanner') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.vulns') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.status') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.scanned') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="scan in filteredScans"
              :key="scan.id"
              class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0"
            >
              <td class="px-4 py-3 font-mono text-[13px] font-medium text-foreground">{{ scan.package_name }}</td>
              <td class="px-4 py-3 font-mono text-[13px] text-muted">{{ scan.package_version }}</td>
              <td class="px-4 py-3"><FormatBadge :format="scan.ecosystem" size="sm" /></td>
              <td class="px-4 py-3">
                <span class="inline-flex items-center px-2 py-0.5 rounded-md text-[11px] font-semibold border border-border text-muted">{{ scan.scanner || 'osv' }}</span>
              </td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-1.5 text-xs font-mono">
                  <span v-if="scan.critical_count > 0" class="px-1.5 py-0.5 rounded bg-danger-soft text-danger font-semibold">{{ scan.critical_count }}C</span>
                  <span v-if="scan.high_count > 0" class="px-1.5 py-0.5 rounded bg-warning-soft text-warning font-semibold">{{ scan.high_count }}H</span>
                  <span v-if="scan.medium_count > 0" class="px-1.5 py-0.5 rounded bg-[#f59e0b]/10 text-[#f59e0b] font-semibold">{{ scan.medium_count }}M</span>
                  <span v-if="scan.low_count > 0" class="px-1.5 py-0.5 rounded bg-surface-hover text-muted font-semibold">{{ scan.low_count }}L</span>
                  <span v-if="scan.vuln_count === 0 && scan.scan_status === 'completed'" class="text-success text-xs">{{ t('sec.vuln.clean') }}</span>
                  <span v-if="scan.scan_status === 'scanning'" class="text-muted text-xs">—</span>
                </div>
              </td>
              <td class="px-4 py-3">
                <span :class="scanStatusClass(scan.scan_status)" :title="scan.scan_error ?? undefined">
                  <ArrowPathIcon v-if="scan.scan_status === 'scanning'" class="w-3 h-3 animate-spin" aria-hidden="true" />
                  {{ statusLabel(scan.scan_status) }}
                </span>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDate(scan.scanned_at || scan.created_at) }}</td>
              <td class="px-4 py-3">
                <button
                  type="button"
                  class="px-2 py-1 rounded-md text-[11px] font-medium border border-border text-muted hover:bg-surface-hover hover:text-foreground transition-colors disabled:opacity-50"
                  :class="{ 'border-danger/30 text-danger bg-danger-soft/40': scan.quarantined }"
                  :disabled="quarantining === scan.id"
                  :aria-label="`${scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine')}: ${scan.package_name}`"
                  @click="toggleQuarantine(scan)"
                >
                  {{ scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Scan drawer -->
    <AdminDialog
      :open="showDrawer"
      drawer
      max-width="max-w-md"
      :title="t('sec.vuln.drawerTitle')"
      :description="`${t('sec.vuln.poweredBy')} ${activeScannerLabel}`"
      @close="showDrawer = false"
    >
      <div class="space-y-4">
        <div>
          <label for="vuln-pkg" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.common.package') }}</label>
          <select
            id="vuln-pkg"
            v-model="form.artifactId"
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
            @change="onPackageChange"
          >
            <option value="" disabled>{{ t('sec.common.selectPackage') }}</option>
            <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version }}</option>
          </select>
          <p v-if="!packages.length" class="text-xs text-muted mt-1">{{ t('sec.common.noPackages') }}</p>
        </div>
        <div>
          <label for="vuln-eco" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.vuln.col.ecosystem') }}</label>
          <select
            id="vuln-eco"
            v-model="form.ecosystem"
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
          >
            <option value="" disabled>{{ t('sec.vuln.selectEcosystem') }}</option>
            <option v-for="e in VULN_ECOSYSTEMS" :key="e" :value="e">{{ formatLabel(e) }}</option>
          </select>
          <p class="text-xs text-muted mt-1">{{ t('sec.vuln.ecosystemHint') }}</p>
        </div>
      </div>
      <template #footer>
        <button type="button" class="px-4 py-2 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors" @click="showDrawer = false">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="flex items-center gap-2 px-4 py-2 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50"
          :disabled="scanning || !form.artifactId || !form.ecosystem"
          @click="startScan"
        >
          <ArrowPathIcon v-if="scanning" class="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
          <ShieldCheckIcon v-else class="w-3.5 h-3.5" aria-hidden="true" />
          {{ scanning ? t('sec.vuln.starting') : t('sec.vuln.start') }}
        </button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { ChevronRightIcon, ArrowPathIcon, MagnifyingGlassIcon, ShieldCheckIcon } from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import { useToastStore } from '../../stores/toast'
import { useRepoPackages } from '../../composables/useRepoPackages'
import {
  listVulnScans,
  startVulnScan,
  setVulnQuarantine,
  VULN_ECOSYSTEMS,
  type VulnScan,
  type VulnScanner,
} from '../../api/security'
import { formatLabel } from '../../lib/formats'
import { t, tDynamic } from '../../i18n/en'

const POLL_MS = 3000

const toasts = useToastStore()
const { repos, selectedRepo, packages, findPackage } = useRepoPackages()

const scans = ref<VulnScan[]>([])
const loadingScans = ref(false)
const loadError = ref('')
const showDrawer = ref(false)
const scanning = ref(false)
const quarantining = ref<string | null>(null)
const searchQuery = ref('')
const severityFilter = ref('all')
const statusFilter = ref('all')
const activeScanner = ref<VulnScanner>('osv')

const scanners: { value: VulnScanner; label: string }[] = [
  { value: 'osv', label: 'OSV-Scanner' },
  { value: 'trivy', label: 'Trivy' },
  { value: 'grype', label: 'Grype' },
]
const activeScannerLabel = computed(() => scanners.find((s) => s.value === activeScanner.value)?.label ?? '')

const form = ref({ artifactId: '', ecosystem: '' })

const severityOptions = computed(() => [
  { label: t('sec.common.all'), value: 'all' },
  { label: t('sec.vuln.critical'), value: 'critical' },
  { label: t('sec.vuln.high'), value: 'high' },
  { label: t('sec.vuln.medium'), value: 'medium' },
  { label: t('sec.vuln.low'), value: 'low' },
])

const statusOptions = computed(() => [
  { label: t('sec.common.all'), value: 'all' },
  { label: t('sec.vuln.status.scanning'), value: 'scanning' },
  { label: t('sec.vuln.status.completed'), value: 'completed' },
  { label: t('sec.vuln.status.failed'), value: 'failed' },
])

const totalCritical = computed(() => scans.value.reduce((s, r) => s + (r.critical_count || 0), 0))
const totalHigh = computed(() => scans.value.reduce((s, r) => s + (r.high_count || 0), 0))
const totalMedium = computed(() => scans.value.reduce((s, r) => s + (r.medium_count || 0), 0))
const totalLow = computed(() => scans.value.reduce((s, r) => s + (r.low_count || 0), 0))
const totalVulns = computed(() => totalCritical.value + totalHigh.value + totalMedium.value + totalLow.value)

const filteredScans = computed(() => {
  let result = scans.value
  const q = searchQuery.value.trim().toLowerCase()
  if (q) result = result.filter((s) => s.package_name.toLowerCase().includes(q))
  if (statusFilter.value !== 'all') result = result.filter((s) => s.scan_status === statusFilter.value)
  if (severityFilter.value !== 'all') {
    const key = `${severityFilter.value}_count` as 'critical_count' | 'high_count' | 'medium_count' | 'low_count'
    result = result.filter((s) => s[key] > 0)
  }
  return result
})

function scanStatusClass(status: string): string {
  const base = 'inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[11px] font-semibold '
  if (status === 'scanning') return base + 'text-warning bg-warning-soft/60'
  if (status === 'failed') return base + 'text-danger bg-danger-soft/60'
  return base + 'text-success bg-success-soft/60'
}

function statusLabel(status: string): string {
  return ['scanning', 'completed', 'failed'].includes(status) ? tDynamic(`sec.vuln.status.${status}`) : status
}

function formatDate(ts?: string | null): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

let pollTimer: ReturnType<typeof setTimeout> | undefined
let loadSeq = 0

function schedulePoll(): void {
  clearTimeout(pollTimer)
  if (scans.value.some((s) => s.scan_status === 'scanning')) {
    pollTimer = setTimeout(() => void loadScans(true), POLL_MS)
  }
}

// `silent` refreshes (polling) keep the table visible instead of flashing the spinner.
async function loadScans(silent = false): Promise<void> {
  const repo = selectedRepo.value
  clearTimeout(pollTimer)
  if (!repo) {
    scans.value = []
    loadError.value = ''
    return
  }
  const seq = ++loadSeq
  if (!silent) loadingScans.value = true
  try {
    const result = await listVulnScans(repo)
    if (seq !== loadSeq) return
    scans.value = result
    loadError.value = ''
  } catch (err) {
    if (seq !== loadSeq) return
    if (!silent) {
      scans.value = []
      loadError.value = err instanceof Error ? err.message : t('sec.vuln.loadFailed')
    }
  } finally {
    if (seq === loadSeq) {
      loadingScans.value = false
      schedulePoll()
    }
  }
}

function openDrawer(): void {
  form.value = { artifactId: '', ecosystem: '' }
  showDrawer.value = true
}

function onPackageChange(): void {
  const fmt = findPackage(form.value.artifactId)?.format?.toLowerCase() ?? ''
  form.value.ecosystem = (VULN_ECOSYSTEMS as readonly string[]).includes(fmt) ? fmt : ''
}

async function startScan(): Promise<void> {
  const pkg = findPackage(form.value.artifactId)
  if (!pkg || !selectedRepo.value || scanning.value) return
  scanning.value = true
  try {
    await startVulnScan(selectedRepo.value, {
      artifact_id: pkg.id,
      package_name: pkg.name,
      package_version: pkg.version,
      ecosystem: form.value.ecosystem,
      scanner: activeScanner.value,
    })
    toasts.success(t('sec.vuln.started'))
    showDrawer.value = false
    await loadScans()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vuln.startFailed'))
  } finally {
    scanning.value = false
  }
}

async function toggleQuarantine(scan: VulnScan): Promise<void> {
  if (quarantining.value) return
  quarantining.value = scan.id
  try {
    const updated = await setVulnQuarantine(selectedRepo.value, scan.id, !scan.quarantined)
    scans.value = scans.value.map((s) => (s.id === scan.id ? { ...s, quarantined: updated.quarantined } : s))
    toasts.success(updated.quarantined ? t('sec.vuln.quarantined') : t('sec.vuln.quarantineRemoved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vuln.quarantineFailed'))
  } finally {
    quarantining.value = null
  }
}

watch(selectedRepo, () => void loadScans())
onBeforeUnmount(() => {
  clearTimeout(pollTimer)
  loadSeq++
})
</script>

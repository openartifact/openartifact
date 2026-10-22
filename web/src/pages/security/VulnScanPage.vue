<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header -->
    <PageHeader :title="t('sec.vuln.title')" :description="t('sec.vuln.description')">
      <template #actions>
        <div class="flex items-center gap-3 flex-wrap">
          <AppButton variant="primary" size="md" :disabled="!selectedRepo" @click="openDrawer">
            <ShieldCheckIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('sec.vuln.scanPackage') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <!-- Repo selector + refresh -->
    <div class="flex items-center gap-3">
      <select
        v-model="selectedRepo"
        :aria-label="t('sec.common.selectRepo')"
        class="h-9 px-3 rounded-lg bg-surface border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
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
        <div class="text-micro font-semibold text-muted">{{ t('sec.vuln.totalScans') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums">{{ scans.length }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.vuln.critical') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-danger">{{ totalCritical }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.vuln.high') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-warning">{{ totalHigh }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.vuln.medium') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-severity-medium">{{ totalMedium }}</div>
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
          <div class="h-full bg-severity-medium transition-all" :style="{ width: `${(totalMedium / totalVulns) * 100}%` }" />
          <div class="h-full bg-muted/40 transition-all" :style="{ width: `${(totalLow / totalVulns) * 100}%` }" />
        </template>
      </div>
      <div class="flex items-center gap-4 mt-3 text-xs text-muted flex-wrap">
        <span class="flex items-center gap-1.5"><span class="w-2 h-2 rounded-full bg-danger" /> {{ t('sec.vuln.critical') }}: {{ totalCritical }}</span>
        <span class="flex items-center gap-1.5"><span class="w-2 h-2 rounded-full bg-warning" /> {{ t('sec.vuln.high') }}: {{ totalHigh }}</span>
        <span class="flex items-center gap-1.5"><span class="w-2 h-2 rounded-full bg-severity-medium" /> {{ t('sec.vuln.medium') }}: {{ totalMedium }}</span>
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
          class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border-strong text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
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
        <span class="text-caption font-mono text-muted">{{ filteredScans.length }} {{ t('sec.common.scans') }}</span>
      </div>

      <div v-if="loadingScans" class="py-12 flex flex-col items-center gap-3 text-muted" role="status">
        <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
        <span class="text-sm">{{ t('sec.common.loadingScans') }}</span>
      </div>

      <div v-else-if="loadError" class="py-12 text-center" role="alert">
        <p class="text-sm text-danger">{{ loadError }}</p>
        <AppButton variant="secondary" size="sm" @click="loadScans()">
          {{ t('sec.common.retry') }}
        </AppButton>
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
              <td class="px-4 py-3 font-mono text-ui font-medium text-foreground">{{ scan.package_name }}</td>
              <td class="px-4 py-3 font-mono text-ui text-muted">{{ scan.package_version }}</td>
              <td class="px-4 py-3"><FormatBadge :format="scan.ecosystem" size="sm" /></td>
              <td class="px-4 py-3">
                <span class="inline-flex items-center px-2 py-0.5 rounded-md text-caption font-semibold border border-border text-muted">{{ scan.scanner || 'osv' }}</span>
              </td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-1.5 text-xs font-mono" :data-counts="vulnCountsState(scan)">
                  <template v-if="vulnCountsState(scan) === 'counts'">
                    <span v-if="scan.critical_count > 0" class="px-1.5 py-0.5 rounded bg-danger-soft text-danger font-semibold">{{ scan.critical_count }}C</span>
                    <span v-if="scan.high_count > 0" class="px-1.5 py-0.5 rounded bg-warning-soft text-warning font-semibold">{{ scan.high_count }}H</span>
                    <span v-if="scan.medium_count > 0" class="px-1.5 py-0.5 rounded bg-severity-medium/10 text-severity-medium font-semibold">{{ scan.medium_count }}M</span>
                    <span v-if="scan.low_count > 0" class="px-1.5 py-0.5 rounded bg-surface-hover text-muted font-semibold">{{ scan.low_count }}L</span>
                  </template>
                  <span v-else-if="vulnCountsState(scan) === 'clean'" class="text-success text-xs">{{ t('sec.vuln.clean') }}</span>
                  <span v-else-if="vulnCountsState(scan) === 'partialEmpty'" class="text-warning text-xs">{{ t('sec.vuln.partialEmpty') }}</span>
                  <span v-else-if="vulnCountsState(scan) === 'notScanned'" class="text-danger text-xs">{{ t('sec.vuln.notScanned') }}</span>
                  <span v-else class="text-muted text-xs">—</span>
                </div>
              </td>
              <td class="px-4 py-3">
                <div class="flex items-start gap-1.5">
                  <ArrowPathIcon v-if="scan.scan_status === 'scanning'" class="w-3 h-3 mt-1 animate-spin text-warning" aria-hidden="true" />
                  <VulnScanStatus :status="scan.scan_status" :scan-error="scan.scan_error" :failed-scanners="scan.failed_scanners" />
                </div>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDateTime(scan.scanned_at || scan.created_at) }}</td>
              <td class="px-4 py-3">
                <AppButton variant="secondary" size="xs" :class="{ 'border-danger/30 text-danger bg-danger-soft/40': scan.quarantined }" :disabled="quarantining === scan.id" :aria-label="`${scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine')}: ${scan.package_name}`" @click="toggleQuarantine(scan)">
                  {{ scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine') }}
                </AppButton>
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
      :description="`${t('sec.vuln.poweredBy')} ${selectedScannerLabels}`"
      @close="showDrawer = false"
    >
      <div class="space-y-4">
        <fieldset>
          <legend class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.vuln.scannersLabel') }}</legend>
          <div class="flex items-center gap-4 flex-wrap">
            <label v-for="s in scanners" :key="s.value" class="inline-flex items-center gap-2 text-sm text-foreground">
              <input
                v-model="selectedScanners"
                type="checkbox"
                :value="s.value"
                :data-testid="`scanner-${s.value}`"
                class="h-4 w-4 rounded border-border-strong accent-primary"
              />
              {{ s.label }}
            </label>
          </div>
          <p class="text-xs text-muted mt-1">
            {{ selectedScanners.length ? t('sec.vuln.scannersHint') : t('sec.vuln.scannersRequired') }}
          </p>
        </fieldset>
        <div>
          <label for="vuln-pkg" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.common.package') }}</label>
          <select
            id="vuln-pkg"
            v-model="form.artifactId"
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
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
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
          >
            <option value="" disabled>{{ t('sec.vuln.selectEcosystem') }}</option>
            <option v-for="e in VULN_ECOSYSTEMS" :key="e" :value="e">{{ formatLabel(e) }}</option>
          </select>
          <p class="text-xs text-muted mt-1">{{ t('sec.vuln.ecosystemHint') }}</p>
        </div>
      </div>
      <template #footer>
        <AppButton variant="secondary" size="md" @click="showDrawer = false">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="md" :loading="scanning" :disabled="scanning || !form.artifactId || !form.ecosystem || !selectedScanners.length" @click="startScan">
          <ShieldCheckIcon v-if="!(scanning)" class="w-3.5 h-3.5" aria-hidden="true" />
          {{ scanning ? t('sec.vuln.starting') : t('sec.vuln.start') }}
        </AppButton>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../components/AppButton.vue'
import PageHeader from '../../components/PageHeader.vue'
import { formatDateTime } from '../../lib/datetime'
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { ArrowPathIcon, MagnifyingGlassIcon, ShieldCheckIcon } from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import VulnScanStatus from '../../components/VulnScanStatus.vue'
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
import { normalizeScannerSelection, vulnCountsState } from '../../lib/scanStatus'
import { t } from '../../i18n/en'

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
const selectedScanners = ref<VulnScanner[]>(['osv'])

const scanners: { value: VulnScanner; label: string }[] = [
  { value: 'osv', label: 'OSV-Scanner' },
  { value: 'trivy', label: 'Trivy' },
  { value: 'grype', label: 'Grype' },
]
const selectedScannerLabels = computed(() =>
  scanners
    .filter((s) => selectedScanners.value.includes(s.value))
    .map((s) => s.label)
    .join(', '),
)

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
  { label: t('sec.vuln.status.partial'), value: 'partial' },
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
  // Counted before the early return: a response still in flight for the
  // previous repository must not land after the selection was cleared.
  const seq = ++loadSeq
  if (!repo) {
    scans.value = []
    loadError.value = ''
    loadingScans.value = false
    return
  }
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
  selectedScanners.value = ['osv']
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
      scanners: normalizeScannerSelection(selectedScanners.value),
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

watch(selectedRepo, () => {
  // Another repository's scans must not stay on screen while this one loads.
  scans.value = []
  void loadScans()
})
onBeforeUnmount(() => {
  clearTimeout(pollTimer)
  loadSeq++
})
</script>

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
          <ScanExportButtons :url="exportUrl" />
          <AppButton variant="primary" size="md" @click="openDrawer">
            <ShieldCheckIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('sec.vuln.scanPackage') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <!-- Summary cards (respect the repository filter only; click to filter) -->
    <section :aria-label="t('sec.vuln.summaryLabel')" class="space-y-3">
      <p v-if="summaryError" class="text-sm text-danger" role="alert">{{ summaryError }}</p>
      <div class="grid grid-cols-2 sm:grid-cols-5 gap-3">
        <div class="rounded-xl border border-border bg-surface p-4" data-testid="summary-total">
          <div class="text-micro font-semibold text-muted">{{ t('sec.vuln.totalScans') }}</div>
          <div class="text-2xl font-bold mt-1 tabular-nums">{{ summary ? summary.total : '—' }}</div>
        </div>
        <button
          v-for="card in summaryCards"
          :key="card.testId"
          type="button"
          :data-testid="card.testId"
          :aria-pressed="isCardActive(card)"
          class="rounded-xl border bg-surface p-4 text-left transition-colors hover:bg-surface-hover/40 focus:outline-none focus:ring-2 focus:ring-primary/50"
          :class="isCardActive(card) ? 'border-primary ring-1 ring-primary/50' : 'border-border'"
          @click="toggleCard(card)"
        >
          <div class="text-micro font-semibold text-muted">{{ card.label }}</div>
          <div class="text-2xl font-bold mt-1 tabular-nums" :class="card.tone">{{ card.count === undefined ? '—' : card.count }}</div>
        </button>
      </div>
    </section>

    <!-- Filter toolbar -->
    <div class="flex items-center gap-3 flex-wrap">
      <select
        :value="filters.repo"
        :aria-label="t('sec.vuln.filterRepo')"
        :class="selectClass"
        data-testid="filter-repo"
        @change="onSelect('repo', $event)"
      >
        <option value="">{{ t('sec.vuln.allRepos') }}</option>
        <option v-if="filters.repo && !repos.some((r) => r.name === filters.repo)" :value="filters.repo">{{ filters.repo }}</option>
        <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }}</option>
      </select>
      <select
        :value="filters.severity"
        :aria-label="t('sec.vuln.filterSeverity')"
        :class="selectClass"
        data-testid="filter-severity"
        @change="onSelect('severity', $event)"
      >
        <option value="">{{ t('sec.vuln.anySeverity') }}</option>
        <option v-for="s in VULN_SEVERITY_FILTERS" :key="s" :value="s">{{ tDynamic(`sec.vuln.atLeast.${s}`) }}</option>
      </select>
      <select
        :value="filters.status"
        :aria-label="t('sec.vuln.filterStatus')"
        :class="selectClass"
        data-testid="filter-status"
        @change="onSelect('status', $event)"
      >
        <option value="">{{ t('sec.vuln.anyStatus') }}</option>
        <option v-for="s in VULN_STATUS_FILTERS" :key="s" :value="s">{{ vulnScanLabel(s) }}</option>
      </select>
      <select
        :value="filters.scanner"
        :aria-label="t('sec.vuln.filterScanner')"
        :class="selectClass"
        data-testid="filter-scanner"
        @change="onSelect('scanner', $event)"
      >
        <option value="">{{ t('sec.vuln.anyScanner') }}</option>
        <option v-for="s in scanners" :key="s.value" :value="s.value">{{ s.label }}</option>
      </select>
      <select
        :value="filters.quarantined"
        :aria-label="t('sec.vuln.filterQuarantine')"
        :class="selectClass"
        data-testid="filter-quarantined"
        @change="onSelect('quarantined', $event)"
      >
        <option value="">{{ t('sec.vuln.anyQuarantine') }}</option>
        <option value="true">{{ t('sec.vuln.onlyQuarantined') }}</option>
        <option value="false">{{ t('sec.vuln.notQuarantined') }}</option>
      </select>
      <AppButton v-if="hasFilters" variant="secondary" size="sm" data-testid="clear-filters" @click="clearFilters">
        {{ t('sec.vuln.clearFilters') }}
      </AppButton>
      <button
        type="button"
        :aria-label="t('common.refresh')"
        class="flex items-center justify-center w-9 h-9 rounded-lg border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50"
        :disabled="loading"
        @click="refresh()"
      >
        <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': loading }" aria-hidden="true" />
      </button>
    </div>

    <!-- Scans table -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('sec.vuln.results') }}</h3>
        <span class="text-caption font-mono text-muted">{{ scans.length }}{{ hasMore ? '+' : '' }} {{ t('sec.common.scans') }}</span>
      </div>

      <div v-if="loading" class="py-12 flex flex-col items-center gap-3 text-muted" role="status">
        <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
        <span class="text-sm">{{ t('sec.common.loadingScans') }}</span>
      </div>

      <div v-else-if="loadError" class="py-12 text-center" role="alert">
        <p class="text-sm text-danger">{{ loadError }}</p>
        <AppButton variant="secondary" size="sm" @click="refresh()">
          {{ t('sec.common.retry') }}
        </AppButton>
      </div>

      <div v-else-if="scans.length === 0" data-testid="scans-empty">
        <EmptyState
          v-if="hasFilters"
          :title="t('sec.vuln.noMatchTitle')"
          :description="t('sec.vuln.noMatchDesc')"
        />
        <EmptyState
          v-else
          :title="t('sec.vuln.emptyTitle')"
          :description="t('sec.vuln.emptyDesc')"
        />
        <div v-if="hasFilters" class="flex justify-center pb-10 -mt-8">
          <AppButton variant="secondary" size="sm" data-testid="empty-clear-filters" @click="clearFilters">
            {{ t('sec.vuln.clearFilters') }}
          </AppButton>
        </div>
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="border-b border-border bg-surface-hover/40">
            <tr>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.repository') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.package') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.vulns') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.status') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.scanner') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.scanned') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.vuln.col.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="scan in scans"
              :key="scan.id"
              class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0"
            >
              <td class="px-4 py-3">
                <div class="flex items-center gap-2">
                  <span class="font-mono text-ui text-foreground" data-testid="scan-repo">{{ scan.repository_name }}</span>
                  <FormatBadge :format="scan.repository_format" size="sm" />
                </div>
              </td>
              <td class="px-4 py-3 font-mono text-ui font-medium text-foreground">{{ scan.package_name }}@{{ scan.package_version }}</td>
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
                <div class="flex flex-col items-start gap-1.5">
                  <div class="flex items-start gap-1.5">
                    <ArrowPathIcon v-if="scan.scan_status === 'scanning'" class="w-3 h-3 mt-1 animate-spin text-warning" aria-hidden="true" />
                    <VulnScanStatus :status="scan.scan_status" :scan-error="scan.scan_error" :failed-scanners="scan.failed_scanners" />
                  </div>
                  <StatusBadge v-if="scan.quarantined" :label="t('sec.vuln.quarantined')" variant="danger" data-testid="scan-quarantined" />
                </div>
              </td>
              <td class="px-4 py-3">
                <div class="flex flex-wrap gap-1">
                  <span
                    v-for="name in scannerNames(scan)"
                    :key="name"
                    class="inline-flex items-center px-2 py-0.5 rounded-md text-caption font-semibold border border-border text-muted"
                  >{{ name }}</span>
                </div>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDateTime(scan.scanned_at || scan.created_at) }}</td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-1.5">
                  <AppButton variant="secondary" size="xs" :aria-label="`${t('sec.vuln.details')}: ${scan.package_name}`" data-testid="scan-details" @click="openDetail(scan)">
                    {{ t('sec.vuln.details') }}
                  </AppButton>
                  <AppButton variant="secondary" size="xs" :class="{ 'border-danger/30 text-danger bg-danger-soft/40': scan.quarantined }" :disabled="quarantining === scan.id" :aria-label="`${scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine')}: ${scan.package_name}`" @click="toggleQuarantine(scan)">
                    {{ scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine') }}
                  </AppButton>
                  <AppButton variant="secondary" size="xs" :aria-label="`${t('sec.vuln.delete')}: ${scan.package_name}`" data-testid="scan-delete" @click="deleteTarget = scan">
                    {{ t('sec.vuln.delete') }}
                  </AppButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="hasMore && !loading && !loadError" class="px-4 py-3 border-t border-border flex justify-center">
        <AppButton variant="secondary" size="sm" :loading="loadingMore" :disabled="loadingMore" data-testid="load-more" @click="loadMore">
          {{ loadingMore ? t('sec.vuln.loadingMore') : t('sec.vuln.loadMore') }}
        </AppButton>
      </div>
    </div>

    <!-- Scan details (full findings, loaded from the scan's repository) -->
    <AdminDialog
      :open="detailRow !== null"
      drawer
      max-width="max-w-2xl"
      :title="t('sec.vuln.detailTitle')"
      :description="detailRow ? `${detailRow.repository_name} · ${detailRow.package_name}@${detailRow.package_version}` : ''"
      @close="closeDetail"
    >
      <div v-if="detailLoading" class="py-8 flex justify-center text-muted" role="status">
        <ArrowPathIcon class="w-5 h-5 animate-spin" aria-hidden="true" />
        <span class="sr-only">{{ t('sec.common.loadingScans') }}</span>
      </div>
      <p v-else-if="detailError" class="text-sm text-danger" role="alert">{{ detailError }}</p>
      <div v-else-if="detail" class="space-y-4" data-testid="scan-detail">
        <VulnScanStatus :status="detail.scan_status" :scan-error="detail.scan_error" :failed-scanners="detail.failed_scanners" />
        <h3 class="text-sm font-semibold">{{ t('sec.vuln.findings') }}</h3>
        <p v-if="!findings.length" class="text-sm text-muted">{{ t('sec.vuln.noFindings') }}</p>
        <div v-else class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="border-b border-border">
              <tr>
                <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.id') }}</th>
                <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.severity') }}</th>
                <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.cvss') }}</th>
                <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.fixedIn') }}</th>
                <th scope="col" class="text-left py-2 text-muted font-medium">{{ t('sec.vuln.finding.summary') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="f in findings" :key="f.id" class="border-b border-border last:border-0 align-top">
                <td class="py-2 pr-3 font-mono text-xs">{{ f.id }}</td>
                <td class="py-2 pr-3 text-xs">{{ f.severity }}</td>
                <td class="py-2 pr-3 text-xs font-mono">{{ f.cvss > 0 ? f.cvss : '—' }}</td>
                <td class="py-2 pr-3 text-xs font-mono">{{ f.fixed_in || '—' }}</td>
                <td class="py-2 text-xs text-muted">{{ f.summary }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </AdminDialog>

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('sec.vuln.deleteTitle')"
      :description="deleteTarget ? `${deleteTarget.repository_name} · ${deleteTarget.package_name}@${deleteTarget.package_version}` : ''"
      :confirm-label="t('sec.vuln.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    />

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
        <div>
          <label for="vuln-repo" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.vuln.drawerRepo') }}</label>
          <select
            id="vuln-repo"
            v-model="drawerRepo"
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
          >
            <option value="" disabled>{{ t('sec.common.selectRepo') }}</option>
            <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }}</option>
          </select>
        </div>
        <fieldset>
          <legend class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.vuln.scannersLabel') }}</legend>
          <div class="flex items-center gap-4 flex-wrap">
            <label
              v-for="s in scanners"
              :key="s.value"
              class="inline-flex items-center gap-2 text-sm"
              :class="scannerReady(s.value) ? 'text-foreground' : 'text-muted'"
            >
              <input
                v-model="selectedScanners"
                type="checkbox"
                :value="s.value"
                :disabled="!scannerReady(s.value)"
                :data-testid="`scanner-${s.value}`"
                class="h-4 w-4 rounded border-border-strong accent-primary"
              />
              {{ s.label }}
            </label>
          </div>
          <ul v-if="unavailableScanners.length" class="mt-2 space-y-1.5" data-testid="scanner-unavailable">
            <li v-for="u in unavailableScanners" :key="u.name" class="text-xs text-muted" :data-testid="`scanner-note-${u.name}`">
              <span class="font-medium text-foreground">{{ scannerLabel(u.name) }}</span>:
              <span
                v-if="u.install_state === 'installing' || u.install_state === 'failed'"
                class="font-medium"
                :class="u.install_state === 'failed' ? 'text-danger' : 'text-foreground'"
                :data-testid="`scanner-install-${u.name}`"
              >{{ t(`sec.vuln.installState.${u.install_state}`) }}.</span>
              {{ u.message || t('sec.vuln.scannerNotReady') }}
              <a
                v-if="safeDocsUrl(u.docs_url)"
                :href="safeDocsUrl(u.docs_url)"
                target="_blank"
                rel="noopener noreferrer"
                class="text-primary underline"
              >{{ t('sec.vuln.scannerSetupGuide') }}</a>
            </li>
          </ul>
          <p class="text-xs text-muted mt-1">
            {{ selectedScanners.length ? t('sec.vuln.scannersHint') : t('sec.vuln.scannersRequired') }}
          </p>
        </fieldset>
        <div>
          <label for="vuln-pkg" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.common.package') }}</label>
          <select
            id="vuln-pkg"
            v-model="form.artifactId"
            :disabled="!drawerRepo"
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
            @change="onPackageChange"
          >
            <option value="" disabled>{{ t('sec.common.selectPackage') }}</option>
            <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version }}</option>
          </select>
          <p v-if="drawerRepo && !packages.length" class="text-xs text-muted mt-1">{{ t('sec.common.noPackages') }}</p>
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
import ScanExportButtons from '../../components/ScanExportButtons.vue'
import { formatDateTime } from '../../lib/datetime'
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useRoute, useRouter, type LocationQuery } from 'vue-router'
import { ArrowPathIcon, ShieldCheckIcon } from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import VulnScanStatus from '../../components/VulnScanStatus.vue'
import { useToastStore } from '../../stores/toast'
import { useRepoPackages } from '../../composables/useRepoPackages'
import {
  listAllVulnScans,
  vulnScansExportUrl,
  type ScanExportFormat,
  getVulnScanSummary,
  getVulnScan,
  deleteVulnScan,
  listVulnScanners,
  startVulnScan,
  setVulnQuarantine,
  vulnFindings,
  VULN_ECOSYSTEMS,
  type VulnScan,
  type VulnScanRow,
  type VulnScanSummary,
  type VulnScanner,
  type VulnScannerStatus,
} from '../../api/security'
import { formatLabel } from '../../lib/formats'
import { normalizeScannerSelection, vulnCountsState, vulnScanLabel } from '../../lib/scanStatus'
import {
  EMPTY_VULN_FILTERS,
  VULN_SEVERITY_FILTERS,
  VULN_STATUS_FILTERS,
  buildVulnQuery,
  hasVulnFilters,
  parseVulnFilters,
  toVulnScanFilters,
  type VulnFilterState,
} from '../../lib/vulnScanFilters'
import { t, tDynamic } from '../../i18n/en'

const POLL_MS = 3000
const PAGE_LIMIT = 25

const selectClass =
  'h-9 px-3 rounded-lg bg-surface border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50'

const toasts = useToastStore()
const route = useRoute()
const router = useRouter()
// The composable's selection is the scan drawer's repository: the overview itself is cross-repository.
const { repos, reposLoaded, selectedRepo: drawerRepo, packages, findPackage } = useRepoPackages({ selection: 'manual' })

// ─── Filters: the URL query is the single source of truth ───────────────────

const filters = computed(() => parseVulnFilters(route.query))
/** Export of the scans the active filters select (the whole selection, not the loaded pages). */
function exportUrl(format: ScanExportFormat): string {
  return vulnScansExportUrl(toVulnScanFilters(filters.value), format)
}
const filtersKey = computed(() => JSON.stringify(filters.value))
const hasFilters = computed(() => hasVulnFilters(filters.value))

// The query being navigated to: two changes before the first navigation settles must both apply.
let pendingQuery: LocationQuery | undefined

function setFilter(patch: Partial<VulnFilterState>): void {
  const current = pendingQuery ?? route.query
  const query = buildVulnQuery(current, { ...parseVulnFilters(current), ...patch })
  pendingQuery = query as LocationQuery
  void router.replace({ query }).finally(() => {
    if (pendingQuery === query) pendingQuery = undefined
  })
}

function clearFilters(): void {
  setFilter(EMPTY_VULN_FILTERS)
}

function onSelect(key: keyof VulnFilterState, event: Event): void {
  setFilter({ [key]: (event.target as HTMLSelectElement).value } as Partial<VulnFilterState>)
}

// A repository that does not exist is dropped from the filter once the list is known.
watch(reposLoaded, () => {
  const repo = filters.value.repo
  if (repo && repos.value.length > 0 && !repos.value.some((r) => r.name === repo)) setFilter({ repo: '' })
})

// ─── Summary cards ──────────────────────────────────────────────────────────

type CardFilter = { key: 'severity'; value: VulnFilterState['severity'] } | { key: 'status'; value: VulnFilterState['status'] } | { key: 'quarantined'; value: 'true' }
type SummaryCard = CardFilter & { testId: string; label: string; count: number | undefined; tone: string }

const summary = ref<VulnScanSummary | null>(null)
const summaryError = ref('')
let summarySeq = 0

async function loadSummary(): Promise<void> {
  const seq = ++summarySeq
  try {
    const result = await getVulnScanSummary(filters.value.repo || undefined)
    if (seq !== summarySeq) return
    summary.value = result
    summaryError.value = ''
  } catch (err) {
    if (seq !== summarySeq) return
    summary.value = null
    summaryError.value = err instanceof Error ? err.message : t('sec.vuln.summaryFailed')
  }
}

const summaryCards = computed<SummaryCard[]>(() => {
  const s = summary.value
  const severity = (value: 'critical' | 'high' | 'medium' | 'low', tone: string): SummaryCard => ({
    key: 'severity',
    value,
    testId: `summary-${value}`,
    label: tDynamic(`sec.vuln.atLeast.${value}`),
    count: s?.severity_at_least[value],
    tone,
  })
  const status = (value: 'completed' | 'partial' | 'failed' | 'scanning', tone: string): SummaryCard => ({
    key: 'status',
    value,
    testId: `summary-${value}`,
    label: vulnScanLabel(value),
    count: s?.[value],
    tone,
  })
  return [
    severity('critical', 'text-danger'),
    severity('high', 'text-warning'),
    severity('medium', 'text-severity-medium'),
    severity('low', ''),
    status('completed', 'text-success'),
    status('partial', 'text-warning'),
    status('failed', 'text-danger'),
    status('scanning', ''),
    { key: 'quarantined', value: 'true', testId: 'summary-quarantined', label: t('sec.vuln.quarantined'), count: s?.quarantined, tone: 'text-danger' },
  ]
})

function isCardActive(card: CardFilter): boolean {
  return filters.value[card.key] === card.value
}

function toggleCard(card: CardFilter): void {
  setFilter({ [card.key]: isCardActive(card) ? '' : card.value } as Partial<VulnFilterState>)
}

// ─── Scan list (keyset-paginated, worst first as returned) ──────────────────

const scans = ref<VulnScanRow[]>([])
const hasMore = ref(false)
const nextCursor = ref('')
const pagesLoaded = ref(0)
// True from the start: the filters watcher below loads the first page immediately.
const loading = ref(true)
const loadingMore = ref(false)
const loadError = ref('')
const quarantining = ref<string | null>(null)

let pollTimer: ReturnType<typeof setTimeout> | undefined
// Bumped by every list (re)load; a response carrying an older value is discarded.
let listSeq = 0

function schedulePoll(): void {
  clearTimeout(pollTimer)
  if (scans.value.some((s) => s.scan_status === 'scanning')) {
    pollTimer = setTimeout(() => void refresh(true), POLL_MS)
  }
}

// `silent` refreshes (polling) keep the table visible instead of flashing the spinner.
async function loadScans(silent = false): Promise<void> {
  clearTimeout(pollTimer)
  const seq = ++listSeq
  loadingMore.value = false
  if (!silent) {
    loading.value = true
    loadError.value = ''
    scans.value = []
    hasMore.value = false
    nextCursor.value = ''
    pagesLoaded.value = 0
  }
  try {
    const page = await listAllVulnScans({ ...toVulnScanFilters(filters.value), limit: PAGE_LIMIT })
    if (seq !== listSeq) return
    if (silent && pagesLoaded.value > 1) {
      // Pages beyond the first stay loaded: refresh the rows in place.
      const fresh = new Map(page.scans.map((s) => [s.id, s]))
      scans.value = scans.value.map((s) => fresh.get(s.id) ?? s)
    } else {
      scans.value = page.scans
      hasMore.value = page.has_more
      nextCursor.value = page.next_cursor ?? ''
      pagesLoaded.value = 1
    }
    loadError.value = ''
  } catch (err) {
    if (seq !== listSeq) return
    if (!silent) loadError.value = err instanceof Error ? err.message : t('sec.vuln.loadFailed')
  } finally {
    if (seq === listSeq) {
      loading.value = false
      schedulePoll()
    }
  }
}

async function loadMore(): Promise<void> {
  if (!hasMore.value || !nextCursor.value || loadingMore.value || loading.value) return
  const seq = listSeq
  loadingMore.value = true
  try {
    const page = await listAllVulnScans({ ...toVulnScanFilters(filters.value), limit: PAGE_LIMIT, cursor: nextCursor.value })
    if (seq !== listSeq) return
    const seen = new Set(scans.value.map((s) => s.id))
    scans.value = [...scans.value, ...page.scans.filter((s) => !seen.has(s.id))]
    hasMore.value = page.has_more
    nextCursor.value = page.next_cursor ?? ''
    pagesLoaded.value++
  } catch (err) {
    if (seq !== listSeq) return
    toasts.error(err instanceof Error ? err.message : t('sec.vuln.loadMoreFailed'))
  } finally {
    if (seq === listSeq) {
      loadingMore.value = false
      schedulePoll()
    }
  }
}

function refresh(silent = false): Promise<unknown> {
  return Promise.all([loadScans(silent), loadSummary()])
}

function scannerNames(scan: VulnScanRow): string[] {
  const names = scan.scanner.split(',').filter(Boolean)
  return names.length ? names : ['osv']
}

// Any filter change starts over with the first page; the summary only follows the repository.
watch(filtersKey, () => void loadScans(), { immediate: true })
watch(
  () => filters.value.repo,
  () => {
    summary.value = null
    void loadSummary()
  },
  { immediate: true },
)

// ─── Row actions ────────────────────────────────────────────────────────────

async function toggleQuarantine(scan: VulnScanRow): Promise<void> {
  if (quarantining.value) return
  quarantining.value = scan.id
  try {
    const updated = await setVulnQuarantine(scan.repository_name, scan.id, !scan.quarantined)
    const wanted = filters.value.quarantined
    if (wanted && String(updated.quarantined) !== wanted) {
      // The row no longer matches the quarantine filter.
      scans.value = scans.value.filter((s) => s.id !== scan.id)
    } else {
      scans.value = scans.value.map((s) => (s.id === scan.id ? { ...s, quarantined: updated.quarantined } : s))
    }
    toasts.success(updated.quarantined ? t('sec.vuln.quarantined') : t('sec.vuln.quarantineRemoved'))
    void loadSummary()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vuln.quarantineFailed'))
  } finally {
    quarantining.value = null
  }
}

const deleteTarget = ref<VulnScanRow | null>(null)
const deleting = ref(false)

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleting.value) return
  deleting.value = true
  try {
    await deleteVulnScan(target.repository_name, target.id)
    scans.value = scans.value.filter((s) => s.id !== target.id)
    if (detailRow.value?.id === target.id) closeDetail()
    deleteTarget.value = null
    toasts.success(t('sec.vuln.deleted'))
    void loadSummary()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vuln.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

// ─── Scan details: the full findings come from the scan's own repository ────

const detailRow = ref<VulnScanRow | null>(null)
const detail = ref<VulnScan | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
let detailSeq = 0

const findings = computed(() => (detail.value ? vulnFindings(detail.value) : []))

async function openDetail(row: VulnScanRow): Promise<void> {
  const seq = ++detailSeq
  detailRow.value = row
  detail.value = null
  detailError.value = ''
  detailLoading.value = true
  try {
    const full = await getVulnScan(row.repository_name, row.id)
    if (seq !== detailSeq) return
    detail.value = full
  } catch (err) {
    if (seq !== detailSeq) return
    detailError.value = err instanceof Error ? err.message : t('sec.vuln.detailLoadFailed')
  } finally {
    if (seq === detailSeq) detailLoading.value = false
  }
}

function closeDetail(): void {
  detailSeq++
  detailRow.value = null
  detail.value = null
  detailLoading.value = false
}

// ─── Scan drawer ────────────────────────────────────────────────────────────

const showDrawer = ref(false)
const scanning = ref(false)
const selectedScanners = ref<VulnScanner[]>(['osv'])
const form = ref({ artifactId: '', ecosystem: '' })

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

// Server-side readiness per scanner, loaded for the drawer's repository. A scanner
// without an entry (no repository yet, list not loaded or failed) stays selectable:
// the server still reports a failed scan with the reason.
const scannerStatuses = ref<VulnScannerStatus[]>([])

function scannerReady(name: VulnScanner): boolean {
  const status = scannerStatuses.value.find((x) => x.name === name)
  return !status || status.ready
}

const unavailableScanners = computed(() => scannerStatuses.value.filter((x) => !x.ready))

function scannerLabel(name: string): string {
  return scanners.find((x) => x.value === name)?.label ?? name
}

// The link comes from the server; only follow https URLs.
function safeDocsUrl(url: string | null | undefined): string | undefined {
  return url && url.startsWith('https://') ? url : undefined
}

async function loadScannerStatuses(): Promise<void> {
  const repo = drawerRepo.value
  if (!repo) return
  try {
    const statuses = await listVulnScanners(repo)
    if (!showDrawer.value || repo !== drawerRepo.value) return
    scannerStatuses.value = statuses
    selectedScanners.value = selectedScanners.value.filter((name) => scannerReady(name))
  } catch {
    if (repo === drawerRepo.value) scannerStatuses.value = []
  }
}

function openDrawer(): void {
  form.value = { artifactId: '', ecosystem: '' }
  selectedScanners.value = ['osv']
  scannerStatuses.value = []
  // Prefilled from the repository filter; otherwise the last choice is kept.
  const prefill = filters.value.repo
  const repoChanges = !!prefill && prefill !== drawerRepo.value
  if (prefill) drawerRepo.value = prefill
  showDrawer.value = true
  // A changed repository is loaded by the watcher below.
  if (!repoChanges) void loadScannerStatuses()
}

watch(drawerRepo, () => {
  form.value = { artifactId: '', ecosystem: '' }
  scannerStatuses.value = []
  if (showDrawer.value) void loadScannerStatuses()
})

function onPackageChange(): void {
  const fmt = findPackage(form.value.artifactId)?.format?.toLowerCase() ?? ''
  form.value.ecosystem = (VULN_ECOSYSTEMS as readonly string[]).includes(fmt) ? fmt : ''
}

async function startScan(): Promise<void> {
  const pkg = findPackage(form.value.artifactId)
  if (!pkg || !drawerRepo.value || scanning.value) return
  scanning.value = true
  try {
    await startVulnScan(drawerRepo.value, {
      artifact_id: pkg.id,
      package_name: pkg.name,
      package_version: pkg.version,
      ecosystem: form.value.ecosystem,
      scanners: normalizeScannerSelection(selectedScanners.value),
    })
    toasts.success(t('sec.vuln.started'))
    showDrawer.value = false
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vuln.startFailed'))
  } finally {
    scanning.value = false
  }
}

onBeforeUnmount(() => {
  clearTimeout(pollTimer)
  listSeq++
  summarySeq++
  detailSeq++
})
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header -->
    <PageHeader :title="t('sec.license.title')" :description="t('sec.license.description')">
      <template #actions>
        <AppButton variant="primary" size="md" :disabled="!selectedRepo" @click="openDrawer">
          <ArrowPathIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('sec.license.trigger') }}
        </AppButton>
      </template>
    </PageHeader>

    <!-- Repo selector -->
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
        @click="loadScans"
      >
        <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': loadingScans }" aria-hidden="true" />
      </button>
    </div>

    <!-- Stat tiles (derived from the loaded scans) -->
    <div class="grid grid-cols-3 gap-4">
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.license.totalScans') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums">{{ scans.length }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.license.distinct') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-success">{{ distinctLicenses }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.license.noLicense') }}</div>
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
          class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border-strong text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
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
        <span class="text-caption font-mono text-muted">{{ filteredScans.length }} {{ t('sec.common.scans') }}</span>
      </div>

      <div v-if="loadingScans" class="py-12 flex flex-col items-center gap-3 text-muted" role="status">
        <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
        <span class="text-sm">{{ t('sec.common.loadingScans') }}</span>
      </div>

      <div v-else-if="loadError" class="py-12 text-center" role="alert">
        <p class="text-sm text-danger">{{ loadError }}</p>
        <AppButton variant="secondary" size="sm" @click="loadScans">
          {{ t('sec.common.retry') }}
        </AppButton>
      </div>

      <EmptyState
        v-else-if="!selectedRepo"
        :title="reposLoaded && !repos.length ? t('sec.common.noReposTitle') : t('sec.common.selectRepoTitle')"
        :description="reposLoaded && !repos.length ? t('sec.common.noReposDesc') : t('sec.license.selectRepoDesc')"
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
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.license.col.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="scan in filteredScans"
              :key="scan.id"
              class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0"
            >
              <td class="px-4 py-3 font-mono text-ui text-foreground">
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
                    :class="[
                      'inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-caption font-mono border',
                      isUnresolved(scan, lic) ? 'border-warning/40 bg-warning-soft text-warning' : 'border-border text-foreground/80',
                    ]"
                    :title="isUnresolved(scan, lic) ? t('sec.license.unresolvedHint') : undefined"
                    :data-testid="isUnresolved(scan, lic) ? 'license-unresolved' : undefined"
                  >{{ lic }}<span v-if="isUnresolved(scan, lic)" class="font-sans">· {{ t('sec.license.unresolved') }}</span></span>
                </div>
                <span v-else class="text-xs text-muted">{{ t('sec.license.none') }}</span>
                <p
                  v-if="scan.licenses?.length && scan.license_source === 'client_declared'"
                  class="mt-1 text-caption text-muted"
                  :title="t('sec.license.clientDeclaredHint')"
                  data-testid="license-client-declared"
                >{{ t('sec.license.clientDeclared') }}</p>
                <div v-if="violationsFor(scan.id).length" class="mt-2">
                  <LicensePolicyViolations :violations="violationsFor(scan.id)" />
                </div>
              </td>
              <td class="px-4 py-3">
                <div class="flex flex-col items-start gap-1">
                  <span :class="scanStatusClass(scan.scan_status)" :title="scan.scan_error ?? undefined">{{ statusLabel(scan.scan_status) }}</span>
                  <StatusBadge
                    v-if="licenseQuarantineState(scan) === 'quarantined'"
                    :label="t('sec.license.quarantined')"
                    variant="danger"
                    :title="t('sec.license.quarantinedHint')"
                    data-testid="license-quarantined"
                  />
                  <StatusBadge
                    v-else-if="licenseQuarantineState(scan) === 'approved'"
                    :label="t('sec.license.approved')"
                    variant="success"
                    :title="t('sec.license.approvedHint')"
                    data-testid="license-approved"
                  />
                </div>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDateTime(scan.scanned_at || scan.created_at) }}</td>
              <td class="px-4 py-3">
                <AppButton variant="secondary" size="xs" :class="{ 'border-danger/30 text-danger bg-danger-soft/40': scan.quarantined }" :disabled="quarantining === scan.id" :aria-label="`${scan.quarantined ? t('sec.license.approve') : t('sec.license.quarantine')}: ${packageLabel(scan.artifact_id)}`" @click="toggleQuarantine(scan)">
                  {{ scan.quarantined ? t('sec.license.approve') : t('sec.license.quarantine') }}
                </AppButton>
              </td>
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
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
        >
          <option value="" disabled>{{ t('sec.common.selectPackage') }}</option>
          <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version }}</option>
        </select>
        <p v-if="!packages.length" class="text-xs text-muted mt-1">{{ t('sec.common.noPackages') }}</p>
      </div>
      <template #footer>
        <AppButton variant="secondary" size="md" @click="showDrawer = false">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="md" :disabled="triggering || !artifactId" @click="triggerScan">
          <ArrowPathIcon class="w-3.5 h-3.5" :class="{ 'animate-spin': triggering }" aria-hidden="true" />
          {{ t('sec.license.start') }}
        </AppButton>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../components/AppButton.vue'
import PageHeader from '../../components/PageHeader.vue'
import { formatDateTime } from '../../lib/datetime'
import { ref, computed, watch } from 'vue'
import { ArrowPathIcon, MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import LicensePolicyViolations from '../../components/LicensePolicyViolations.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import { useToastStore } from '../../stores/toast'
import { useRepoPackages } from '../../composables/useRepoPackages'
import {
  listLicenseScans,
  triggerLicenseScan,
  setLicenseQuarantine,
  type LicensePolicyViolation,
  type LicenseScan,
} from '../../api/security'
import { licenseQuarantineState } from '../../lib/scanStatus'
import { t, tDynamic } from '../../i18n/en'

const toasts = useToastStore()
const { repos, reposLoaded, selectedRepo, packages, packageLabel, findPackage } = useRepoPackages()

const scans = ref<LicenseScan[]>([])
const loadingScans = ref(false)
const loadError = ref('')
const triggering = ref(false)
const showDrawer = ref(false)
const artifactId = ref('')
const searchQuery = ref('')
const statusFilter = ref('all')
const quarantining = ref<string | null>(null)

// A license read from the artifact that names no single SPDX license ("BSD", "BSD License"): kept raw, flagged for review.
const isUnresolved = (scan: LicenseScan, lic: string) => scan.unresolved_licenses?.includes(lic) ?? false
// Policy violations are only reported by the scan run itself (the list endpoint
// does not carry them), so they are kept per scan id for this session.
const violations = ref<Record<string, LicensePolicyViolation[]>>({})
let loadSeq = 0

function violationsFor(scanId: string): LicensePolicyViolation[] {
  return violations.value[scanId] ?? []
}

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
  const base = 'inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-caption font-semibold '
  if (status === 'failed') return base + 'text-danger bg-danger-soft/60'
  if (status === 'completed') return base + 'text-success bg-success-soft/60'
  return base + 'text-warning bg-warning-soft/60'
}

function statusLabel(status: string): string {
  return ['pending', 'completed', 'failed'].includes(status) ? tDynamic(`sec.license.status.${status}`) : status
}


async function loadScans(): Promise<void> {
  const repo = selectedRepo.value
  // Counted before the early return: a response still in flight for the
  // previous repository must not land after the selection was cleared.
  const seq = ++loadSeq
  if (!repo) {
    scans.value = []
    loadError.value = ''
    loadingScans.value = false
    return
  }
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
    const run = await triggerLicenseScan(selectedRepo.value, artifactId.value)
    violations.value = { ...violations.value, [run.id]: run.policy_violations ?? [] }
    if (run.quarantined) toasts.warning(t('sec.license.startedQuarantined'))
    else toasts.success(t('sec.license.started'))
    showDrawer.value = false
    await loadScans()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.license.startFailed'))
  } finally {
    triggering.value = false
  }
}

async function toggleQuarantine(scan: LicenseScan): Promise<void> {
  if (!selectedRepo.value || quarantining.value) return
  quarantining.value = scan.id
  try {
    // Approving releases the artifact (quarantined: false); quarantining blocks downloads again.
    const updated = await setLicenseQuarantine(selectedRepo.value, scan.id, !scan.quarantined)
    scans.value = scans.value.map((s) => (s.id === scan.id ? { ...s, ...updated } : s))
    toasts.success(updated.quarantined ? t('sec.license.quarantineDone') : t('sec.license.approveDone'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.license.quarantineFailed'))
  } finally {
    quarantining.value = null
  }
}

watch(selectedRepo, () => {
  // Another repository's scans must not stay on screen while this one loads.
  scans.value = []
  void loadScans()
})
</script>

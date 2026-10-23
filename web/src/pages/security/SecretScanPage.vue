<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header -->
    <PageHeader :title="t('sec.secret.title')" :description="t('sec.secret.description')">
      <template #actions>
        <div class="flex items-center gap-3 flex-wrap">
          <ScanExportButtons :url="exportUrl" :disabled="!selectedRepo" />
          <AppButton variant="primary" size="md" :disabled="!selectedRepo" @click="openDrawer">
            <ArrowPathIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('sec.secret.trigger') }}
          </AppButton>
        </div>
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

    <!-- Stats tiles -->
    <div class="grid grid-cols-2 sm:grid-cols-4 gap-4">
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.secret.total') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums">{{ scans.length }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.secret.flagged') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-danger">{{ flaggedCount }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.secret.clean') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-success">{{ cleanCount }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.secret.quarantined') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-warning">{{ quarantinedCount }}</div>
      </div>
    </div>

    <!-- Status tabs + search -->
    <div class="flex items-center gap-3 flex-wrap">
      <div role="group" :aria-label="t('sec.common.filterStatus')" class="flex items-center gap-1 bg-surface-hover/60 rounded-lg p-0.5">
        <button
          v-for="tab in tabs"
          :key="tab.id"
          type="button"
          :aria-pressed="activeTab === tab.id"
          class="px-3 py-1.5 rounded-md text-xs font-medium transition-colors"
          :class="activeTab === tab.id ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
          @click="activeTab = tab.id"
        >
          {{ tab.label }}
          <span class="ml-1 font-mono text-micro">({{ tab.count }})</span>
        </button>
      </div>
      <div class="relative flex-1 max-w-sm">
        <MagnifyingGlassIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" aria-hidden="true" />
        <input
          v-model="searchQuery"
          type="search"
          :aria-label="t('sec.secret.search')"
          :placeholder="t('sec.secret.search')"
          class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border-strong text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
      </div>
    </div>

    <!-- Scans table -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('sec.secret.results') }}</h3>
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
        :description="reposLoaded && !repos.length ? t('sec.common.noReposDesc') : t('sec.secret.selectRepoDesc')"
      />

      <EmptyState
        v-else-if="filteredScans.length === 0"
        :title="t('sec.secret.emptyTitle')"
        :description="t('sec.secret.emptyDesc')"
      />

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="border-b border-border bg-surface-hover/40">
            <tr>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.artifact') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.status') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.secret.findings') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.scanned') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="scan in filteredScans"
              :key="scan.id"
              class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0 align-top"
            >
              <td class="px-4 py-3 font-mono text-ui text-foreground">
                <span class="inline-flex items-center gap-2">
                  <FormatBadge v-if="formatOf(scan.artifact_id)" :format="formatOf(scan.artifact_id)" size="sm" />
                  {{ packageLabel(scan.artifact_id) }}
                </span>
              </td>
              <td class="px-4 py-3">
                <span :class="scanStatusClass(scan.scan_status)">{{ statusLabel(scan.scan_status) }}</span>
              </td>
              <td class="px-4 py-3 text-ui">
                <span class="font-mono text-foreground">{{ scan.finding_count }}</span>
                <ul v-if="scan.findings?.length" class="mt-1 space-y-0.5">
                  <li v-for="(f, i) in scan.findings" :key="i" class="text-caption text-muted">
                    <span class="font-semibold text-foreground/80">{{ f.type }}</span>
                    <span v-if="f.severity"> · {{ f.severity }}</span>
                    <span v-if="f.line_hint"> · {{ f.line_hint }}</span>
                    <span v-if="f.redacted_value" class="font-mono"> · {{ f.redacted_value }}</span>
                  </li>
                </ul>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDateTime(scan.scanned_at || scan.created_at) }}</td>
              <td class="px-4 py-3">
                <AppButton variant="secondary" size="xs" :class="{ 'border-danger/30 text-danger bg-danger-soft/40': scan.quarantined }" :disabled="quarantining === scan.id" :aria-label="`${scan.quarantined ? t('sec.vuln.unquarantine') : t('sec.vuln.quarantine')}: ${packageLabel(scan.artifact_id)}`" @click="toggleQuarantine(scan)">
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
      :title="t('sec.secret.drawerTitle')"
      :description="t('sec.secret.drawerHint')"
      @close="showDrawer = false"
    >
      <div class="space-y-4">
        <div>
          <label for="secret-pkg" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.common.package') }}</label>
          <select
            id="secret-pkg"
            v-model="form.artifactId"
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
          >
            <option value="" disabled>{{ t('sec.common.selectPackage') }}</option>
            <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version }}</option>
          </select>
          <p v-if="!packages.length" class="text-xs text-muted mt-1">{{ t('sec.common.noPackages') }}</p>
        </div>
        <div>
          <label for="secret-content" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.secret.content') }}</label>
          <textarea
            id="secret-content"
            v-model="form.content"
            rows="8"
            spellcheck="false"
            :placeholder="t('sec.secret.contentPlaceholder')"
            class="w-full px-3 py-2 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50 resize-none font-mono"
          />
          <p class="text-xs text-muted mt-1">{{ t('sec.secret.contentHint') }}</p>
        </div>
      </div>
      <template #footer>
        <AppButton variant="secondary" size="md" @click="showDrawer = false">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="md" :disabled="triggering || !form.artifactId || !form.content.trim()" @click="triggerScan">
          <ArrowPathIcon class="w-3.5 h-3.5" :class="{ 'animate-spin': triggering }" aria-hidden="true" />
          {{ t('sec.secret.start') }}
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
import { ref, computed, watch } from 'vue'
import { ArrowPathIcon, MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import { useToastStore } from '../../stores/toast'
import { useRepoPackages } from '../../composables/useRepoPackages'
import { listSecretScans, triggerSecretScan, setSecretQuarantine, secretScansExportUrl, type ScanExportFormat, type SecretScan } from '../../api/security'
import { t, tDynamic } from '../../i18n/en'

const toasts = useToastStore()
const { repos, reposLoaded, selectedRepo, packages, packageLabel, findPackage } = useRepoPackages()

const scans = ref<SecretScan[]>([])
const loadingScans = ref(false)
const loadError = ref('')
const triggering = ref(false)
const quarantining = ref<string | null>(null)
const showDrawer = ref(false)
const form = ref({ artifactId: '', content: '' })
const searchQuery = ref('')
const activeTab = ref<'all' | 'flagged' | 'clean'>('all')
let loadSeq = 0

function exportUrl(format: ScanExportFormat): string {
  return secretScansExportUrl(selectedRepo.value, format)
}

const flaggedCount = computed(() => scans.value.filter((s) => s.scan_status === 'flagged').length)
const cleanCount = computed(() => scans.value.filter((s) => s.scan_status === 'clean').length)
const quarantinedCount = computed(() => scans.value.filter((s) => s.quarantined).length)

const tabs = computed(() => [
  { id: 'all' as const, label: t('sec.common.all'), count: scans.value.length },
  { id: 'flagged' as const, label: t('sec.secret.flagged'), count: flaggedCount.value },
  { id: 'clean' as const, label: t('sec.secret.clean'), count: cleanCount.value },
])

const filteredScans = computed(() => {
  let result = scans.value
  const q = searchQuery.value.trim().toLowerCase()
  if (q) result = result.filter((s) => packageLabel(s.artifact_id).toLowerCase().includes(q))
  if (activeTab.value !== 'all') result = result.filter((s) => s.scan_status === activeTab.value)
  return result
})

function formatOf(id: string): string {
  return findPackage(id)?.format ?? ''
}

function scanStatusClass(status: string): string {
  const base = 'inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-caption font-semibold '
  if (status === 'flagged') return base + 'text-danger bg-danger-soft/60'
  if (status === 'clean') return base + 'text-success bg-success-soft/60'
  return base + 'text-warning bg-warning-soft/60'
}

function statusLabel(status: string): string {
  return status === 'flagged' || status === 'clean' ? tDynamic(`sec.secret.${status}`) : status
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
    const result = await listSecretScans(repo)
    if (seq !== loadSeq) return
    scans.value = result
    loadError.value = ''
  } catch (err) {
    if (seq !== loadSeq) return
    scans.value = []
    loadError.value = err instanceof Error ? err.message : t('sec.secret.loadFailed')
  } finally {
    if (seq === loadSeq) loadingScans.value = false
  }
}

function openDrawer(): void {
  form.value = { artifactId: '', content: '' }
  showDrawer.value = true
}

async function triggerScan(): Promise<void> {
  if (!selectedRepo.value || !form.value.artifactId || triggering.value) return
  triggering.value = true
  try {
    const result = await triggerSecretScan(selectedRepo.value, form.value.artifactId, form.value.content)
    if (result.scan_status === 'flagged') toasts.warning(t('sec.secret.flaggedToast'))
    else toasts.success(t('sec.secret.cleanToast'))
    showDrawer.value = false
    await loadScans()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.secret.startFailed'))
  } finally {
    triggering.value = false
  }
}

async function toggleQuarantine(scan: SecretScan): Promise<void> {
  if (quarantining.value) return
  quarantining.value = scan.id
  try {
    const updated = await setSecretQuarantine(selectedRepo.value, scan.id, !scan.quarantined)
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
</script>

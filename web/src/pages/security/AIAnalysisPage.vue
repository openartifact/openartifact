<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header -->
    <PageHeader :title="t('sec.ai.title')" :description="t('sec.ai.description')">
      <template #actions>
        <AppButton variant="primary" size="md" :disabled="!selectedRepo" @click="openTriggerDrawer">
          <SparklesIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('sec.ai.trigger') }}
        </AppButton>
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
        :disabled="loadingAnalyses || !selectedRepo"
        @click="loadAnalyses()"
      >
        <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': loadingAnalyses }" aria-hidden="true" />
      </button>
    </div>

    <!-- Stats tiles -->
    <div class="grid grid-cols-2 sm:grid-cols-4 gap-4">
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.ai.total') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums">{{ analyses.length }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.ai.license.SAFE') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-success">{{ countSafe }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.ai.license.REVIEW_REQUIRED') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-warning">{{ countReview }}</div>
      </div>
      <div class="rounded-xl border border-border bg-surface p-4">
        <div class="text-micro font-semibold text-muted">{{ t('sec.ai.quarantineRecommended') }}</div>
        <div class="text-2xl font-bold mt-1 tabular-nums text-danger">{{ countQuarantine }}</div>
      </div>
    </div>

    <!-- Filter toolbar -->
    <div class="flex items-center gap-3 flex-wrap">
      <div class="relative flex-1 max-w-sm">
        <MagnifyingGlassIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" aria-hidden="true" />
        <input
          v-model="searchQuery"
          type="search"
          :aria-label="t('sec.ai.search')"
          :placeholder="t('sec.ai.search')"
          class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border-strong text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
      </div>
      <div role="group" :aria-label="t('sec.common.filterStatus')" class="flex items-center gap-1 bg-surface-hover/60 rounded-lg p-0.5">
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
      <div role="group" :aria-label="t('sec.ai.filterLicense')" class="flex items-center gap-1 bg-surface-hover/60 rounded-lg p-0.5">
        <button
          v-for="l in licenseOptions"
          :key="l.value"
          type="button"
          :aria-pressed="licenseFilter === l.value"
          class="px-3 py-1.5 rounded-md text-xs font-medium transition-colors"
          :class="licenseFilter === l.value ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
          @click="licenseFilter = l.value"
        >{{ l.label }}</button>
      </div>
    </div>

    <!-- Results table -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('sec.ai.results') }}</h3>
        <span class="text-caption font-mono text-muted">{{ filteredAnalyses.length }} {{ t('sec.ai.analysesCount') }}</span>
      </div>

      <div v-if="loadingAnalyses" class="py-12 flex flex-col items-center gap-3 text-muted" role="status">
        <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
        <span class="text-sm">{{ t('sec.ai.loading') }}</span>
      </div>

      <div v-else-if="loadError" class="py-12 text-center" role="alert">
        <p class="text-sm text-danger">{{ loadError }}</p>
        <AppButton variant="secondary" size="sm" @click="loadAnalyses()">
          {{ t('sec.common.retry') }}
        </AppButton>
      </div>

      <EmptyState
        v-else-if="!selectedRepo"
        :title="reposLoaded && !repos.length ? t('sec.common.noReposTitle') : t('sec.common.selectRepoTitle')"
        :description="reposLoaded && !repos.length ? t('sec.common.noReposDesc') : t('sec.ai.selectRepoDesc')"
      />

      <EmptyState
        v-else-if="filteredAnalyses.length === 0"
        :title="t('sec.ai.emptyTitle')"
        :description="t('sec.ai.emptyDesc')"
      />

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="border-b border-border bg-surface-hover/40">
            <tr>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.artifact') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.ai.col.format') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.ai.col.license') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.status') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.ai.col.quarantine') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.ai.col.summary') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 text-muted font-medium">{{ t('sec.common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="analysis in filteredAnalyses"
              :key="analysis.id"
              class="hover:bg-surface-hover/40 transition-colors border-b border-border last:border-0"
            >
              <td class="px-4 py-3">
                <button type="button" class="text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-primary rounded" @click="selectedAnalysis = analysis">
                  <div class="font-mono text-ui font-medium text-foreground">{{ analysis.artifact_name || analysis.artifact_id }}</div>
                  <div class="font-mono text-caption text-muted mt-0.5">{{ analysis.artifact_version }}</div>
                </button>
              </td>
              <td class="px-4 py-3">
                <FormatBadge v-if="analysis.artifact_format" :format="analysis.artifact_format" />
              </td>
              <td class="px-4 py-3">
                <div class="flex flex-col gap-1">
                  <span :class="licenseStatusClass(analysis.license_status)">{{ licenseStatusLabel(analysis.license_status) }}</span>
                  <span v-if="analysis.license_name" class="text-caption text-muted font-mono truncate max-w-[140px]">{{ analysis.license_name }}</span>
                </div>
              </td>
              <td class="px-4 py-3">
                <span :class="analysisStatusClass(analysis.status)">
                  <ArrowPathIcon v-if="isActive(analysis.status)" class="w-3 h-3 animate-spin" aria-hidden="true" />
                  {{ analysisStatusLabel(analysis.status) }}
                </span>
              </td>
              <td class="px-4 py-3">
                <span
                  v-if="analysis.quarantine_recommended"
                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-caption font-semibold text-danger bg-danger-soft"
                >
                  <ExclamationTriangleIcon class="w-3 h-3" aria-hidden="true" />
                  {{ t('sec.ai.quarantine') }}
                </span>
                <span v-else class="text-muted text-xs">—</span>
              </td>
              <td class="px-4 py-3 text-xs text-muted max-w-[200px]">
                <span class="line-clamp-2">{{ truncate(analysis.summary, 60) }}</span>
              </td>
              <td class="px-4 py-3">
                <AppButton variant="secondary" size="xs" :loading="retriggering === analysis.id" :disabled="retriggering === analysis.id" :aria-label="`${t('sec.ai.retrigger')}: ${analysis.artifact_name || analysis.artifact_id}`" @click="retrigger(analysis)">
                  {{ t('sec.ai.retrigger') }}
                </AppButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Detail drawer -->
    <AdminDialog
      :open="!!selectedAnalysis"
      drawer
      max-width="max-w-lg"
      :title="selectedAnalysis?.artifact_name || selectedAnalysis?.artifact_id || ''"
      :description="selectedAnalysis?.artifact_version"
      @close="selectedAnalysis = null"
    >
      <div v-if="selectedAnalysis" class="space-y-5">
        <div class="flex items-center gap-3 flex-wrap">
          <FormatBadge v-if="selectedAnalysis.artifact_format" :format="selectedAnalysis.artifact_format" />
          <span :class="analysisStatusClass(selectedAnalysis.status)">
            <ArrowPathIcon v-if="isActive(selectedAnalysis.status)" class="w-3 h-3 animate-spin" aria-hidden="true" />
            {{ analysisStatusLabel(selectedAnalysis.status) }}
          </span>
          <span
            v-if="selectedAnalysis.quarantine_recommended"
            class="inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-caption font-semibold text-danger bg-danger-soft"
          >
            <ExclamationTriangleIcon class="w-3 h-3" aria-hidden="true" />
            {{ t('sec.ai.quarantineRecommended') }}
          </span>
          <span v-else class="inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-caption font-semibold text-success bg-success-soft/60">
            {{ t('sec.ai.noQuarantine') }}
          </span>
        </div>

        <div v-if="selectedAnalysis.error_message" class="rounded-lg bg-danger-soft border border-danger/20 px-4 py-3" role="alert">
          <p class="text-xs font-semibold text-danger mb-1">{{ t('sec.ai.error') }}</p>
          <p class="text-xs text-danger/80 font-mono">{{ selectedAnalysis.error_message }}</p>
        </div>

        <div v-if="selectedAnalysis.summary">
          <h4 class="text-xs font-semibold text-muted mb-2">{{ t('sec.ai.col.summary') }}</h4>
          <p class="text-sm text-foreground leading-relaxed">{{ selectedAnalysis.summary }}</p>
        </div>

        <div v-if="selectedAnalysis.tags?.length">
          <h4 class="text-xs font-semibold text-muted mb-2">{{ t('sec.ai.tags') }}</h4>
          <div class="flex flex-wrap gap-1.5">
            <span
              v-for="tag in selectedAnalysis.tags"
              :key="tag"
              class="inline-flex items-center px-2 py-0.5 rounded-md text-caption font-medium bg-primary/10 text-primary border border-primary/20"
            >{{ tag }}</span>
          </div>
        </div>

        <div>
          <h4 class="text-xs font-semibold text-muted mb-2">{{ t('sec.ai.col.license') }}</h4>
          <div class="flex items-center gap-2 flex-wrap">
            <span :class="licenseStatusClass(selectedAnalysis.license_status)">{{ licenseStatusLabel(selectedAnalysis.license_status) }}</span>
            <span v-if="selectedAnalysis.license_name" class="text-sm text-foreground font-mono">{{ selectedAnalysis.license_name }}</span>
            <span v-else class="text-sm text-muted">{{ t('sec.ai.unknownLicense') }}</span>
          </div>
        </div>

        <div v-if="selectedAnalysis.security_notes">
          <h4 class="text-xs font-semibold text-muted mb-2">{{ t('sec.ai.securityNotes') }}</h4>
          <p class="text-sm text-foreground leading-relaxed whitespace-pre-wrap">{{ selectedAnalysis.security_notes }}</p>
        </div>

        <div class="rounded-lg bg-surface-hover/60 border border-border px-4 py-3 space-y-2">
          <h4 class="text-xs font-semibold text-muted">{{ t('sec.ai.modelInfo') }}</h4>
          <dl class="grid grid-cols-2 gap-2 text-xs">
            <div>
              <dt class="text-muted">{{ t('sec.ai.model') }}</dt>
              <dd class="font-mono text-foreground mt-0.5">{{ selectedAnalysis.model_used || '—' }}</dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('sec.ai.promptTokens') }}</dt>
              <dd class="font-mono text-foreground mt-0.5 tabular-nums">{{ selectedAnalysis.prompt_tokens ?? '—' }}</dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('sec.ai.completionTokens') }}</dt>
              <dd class="font-mono text-foreground mt-0.5 tabular-nums">{{ selectedAnalysis.completion_tokens ?? '—' }}</dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('sec.ai.updated') }}</dt>
              <dd class="font-mono text-foreground mt-0.5">{{ formatDateTime(selectedAnalysis.updated_at) }}</dd>
            </div>
          </dl>
        </div>
      </div>
      <template #footer>
        <AppButton variant="secondary" size="md" @click="selectedAnalysis = null">
          {{ t('common.close') }}
        </AppButton>
        <AppButton variant="primary" size="md" :loading="retriggering === selectedAnalysis.id" v-if="selectedAnalysis" :disabled="retriggering === selectedAnalysis.id" @click="retrigger(selectedAnalysis)">
          <SparklesIcon v-if="!(retriggering === selectedAnalysis.id)" class="w-3.5 h-3.5" aria-hidden="true" />
          {{ retriggering === selectedAnalysis.id ? t('sec.ai.triggering') : t('sec.ai.retriggerLong') }}
        </AppButton>
      </template>
    </AdminDialog>

    <!-- Trigger drawer -->
    <AdminDialog
      :open="showTriggerDrawer"
      drawer
      max-width="max-w-md"
      :title="t('sec.ai.triggerTitle')"
      :description="t('sec.ai.triggerHint')"
      @close="showTriggerDrawer = false"
    >
      <div class="space-y-4">
        <div>
          <label for="ai-pkg" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.common.package') }}</label>
          <select
            id="ai-pkg"
            v-model="triggerForm.artifactId"
            class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
          >
            <option value="" disabled>{{ t('sec.common.selectPackage') }}</option>
            <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version || '?' }}</option>
          </select>
          <p v-if="!packages.length" class="text-xs text-muted mt-1">{{ t('sec.common.noPackages') }}</p>
        </div>
        <div>
          <label for="ai-manifest" class="block text-xs font-medium text-muted mb-1.5">
            {{ t('sec.ai.manifest') }} <span class="font-normal">({{ t('sec.ai.optional') }})</span>
          </label>
          <textarea
            id="ai-manifest"
            v-model="triggerForm.manifestText"
            rows="5"
            spellcheck="false"
            :placeholder="t('sec.ai.manifestPlaceholder')"
            class="w-full px-3 py-2 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50 resize-none font-mono"
          />
        </div>
      </div>
      <template #footer>
        <AppButton variant="secondary" size="md" @click="showTriggerDrawer = false">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="md" :loading="triggering" :disabled="triggering || !triggerForm.artifactId" @click="submitTrigger">
          <SparklesIcon v-if="!(triggering)" class="w-3.5 h-3.5" aria-hidden="true" />
          {{ triggering ? t('sec.ai.triggering') : t('sec.ai.start') }}
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
import {
  ArrowPathIcon,
  MagnifyingGlassIcon,
  SparklesIcon,
  ExclamationTriangleIcon,
} from '@heroicons/vue/24/outline'
import EmptyState from '../../components/EmptyState.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import { useToastStore } from '../../stores/toast'
import { useRepoPackages } from '../../composables/useRepoPackages'
import { listAIAnalyses, triggerAIAnalysis, type AIAnalysis } from '../../api/ai'
import { t, tDynamic } from '../../i18n/en'

const PAGE_SIZE = 100 // server maximum
const MAX_PAGES = 10
const POLL_MS = 4000

const toasts = useToastStore()
const { repos, reposLoaded, selectedRepo, packages, findPackage } = useRepoPackages()

const analyses = ref<AIAnalysis[]>([])
const loadingAnalyses = ref(false)
const loadError = ref('')
const triggering = ref(false)
const retriggering = ref<string | null>(null)
const searchQuery = ref('')
const statusFilter = ref('all')
const licenseFilter = ref('all')
const selectedAnalysis = ref<AIAnalysis | null>(null)
const showTriggerDrawer = ref(false)
const triggerForm = ref({ artifactId: '', manifestText: '' })

const statusOptions = computed(() => [
  { label: t('sec.common.all'), value: 'all' },
  { label: t('sec.ai.status.pending'), value: 'pending' },
  { label: t('sec.ai.status.processing'), value: 'processing' },
  { label: t('sec.ai.status.completed'), value: 'completed' },
  { label: t('sec.ai.status.failed'), value: 'failed' },
])

const licenseOptions = computed(() => [
  { label: t('sec.common.all'), value: 'all' },
  { label: t('sec.ai.license.SAFE'), value: 'SAFE' },
  { label: t('sec.ai.license.REVIEW_REQUIRED'), value: 'REVIEW_REQUIRED' },
  { label: t('sec.ai.license.UNKNOWN'), value: 'UNKNOWN' },
])

const countSafe = computed(() => analyses.value.filter((a) => a.license_status === 'SAFE').length)
const countReview = computed(() => analyses.value.filter((a) => a.license_status === 'REVIEW_REQUIRED').length)
const countQuarantine = computed(() => analyses.value.filter((a) => a.quarantine_recommended).length)

const filteredAnalyses = computed(() => {
  let result = analyses.value
  const q = searchQuery.value.trim().toLowerCase()
  if (q) result = result.filter((a) => (a.artifact_name || a.artifact_id).toLowerCase().includes(q))
  if (statusFilter.value !== 'all') result = result.filter((a) => a.status === statusFilter.value)
  if (licenseFilter.value !== 'all') result = result.filter((a) => a.license_status === licenseFilter.value)
  return result
})

function isActive(status: string): boolean {
  return status === 'pending' || status === 'processing'
}

function analysisStatusClass(status: string): string {
  const base = 'inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-caption font-semibold '
  if (status === 'processing') return base + 'text-primary bg-primary/10'
  if (status === 'completed') return base + 'text-success bg-success-soft/60'
  if (status === 'failed') return base + 'text-danger bg-danger-soft'
  return base + 'text-warning bg-warning-soft/60'
}

function analysisStatusLabel(status: string): string {
  return ['pending', 'processing', 'completed', 'failed'].includes(status) ? tDynamic(`sec.ai.status.${status}`) : status
}

function licenseStatusClass(status: string): string {
  const base = 'inline-flex items-center px-2 py-0.5 rounded-md text-caption font-semibold '
  if (status === 'SAFE') return base + 'text-success bg-success-soft/60'
  if (status === 'REVIEW_REQUIRED') return base + 'text-warning bg-warning-soft/60'
  return base + 'text-muted bg-surface-hover'
}

function licenseStatusLabel(status: string): string {
  return ['SAFE', 'REVIEW_REQUIRED', 'UNKNOWN'].includes(status) ? tDynamic(`sec.ai.license.${status}`) : status
}

function truncate(text: string, length: number): string {
  if (!text) return '—'
  return text.length > length ? text.slice(0, length) + '…' : text
}


let pollTimer: ReturnType<typeof setTimeout> | undefined
let loadSeq = 0

function schedulePoll(): void {
  clearTimeout(pollTimer)
  if (analyses.value.some((a) => isActive(a.status))) {
    pollTimer = setTimeout(() => void loadAnalyses(true), POLL_MS)
  }
}

async function fetchAll(repo: string): Promise<AIAnalysis[]> {
  const all: AIAnalysis[] = []
  for (let page = 0; page < MAX_PAGES; page++) {
    const res = await listAIAnalyses(repo, { limit: PAGE_SIZE, offset: page * PAGE_SIZE })
    const batch = res.analyses ?? []
    all.push(...batch)
    if (batch.length < PAGE_SIZE || all.length >= res.total) break
  }
  return all
}

async function loadAnalyses(silent = false): Promise<void> {
  const repo = selectedRepo.value
  clearTimeout(pollTimer)
  // Counted before the early return: a response still in flight for the
  // previous repository must not land after the selection was cleared.
  const seq = ++loadSeq
  if (!repo) {
    analyses.value = []
    loadError.value = ''
    loadingAnalyses.value = false
    return
  }
  if (!silent) loadingAnalyses.value = true
  try {
    const result = await fetchAll(repo)
    if (seq !== loadSeq) return
    analyses.value = result
    loadError.value = ''
    if (selectedAnalysis.value) {
      const id = selectedAnalysis.value.id
      selectedAnalysis.value = result.find((a) => a.id === id) ?? selectedAnalysis.value
    }
  } catch (err) {
    if (seq !== loadSeq) return
    if (!silent) {
      analyses.value = []
      loadError.value = err instanceof Error ? err.message : t('sec.ai.loadFailed')
    }
  } finally {
    if (seq === loadSeq) {
      loadingAnalyses.value = false
      schedulePoll()
    }
  }
}

function openTriggerDrawer(): void {
  triggerForm.value = { artifactId: '', manifestText: '' }
  showTriggerDrawer.value = true
}

async function retrigger(analysis: AIAnalysis): Promise<void> {
  if (retriggering.value) return
  retriggering.value = analysis.id
  try {
    await triggerAIAnalysis(selectedRepo.value, analysis.artifact_id, {
      artifact_format: analysis.artifact_format || undefined,
      artifact_name: analysis.artifact_name || undefined,
      artifact_version: analysis.artifact_version || undefined,
    })
    toasts.success(t('sec.ai.retriggered'))
    await loadAnalyses()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.ai.retriggerFailed'))
  } finally {
    retriggering.value = null
  }
}

async function submitTrigger(): Promise<void> {
  const pkg = findPackage(triggerForm.value.artifactId)
  if (!pkg || !selectedRepo.value || triggering.value) return
  triggering.value = true
  try {
    await triggerAIAnalysis(selectedRepo.value, pkg.id, {
      manifest_text: triggerForm.value.manifestText || undefined,
      artifact_format: pkg.format,
      artifact_name: pkg.name,
      artifact_version: pkg.version,
    })
    toasts.success(t('sec.ai.triggered'))
    showTriggerDrawer.value = false
    await loadAnalyses()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.ai.triggerFailed'))
  } finally {
    triggering.value = false
  }
}

watch(selectedRepo, () => {
  // Another repository's analyses must not stay on screen while this one loads.
  analyses.value = []
  selectedAnalysis.value = null
  void loadAnalyses()
})
onBeforeUnmount(() => {
  clearTimeout(pollTimer)
  loadSeq++
})
</script>

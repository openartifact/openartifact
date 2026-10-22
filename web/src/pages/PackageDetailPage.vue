<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="pkgName">
      <template v-if="currentPkg" #meta>
        <FormatBadge :format="format" />
        <span v-if="currentPkg.version" class="font-mono text-xs bg-surface-hover rounded-md px-2 py-0.5 border border-border text-muted">
          {{ currentPkg.version }}
        </span>
        <span
          v-if="packages.length > 0 && currentPkg?.version === packages[0]?.version"
          class="text-micro font-medium px-2 py-0.5 rounded-full border border-success/30 text-success bg-success-soft"
        >
          {{ t('pkg.latest') }}
        </span>
      </template>
      <template v-if="currentPkg" #actions>
        <div class="inline-flex items-center gap-2 shrink-0">
          <AppButton variant="secondary" @click="copyUrl">
            <ClipboardDocumentIcon class="w-4 h-4 text-muted" aria-hidden="true" />
            {{ t('repoDetail.copyUrl') }}
          </AppButton>
          <AppButton v-if="session.isAdmin" variant="secondary" :to="`/repos/${encodeURIComponent(repoName)}/edit`">
            <Cog6ToothIcon class="w-4 h-4 text-muted" aria-hidden="true" />
            {{ t('nav.settings') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <div v-if="loading" class="text-muted text-sm">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="rounded-xl border border-danger/30 bg-surface p-8 text-center">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <AppButton class="mt-3" variant="secondary" size="sm" @click="load">
        {{ t('browser.retry') }}
      </AppButton>
    </div>
    <EmptyState
      v-else-if="!currentPkg"
      :title="t('pkg.notFound')"
      :description="t('pkg.notFoundHint')"
    />
    <div v-else>
      <!-- Hero Card -->
      <div class="bg-surface rounded-xl border border-border overflow-hidden">
        <!-- Top section -->
        <div class="relative px-6 py-5 border-b border-border">
          <div class="relative">
            <!-- Description -->
            <p class="text-sm text-muted max-w-2xl leading-relaxed">
              {{ repo?.description || repoName }}
            </p>

            <!-- Stats row -->
            <div class="flex items-center gap-5 flex-wrap mt-3.5">
              <div v-if="currentPkg.size" class="leading-tight">
                <div class="text-micro text-muted font-semibold">{{ t('packages.column.size') }}</div>
                <div class="mt-0.5 text-sm font-medium tabular-nums font-mono">{{ formatSize(currentPkg.size) }}</div>
              </div>
              <div v-if="currentPkg.created_at" class="leading-tight">
                <div class="text-micro text-muted font-semibold">{{ t('packages.column.uploaded') }}</div>
                <div class="mt-0.5 text-sm font-medium tabular-nums">{{ formatDate(currentPkg.created_at) }}</div>
              </div>
              <div v-if="currentPkg.sha256" class="leading-tight">
                <div class="text-micro text-muted font-semibold">{{ t('packages.column.sha') }}</div>
                <button
                  type="button"
                  class="mt-0.5 text-sm font-medium tabular-nums font-mono text-left hover:text-primary"
                  :title="`${currentPkg.sha256} — ${t('common.copy')}`"
                  @click="copySha(currentPkg.sha256)"
                >{{ currentPkg.sha256.slice(0, 12) }}</button>
              </div>
            </div>
          </div>
        </div>

        <!-- Tabs (inside the card) -->
        <div class="flex items-center gap-1 px-3 overflow-x-auto">
          <button
            v-for="tab in tabs"
            :key="tab.key"
            class="relative h-11 px-3 text-sm font-medium transition-colors cursor-pointer shrink-0"
            :class="activeTab === tab.key ? 'text-foreground' : 'text-muted hover:text-foreground'"
            @click="activeTab = tab.key"
          >
            <span class="inline-flex items-center gap-1.5">
              {{ tab.label }}
              <span
                v-if="tab.key === 'versions' && packages.length > 0"
                class="text-micro font-mono px-1.5 py-0.5 rounded"
                :class="activeTab === 'versions' ? 'bg-primary-soft text-primary' : 'bg-surface-hover text-muted'"
              >
                {{ packages.length }}
              </span>
            </span>
            <span
              v-if="activeTab === tab.key"
              class="absolute left-2 right-2 -bottom-px h-[2px] bg-primary rounded-full"
            />
          </button>
        </div>
      </div>

      <!-- Pull Command -->
      <div class="bg-surface-hover rounded-lg border border-border p-3 mt-4 flex items-center gap-2">
        <span class="w-2 h-2 rounded-full bg-success shrink-0" />
        <code class="font-mono text-xs text-foreground/90 flex-1 truncate">{{ pullCommand }}</code>
        <button
          @click="copyCommand"
          class="flex-shrink-0 text-muted hover:text-foreground transition-colors"
          :title="copied ? t('pkg.copied') : t('pkg.copy')"
          :aria-label="t('pkg.copy')"
        >
          <ClipboardDocumentIcon class="w-4 h-4" aria-hidden="true" />
        </button>
      </div>

      <!-- Tab Content: Overview -->
      <div v-if="activeTab === 'overview'" class="mt-6">
        <div class="bg-surface rounded-lg border border-border p-6">
          <div class="space-y-0">
            <div v-if="currentPkg.sha256" class="flex items-center justify-between gap-4 py-2.5">
              <span class="shrink-0 text-sm text-muted">{{ t('packages.column.sha') }}</span>
              <button
                type="button"
                class="min-w-0 break-all text-right text-sm text-foreground font-mono hover:text-primary"
                :title="t('common.copy')"
                @click="copySha(currentPkg.sha256)"
              >{{ currentPkg.sha256 }}</button>
            </div>
            <p v-else class="text-sm text-muted">{{ t('pkg.overview.noChecksum') }}</p>
          </div>
        </div>
      </div>

      <!-- Tab Content: Versions -->
      <div v-if="activeTab === 'versions'" class="mt-6">
        <div class="bg-surface rounded-lg border border-border overflow-hidden">
          <table class="w-full text-sm">
            <thead class="border-b border-border bg-surface-hover/40">
              <tr>
                <th class="px-4 py-3 text-left text-muted font-medium">{{ t('packages.column.version') }}</th>
                <th class="px-4 py-3 text-left text-muted font-medium">{{ t('packages.column.size') }}</th>
                <th class="px-4 py-3 text-left text-muted font-medium">{{ t('packages.column.uploaded') }}</th>
                <th class="px-4 py-3 text-left text-muted font-medium">{{ t('packages.column.sha') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              <tr
                v-for="(pkg, idx) in packages"
                :key="pkg.id"
                class="hover:bg-surface-hover/40 transition-colors"
              >
                <td class="px-4 py-3">
                  <div class="flex items-center gap-2">
                    <span class="text-foreground font-mono text-sm">{{ pkg.version }}</span>
                    <span
                      v-if="idx === 0"
                      class="text-micro font-mono px-1.5 py-0.5 rounded bg-primary-soft text-primary"
                    >{{ t('pkg.latest') }}</span>
                  </div>
                </td>
                <td class="px-4 py-3 text-foreground font-mono text-xs tabular-nums">{{ pkg.size ? formatSize(pkg.size) : '—' }}</td>
                <td class="px-4 py-3 text-foreground text-xs">{{ pkg.created_at ? formatDate(pkg.created_at) : '—' }}</td>
                <td class="px-4 py-3 text-foreground font-mono text-xs">{{ pkg.sha256 ? pkg.sha256.slice(0, 12) : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Tab Content: Properties -->
      <div v-if="activeTab === 'properties'" class="mt-6">
        <div class="bg-surface rounded-lg border border-border overflow-hidden">
          <!-- Header -->
          <div class="px-5 py-4 flex items-center justify-between border-b border-border">
            <div class="flex items-center gap-2">
              <h3 class="text-sm font-semibold text-foreground">{{ t('pkg.props.title') }}</h3>
              <span
                v-if="Object.keys(properties).length > 0"
                class="px-1.5 py-0.5 rounded-md bg-surface-hover text-caption font-mono font-medium text-muted"
              >
                {{ Object.keys(properties).length }}
              </span>
            </div>
            <AppButton size="sm" @click="addingProp = true">
              <PlusIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('pkg.props.add') }}
            </AppButton>
          </div>

          <!-- Add property inline form -->
          <div v-if="addingProp" class="px-5 py-4 border-b border-border bg-surface-hover/20">
            <div class="flex items-center gap-2">
              <input
                v-model="newPropKey"
                type="text"
                :placeholder="t('pkg.props.key')"
                class="flex-1 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
                @keydown.enter="saveNewProp"
                @keydown.escape="cancelAddProp"
              />
              <input
                v-model="newPropValue"
                type="text"
                :placeholder="t('pkg.props.value')"
                class="flex-1 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
                @keydown.enter="saveNewProp"
                @keydown.escape="cancelAddProp"
              />
              <AppButton
                class="shrink-0"
                size="sm"
                :loading="propsSaving"
                :disabled="!newPropKey.trim()"
                @click="saveNewProp"
              >
                <CheckIcon v-if="!propsSaving" class="w-4 h-4" aria-hidden="true" />
                {{ t('pkg.props.addShort') }}
              </AppButton>
              <button
                type="button"
                class="p-2 rounded-lg text-muted hover:text-foreground hover:bg-surface-hover transition-colors shrink-0"
                :aria-label="t('common.cancel')"
                @click="cancelAddProp"
              >
                <XMarkIcon class="w-4 h-4" />
              </button>
            </div>
          </div>

          <!-- Properties loading -->
          <div v-if="propsLoading" class="px-5 py-6 text-center text-sm text-muted">
            {{ t('pkg.props.loading') }}
          </div>

          <!-- Empty state -->
          <div
            v-else-if="Object.keys(properties).length === 0 && !addingProp"
            class="py-12 text-center"
          >
            <div class="w-10 h-10 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
              <TagIcon class="w-5 h-5 text-muted" />
            </div>
            <p class="text-sm font-medium text-foreground">{{ t('pkg.props.none') }}</p>
            <p class="text-xs text-muted mt-1 mb-4">{{ t('pkg.props.noneHint') }}</p>
            <AppButton size="sm" @click="addingProp = true">
              <PlusIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('pkg.props.add') }}
            </AppButton>
          </div>

          <!-- Properties table -->
          <table v-else-if="Object.keys(properties).length > 0" class="w-full">
            <thead>
              <tr class="bg-surface-hover/30">
                <th class="text-left text-caption font-semibold text-muted px-5 py-2.5 w-1/3">
                  {{ t('pkg.props.key') }}
                </th>
                <th class="text-left text-caption font-semibold text-muted px-3 py-2.5">
                  {{ t('pkg.props.value') }}
                </th>
                <th class="w-10" />
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              <tr
                v-for="(value, key) in properties"
                :key="key"
                class="hover:bg-surface-hover/20 transition-colors group"
              >
                <td class="px-5 py-3">
                  <span class="font-mono text-xs font-medium text-foreground">{{ key }}</span>
                </td>
                <td class="px-3 py-3">
                  <!-- Inline edit mode -->
                  <template v-if="editingKey === key">
                    <div class="flex items-center gap-2">
                      <input
                        v-model="editingValue"
                        type="text"
                        class="flex-1 px-2 py-1 rounded border border-primary bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
                        @keydown.enter="saveEditProp(String(key))"
                        @keydown.escape="cancelEditProp"
                        autofocus
                      />
                      <button
                        type="button"
                        class="p-1 rounded text-success hover:bg-success/10 transition-colors shrink-0"
                        :aria-label="t('common.save')"
                        @click="saveEditProp(String(key))"
                      >
                        <CheckIcon class="w-4 h-4" />
                      </button>
                      <button
                        type="button"
                        class="p-1 rounded text-muted hover:text-foreground hover:bg-surface-hover transition-colors shrink-0"
                        :aria-label="t('common.cancel')"
                        @click="cancelEditProp"
                      >
                        <XMarkIcon class="w-4 h-4" />
                      </button>
                    </div>
                  </template>
                  <!-- Display mode -->
                  <button
                    v-else
                    type="button"
                    class="font-mono text-xs text-foreground/80 hover:text-primary transition-colors text-left w-full truncate"
                    :title="t('pkg.props.clickEdit')"
                    @click="startEditProp(String(key), String(value))"
                  >
                    {{ value }}
                  </button>
                </td>
                <td class="px-3 py-3">
                  <button
                    type="button"
                    :title="t('pkg.props.delete')" :aria-label="t('pkg.props.delete')"
                    class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors opacity-0 group-hover:opacity-100"
                    @click="deleteProp(String(key))"
                  >
                    <TrashIcon class="w-4 h-4" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Tab Content: Security -->
      <div v-if="activeTab === 'security'" class="mt-6">
        <div v-if="!session.isAdmin" class="bg-surface rounded-lg border border-border p-6">
          <EmptyState :title="t('pkg.security.adminOnly')" :description="t('pkg.security.adminOnlyHint')" />
        </div>
        <div v-else-if="scanLoading" class="text-sm text-muted">{{ t('common.loading') }}</div>
        <div v-else-if="scanError" class="bg-surface rounded-lg border border-danger/30 p-6 text-sm text-danger">
          {{ scanError }}
        </div>
        <div v-else-if="!scan" class="bg-surface rounded-lg border border-border p-6">
          <EmptyState :title="t('pkg.security.noScan')" :description="t('pkg.security.noScanHint')" />
        </div>
        <div v-else class="bg-surface rounded-lg border border-border p-6 space-y-4">
          <div class="flex items-center gap-3 flex-wrap">
            <ShieldCheckIcon class="w-6 h-6" :class="scanIconClass" aria-hidden="true" />
            <h3 class="text-sm font-semibold text-foreground" data-testid="scan-headline">{{ scanHeadline }}</h3>
            <span class="text-xs text-muted">{{ scan.scanner }}</span>
            <VulnScanStatus :status="scan.scan_status" :scan-error="scan.scan_error" :failed-scanners="scan.failed_scanners" />
          </div>
          <dl v-if="scanCountsState === 'counts' || scanCountsState === 'clean' || scanCountsState === 'partialEmpty'" class="grid grid-cols-2 sm:grid-cols-4 gap-3 text-sm">
            <div><dt class="text-xs text-muted">{{ t('pkg.security.critical') }}</dt><dd class="font-mono">{{ scan.critical_count }}</dd></div>
            <div><dt class="text-xs text-muted">{{ t('pkg.security.high') }}</dt><dd class="font-mono">{{ scan.high_count }}</dd></div>
            <div><dt class="text-xs text-muted">{{ t('pkg.security.medium') }}</dt><dd class="font-mono">{{ scan.medium_count }}</dd></div>
            <div><dt class="text-xs text-muted">{{ t('pkg.security.low') }}</dt><dd class="font-mono">{{ scan.low_count }}</dd></div>
          </dl>
          <p v-if="scan.scanned_at" class="text-xs text-muted">
            {{ t('pkg.security.scannedAt') }} {{ formatDateTime(scan.scanned_at) }}
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../components/PageHeader.vue'
import { formatDate, formatDateTime } from '../lib/datetime'
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useRoute } from 'vue-router'
import {
  ClipboardDocumentIcon,
  ShieldCheckIcon,
  Cog6ToothIcon,
  PlusIcon,
  CheckIcon,
  XMarkIcon,
  TrashIcon,
  TagIcon,
} from '@heroicons/vue/24/outline'
import { getRepository, listPackageVersions } from '../api/repositories'
import type { Repository, PackageItem } from '../api/repositories'
import { pullCommand as buildPullCommand, repoBaseUrl } from '../lib/installCommands'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n/en'
import { request, ApiError } from '../api/client'
import { useToastStore } from '../stores/toast'
import { listVulnScans, type VulnScan } from '../api/security'
import { vulnCountsState } from '../lib/scanStatus'
import AppButton from '../components/AppButton.vue'
import FormatBadge from '../components/FormatBadge.vue'
import EmptyState from '../components/EmptyState.vue'
import VulnScanStatus from '../components/VulnScanStatus.vue'

const tabs = [
  { key: 'overview', label: t('pkg.tab.overview') },
  { key: 'versions', label: t('pkg.tab.versions') },
  { key: 'properties', label: t('pkg.tab.properties') },
  { key: 'security', label: t('pkg.tab.security') },
] as const

const route = useRoute()

const repoName = computed(() => route.params.name as string)
const format = computed(() => route.params.format as string)
const pkgName = computed(() => route.params.pkg as string)

const toasts = useToastStore()
const session = useSessionStore()
const repo = ref<Repository | null>(null)
const packages = ref<PackageItem[]>([])
const loading = ref(true)
const loadError = ref('')
const activeTab = ref('overview')
const copied = ref(false)

// ─── Properties state ─────────────────────────────────────────────────────────
const properties = ref<Record<string, string>>({})
const propsLoading = ref(false)
const propsSaving = ref(false)
const addingProp = ref(false)
const newPropKey = ref('')
const newPropValue = ref('')
const editingKey = ref<string | null>(null)
const editingValue = ref('')

const artifactId = computed(() => currentPkg.value?.id || '')

let propsGeneration = 0

async function loadProperties(): Promise<void> {
  const id = artifactId.value
  if (!id) return
  const generation = ++propsGeneration
  propsLoading.value = true
  try {
    const res = await request<Record<string, string>>(
      `/api/v1/repositories/${encodeURIComponent(repoName.value)}/artifacts/${encodeURIComponent(id)}/properties`,
    )
    if (generation !== propsGeneration) return
    properties.value = res ?? {}
  } catch (err) {
    if (generation !== propsGeneration) return
    if (!(err instanceof ApiError && (err.status === 404 || err.status === 501))) {
      toasts.error(t('pkg.props.loadFailed'))
    }
    properties.value = {}
  } finally {
    if (generation === propsGeneration) propsLoading.value = false
  }
}

function cancelAddProp(): void {
  addingProp.value = false
  newPropKey.value = ''
  newPropValue.value = ''
}

async function saveNewProp(): Promise<void> {
  const key = newPropKey.value.trim()
  if (!key || !artifactId.value) return
  propsSaving.value = true
  try {
    const updated = { ...properties.value, [key]: newPropValue.value }
    await request<void>(
      `/api/v1/repositories/${encodeURIComponent(repoName.value)}/artifacts/${encodeURIComponent(artifactId.value)}/properties`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ properties: { [key]: newPropValue.value } }),
      },
    )
    properties.value = updated
    cancelAddProp()
    toasts.success(t('pkg.props.added'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('pkg.props.addFailed'))
  } finally {
    propsSaving.value = false
  }
}

function startEditProp(key: string, value: string): void {
  editingKey.value = key
  editingValue.value = value
}

function cancelEditProp(): void {
  editingKey.value = null
  editingValue.value = ''
}

async function saveEditProp(key: string): Promise<void> {
  if (!artifactId.value) return
  try {
    await request<void>(
      `/api/v1/repositories/${encodeURIComponent(repoName.value)}/artifacts/${encodeURIComponent(artifactId.value)}/properties`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ properties: { [key]: editingValue.value } }),
      },
    )
    properties.value = { ...properties.value, [key]: editingValue.value }
    cancelEditProp()
    toasts.success(t('pkg.props.updated'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('pkg.props.updateFailed'))
  }
}

async function deleteProp(key: string): Promise<void> {
  if (!artifactId.value) return
  if (!window.confirm(t('pkg.props.confirmDelete').replace('{key}', key))) return
  try {
    await request<void>(
      `/api/v1/repositories/${encodeURIComponent(repoName.value)}/artifacts/${encodeURIComponent(artifactId.value)}/properties/${encodeURIComponent(key)}`,
      { method: 'DELETE' },
    )
    const next = { ...properties.value }
    delete next[key]
    properties.value = next
    toasts.success(t('pkg.props.deleted'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('pkg.props.deleteFailed'))
  }
}

// Newest version first.
const currentPkg = computed(() => packages.value[0] || null)

// ─── Security (latest vulnerability scan for the current version) ───────────

const scan = ref<VulnScan | null>(null)
const scanCountsState = computed(() => (scan.value ? vulnCountsState(scan.value) : 'pending'))

// A failed scan never reads as "clean": it says the version was not scanned.
const scanHeadline = computed(() => {
  const s = scan.value
  if (!s) return ''
  switch (scanCountsState.value) {
    case 'counts':
      return `${s.vuln_count} ${t('pkg.security.vulns')}`
    case 'notScanned':
      return t('pkg.security.failed')
    case 'partialEmpty':
      return t('pkg.security.partial')
    case 'clean':
      return t('pkg.security.clean')
    default:
      return t('pkg.security.scanPending')
  }
})

const scanIconClass = computed(() => {
  switch (scanCountsState.value) {
    case 'counts':
    case 'notScanned':
      return 'text-danger'
    case 'partialEmpty':
      return 'text-warning'
    case 'clean':
      return 'text-success'
    default:
      return 'text-muted'
  }
})
const scanLoading = ref(false)
const scanError = ref('')
let scanGeneration = 0

async function loadScan(): Promise<void> {
  const pkg = currentPkg.value
  if (!pkg || !session.isAdmin) return
  const generation = ++scanGeneration
  scanLoading.value = true
  scanError.value = ''
  try {
    const scans = await listVulnScans(repoName.value)
    if (generation !== scanGeneration) return
    scan.value =
      scans.find(
        (sc) => sc.package_name === pkg.name && sc.package_version === pkg.version,
      ) ?? null
  } catch (err) {
    if (generation !== scanGeneration) return
    scan.value = null
    scanError.value = err instanceof Error ? err.message : t('pkg.security.loadFailed')
  } finally {
    if (generation === scanGeneration) scanLoading.value = false
  }
}

const pullCommand = computed(() =>
  currentPkg.value
    ? buildPullCommand(format.value, window.location.origin, repoName.value, pkgName.value, currentPkg.value.version)
    : '',
)

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

async function copyUrl(): Promise<void> {
  try {
    await navigator.clipboard.writeText(repoBaseUrl(format.value, window.location.origin, repoName.value))
    toasts.success(t('repoDetail.urlCopied'))
  } catch {
    toasts.error(t('repoDetail.urlCopyFailed'))
  }
}

async function copySha(sha: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(sha)
    toasts.success(t('common.copied'))
  } catch {
    toasts.error(t('settings.tokens.copyFailed'))
  }
}

let copiedTimer: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(copiedTimer))

async function copyCommand(): Promise<void> {
  try {
    await navigator.clipboard.writeText(pullCommand.value)
    copied.value = true
    clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    // best-effort
  }
}

let loadGeneration = 0

async function load(): Promise<void> {
  const generation = ++loadGeneration
  loading.value = true
  loadError.value = ''
  try {
    const [repoRes, versions] = await Promise.all([
      getRepository(repoName.value),
      listPackageVersions(repoName.value, pkgName.value),
    ])
    if (generation !== loadGeneration) return
    repo.value = repoRes
    packages.value = versions
      .filter((p) => p.name === pkgName.value)
      .sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''))
  } catch (err) {
    if (generation !== loadGeneration) return
    packages.value = []
    loadError.value = err instanceof Error ? err.message : t('pkg.loadFailed')
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

// The router reuses this component across /repos/:name/packages/:format/:pkg
// navigations, so reload (and reset per-package state) on any param change.
watch(
  () => [repoName.value, format.value, pkgName.value],
  () => {
    activeTab.value = 'overview'
    propsGeneration++
    propsLoading.value = false
    properties.value = {}
    scan.value = null
    void load()
  },
  { immediate: true },
)

// The tab data loads when the tab opens and again once the package it belongs
// to is known (the tab can be opened while the package list is still loading).
watch([activeTab, artifactId], ([tab]) => {
  if (tab === 'properties') void loadProperties()
  if (tab === 'security') void loadScan()
})
</script>

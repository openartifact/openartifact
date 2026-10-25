<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <RepoHeader :name="repoName" :repo="repo" active="browse" />

    <!-- Toolbar -->
    <div class="flex flex-wrap items-center justify-between gap-3 mb-4">
      <div class="relative">
        <MagnifyingGlassIcon
          class="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted pointer-events-none"
          aria-hidden="true"
        />
        <input
          v-model="searchQuery"
          type="search"
          :placeholder="t('browser.filterPlaceholder')"
          :aria-label="t('browser.filterPlaceholder')"
          class="h-8 w-64 pl-7 pr-2 rounded-md text-xs bg-surface border border-border-strong text-foreground placeholder:text-muted focus:ring-2 focus:ring-primary"
        />
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <!-- Grid/List toggle -->
        <div class="rounded-md border border-border p-0.5 bg-surface flex items-center">
          <button
            class="p-1 rounded transition-colors"
            :class="viewMode === 'list' ? 'bg-primary-soft text-primary' : 'text-muted hover:text-foreground'"
            :aria-label="t('browser.listView')"
            @click="viewMode = 'list'"
          >
            <Bars3Icon class="w-4 h-4" aria-hidden="true" />
          </button>
          <button
            class="p-1 rounded transition-colors"
            :class="viewMode === 'grid' ? 'bg-primary-soft text-primary' : 'text-muted hover:text-foreground'"
            :aria-label="t('browser.gridView')"
            @click="viewMode = 'grid'"
          >
            <Squares2X2Icon class="w-4 h-4" aria-hidden="true" />
          </button>
        </div>

        <!-- Sort button -->
        <AppButton variant="secondary" size="sm" @click="sortKey = sortKey === 'name' ? 'created' : 'name'">
          <ArrowsUpDownIcon class="w-3.5 h-3.5" aria-hidden="true" />
          {{ t('browser.sort') }}: {{ sortKey === 'name' ? t('packages.column.name') : t('packages.column.uploaded') }}
        </AppButton>

        <!-- Refresh -->
        <AppButton
          variant="secondary"
          size="sm"
          :loading="loading"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
          @click="load"
        >
          <ArrowPathIcon v-if="!loading" class="w-3.5 h-3.5" aria-hidden="true" />
        </AppButton>
      </div>
    </div>

    <!-- Loading state -->
    <div v-if="loading && items.length === 0" class="py-16 flex flex-col items-center gap-3 text-muted">
      <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
      <span class="text-sm">{{ t('common.loading') }}</span>
    </div>

    <!-- Error state -->
    <div
      v-else-if="error"
      class="bg-surface rounded-lg border border-danger/30 p-8 flex flex-col items-center gap-3 text-center"
    >
      <ExclamationTriangleIcon class="w-10 h-10 text-danger" aria-hidden="true" />
      <div>
        <p class="font-medium text-foreground mb-1">{{ t('browser.errorTitle') }}</p>
        <p class="text-sm text-muted">{{ error }}</p>
      </div>
      <AppButton class="mt-2" @click="load">
        {{ t('browser.retry') }}
      </AppButton>
    </div>

    <!-- Empty state -->
    <EmptyState
      v-else-if="!loading && total === 0"
      :title="searchQuery ? t('browser.noMatchTitle') : t('browser.emptyTitle')"
      :description="searchQuery ? t('browser.noMatchHint') : t('browser.emptyHint')"
    />

    <!-- List view -->
    <div v-else-if="viewMode === 'list'" class="bg-surface rounded-lg border border-border overflow-hidden">
      <!-- List header -->
      <div
        class="grid grid-cols-[28px_minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_120px] items-center gap-3 px-4 py-2.5 border-b border-border bg-surface-hover text-xs font-semibold text-muted"
      >
        <div>
          <input
            type="checkbox"
            :checked="allPageSelected"
            :indeterminate="somePageSelected && !allPageSelected"
            class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer"
            :aria-label="t('browser.selectAll')"
            @change="toggleAll"
          />
        </div>
        <div>{{ t('packages.column.name') }}</div>
        <div>{{ t('packages.column.version') }}</div>
        <div class="text-right">{{ t('packages.column.size') }}</div>
        <div>{{ t('packages.column.uploaded') }}</div>
        <div class="text-right">{{ t('packages.column.digest') }}</div>
      </div>

      <!-- List rows -->
      <div class="divide-y divide-border">
        <div
          v-for="item in items"
          :key="item.id"
          class="grid grid-cols-[28px_minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_120px] items-center gap-3 px-4 py-2.5 transition-colors group"
          :class="{ 'bg-primary-soft/40': selectedIds.has(itemKey(item)) }"
        >
          <!-- Checkbox -->
          <div>
            <input
              type="checkbox"
              :checked="selectedIds.has(itemKey(item))"
              class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer opacity-0 group-hover:opacity-100 transition-opacity"
              :class="{ 'opacity-100': selectedIds.has(itemKey(item)) }"
              :aria-label="tp('browser.selectItem', { name: item.name })"
              @change="toggleItem(item)"
            />
          </div>

          <!-- Name -->
          <div class="flex items-center gap-2 min-w-0">
            <div
              class="w-7 h-7 rounded-md flex items-center justify-center shrink-0"
              :class="fileTypeClasses(item.name)"
            >
              <ArchiveBoxIcon class="w-4 h-4" aria-hidden="true" />
            </div>
            <router-link
              :to="`/repos/${repoName}/packages/${encodeURIComponent(item.format)}/${encodeURIComponent(item.name)}`"
              class="text-sm font-medium text-foreground hover:text-primary transition-colors truncate"
            >{{ item.name }}</router-link>
            <span
              v-if="item.version === 'latest'"
              class="text-micro font-mono px-1.5 py-0.5 rounded bg-primary-soft text-primary shrink-0"
            >{{ t('pkg.latest') }}</span>
          </div>

          <!-- Version -->
          <div class="font-mono text-xs truncate">
            {{ item.version || '—' }}
          </div>

          <!-- Size -->
          <div class="font-mono text-xs tabular-nums text-muted text-right">
            {{ formatBytesOrDash(item.size) }}
          </div>

          <!-- Pushed -->
          <div class="text-xs text-muted truncate">
            {{ formatDate(item.created_at) }}
          </div>

          <!-- Digest + download -->
          <div class="flex items-center justify-end gap-2 min-w-0">
            <span class="text-xs font-mono text-muted truncate">
              {{ item.sha256 ? `sha256:${item.sha256.slice(0, 12)}` : '—' }}
            </span>
          </div>
        </div>
      </div>
    </div>

    <!-- Grid view -->
    <div v-else class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-3 p-4 bg-surface rounded-lg border border-border">
      <div
        v-for="item in items"
        :key="item.id"
        class="rounded-lg border p-4 transition-all cursor-pointer group"
        :class="selectedIds.has(itemKey(item))
          ? 'border-primary bg-primary-soft/40'
          : 'border-border bg-surface hover:border-primary/30 hover:-translate-y-0.5'
        "
        @click="toggleItem(item)"
      >
        <div class="flex items-start justify-between mb-3">
          <div
            class="w-10 h-10 rounded-md flex items-center justify-center shrink-0"
            :class="fileTypeClasses(item.name)"
          >
            <ArchiveBoxIcon class="w-5 h-5" aria-hidden="true" />
          </div>
          <input
            type="checkbox"
            :checked="selectedIds.has(itemKey(item))"
            class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer opacity-0 group-hover:opacity-100 transition-opacity"
            :class="{ 'opacity-100': selectedIds.has(itemKey(item)) }"
            @click.stop
            @change="toggleItem(item)"
          />
        </div>
        <router-link
          :to="`/repos/${repoName}/packages/${encodeURIComponent(item.format)}/${encodeURIComponent(item.name)}`"
          class="text-sm font-medium text-foreground hover:text-primary transition-colors line-clamp-2"
          @click.stop
        >{{ item.name }}</router-link>
        <div class="mt-1 font-mono text-xs text-muted truncate">
          {{ item.version || '—' }}
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-muted">
          <span class="font-mono tabular-nums">{{ formatBytesOrDash(item.size) }}</span>
          <span>{{ formatDate(item.created_at) }}</span>
        </div>
      </div>
    </div>

    <!-- Pagination (both views; the server pages the list) -->
    <Pagination
      v-if="!error && total > limit"
      class="mt-3 rounded-lg border border-border"
      :total="total"
      :page="page"
      :limit="limit"
      @change="page = $event"
    />

    <!-- Bulk actions bar -->
    <BulkActionsBar
      :selected-items="selectedItemKeys"
      :repo-name="repoName"
      @clear="clearSelection"
      @deleted="onDeleted"
      @error="onBulkError"
    />
  </div>
</template>

<script setup lang="ts">
import RepoHeader from '../components/RepoHeader.vue'
import { formatDate } from '../lib/datetime'
import { formatBytesOrDash } from '../lib/quota'
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useRoute } from 'vue-router'
import { listPackages, getRepository } from '../api/repositories'
import type { Repository, PackageItem, PackageSort } from '../api/repositories'
import AppButton from '../components/AppButton.vue'
import EmptyState from '../components/EmptyState.vue'
import Pagination from '../components/Pagination.vue'
import BulkActionsBar from '../components/BulkActionsBar.vue'
import { useToastStore } from '../stores/toast'
import {
  MagnifyingGlassIcon,
  ArchiveBoxIcon,
  ArrowPathIcon,
  ExclamationTriangleIcon,
  Bars3Icon,
  Squares2X2Icon,
  ArrowsUpDownIcon,
} from '@heroicons/vue/24/outline'
import { t, tp } from '../i18n/en'

const route = useRoute()
const repoName = computed(() => route.params.name as string)
const toasts = useToastStore()

const repo = ref<Repository | null>(null)
// The page of packages on screen; filtering, ordering and paging happen on the server.
const items = ref<PackageItem[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)
const searchQuery = ref('')
// The search text the list was last requested with (searchQuery after a typing pause).
const appliedQuery = ref('')
const sortKey = ref<PackageSort>('name')
const page = ref(1)
const limit = 25
const SEARCH_DEBOUNCE_MS = 300
const viewMode = ref<'list' | 'grid'>('list')

// ── Selection state ────────────────────────────────────────────────────────
const selectedIds = ref<Set<string>>(new Set())

function itemKey(item: PackageItem): string {
  return item.id
}

const selectedItemKeys = computed(() => [...selectedIds.value])

const allPageSelected = computed(() =>
  items.value.length > 0 &&
  items.value.every((i) => selectedIds.value.has(itemKey(i))),
)

const somePageSelected = computed(() =>
  items.value.some((i) => selectedIds.value.has(itemKey(i))),
)

function toggleItem(item: PackageItem): void {
  const key = itemKey(item)
  const next = new Set(selectedIds.value)
  if (next.has(key)) {
    next.delete(key)
  } else {
    next.add(key)
  }
  selectedIds.value = next
}

function toggleAll(): void {
  if (allPageSelected.value) {
    const next = new Set(selectedIds.value)
    items.value.forEach((i) => next.delete(itemKey(i)))
    selectedIds.value = next
  } else {
    const next = new Set(selectedIds.value)
    items.value.forEach((i) => next.add(itemKey(i)))
    selectedIds.value = next
  }
}

function clearSelection(): void {
  selectedIds.value = new Set()
}

// ── File type colors ───────────────────────────────────────────────────────
function fileTypeClasses(name: string): string {
  const lower = name.toLowerCase()
  if (lower.endsWith('.deb') || lower.endsWith('.rpm')) {
    return 'bg-danger-soft text-danger'
  }
  if (lower.endsWith('.jar') || lower.endsWith('.war') || lower.endsWith('.ear')) {
    return 'bg-warning-soft text-warning'
  }
  if (lower.endsWith('.tgz') || lower.endsWith('.tar.gz') || lower.endsWith('.tar')) {
    return 'bg-info-soft text-info'
  }
  if (lower.endsWith('.whl') || lower.endsWith('.egg')) {
    return 'bg-primary-soft text-primary'
  }
  return 'bg-surface-hover text-muted'
}

// ── Bulk action handlers ───────────────────────────────────────────────────
function onDeleted(ids: string[]): void {
  clearSelection()
  // Nothing was deleted (every request failed): the error toast says so.
  if (ids.length > 0) toasts.success(tp('bulk.deleteSuccess', { n: String(ids.length) }))
  void load()
}

function onBulkError(msg: string): void {
  toasts.error(t('bulk.deleteFailed'), msg)
}

// ─────────────────────────────────────────────────────────────────────────



let loadGeneration = 0

async function loadRepo(): Promise<void> {
  try {
    repo.value = await getRepository(repoName.value)
  } catch (err) {
    // The header is cosmetic; the package list reports its own failure.
    repo.value = null
    console.error('[browser] repository lookup failed', err)
  }
}

async function load(): Promise<void> {
  const generation = ++loadGeneration
  loading.value = true
  error.value = null
  try {
    const res = await listPackages(repoName.value, {
      page: page.value,
      limit,
      q: appliedQuery.value,
      sort: sortKey.value,
    })
    if (generation !== loadGeneration) return
    // Rows were deleted from the last page: step back to the last page that exists.
    if (res.packages.length === 0 && res.total > 0 && page.value > 1) {
      page.value = Math.ceil(res.total / limit)
      return
    }
    items.value = res.packages
    total.value = res.total
  } catch (err) {
    if (generation !== loadGeneration) return
    error.value = err instanceof Error ? err.message : t('browser.loadFailed')
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

// Search runs on the server once typing pauses.
let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(searchQuery, (value) => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { appliedQuery.value = value.trim() }, SEARCH_DEBOUNCE_MS)
})
onBeforeUnmount(() => clearTimeout(searchTimer))

// A new repository, search text or order starts over at page 1. Declared before
// the loading watcher so both changes land in one load.
watch([repoName, appliedQuery, sortKey], () => { page.value = 1 })
watch(repoName, () => {
  clearSelection()
  void loadRepo()
}, { immediate: true })
watch([repoName, appliedQuery, sortKey, page], () => { void load() }, { immediate: true })
</script>

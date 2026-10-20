<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <!-- Toolbar -->
    <div class="flex flex-wrap items-center gap-3 mb-4">
      <!-- Breadcrumbs -->
      <nav class="flex items-center gap-1 text-sm min-w-0" aria-label="Breadcrumb">
        <router-link to="/repos" class="flex items-center gap-1 text-muted hover:text-primary transition-colors shrink-0">
          <CubeIcon class="w-4 h-4" aria-hidden="true" />
          <span class="font-medium">{{ repoName }}</span>
        </router-link>
        <ChevronRightIcon class="w-3 h-3 text-muted shrink-0" aria-hidden="true" />
        <span class="text-foreground font-medium truncate font-mono">{{ t('browser.title') }}</span>
      </nav>

      <div class="ml-auto flex items-center gap-2">
        <!-- Filter input -->
        <div class="relative">
          <MagnifyingGlassIcon
            class="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted pointer-events-none"
            aria-hidden="true"
          />
          <input
            v-model="searchQuery"
            type="search"
            :placeholder="t('browser.filterPlaceholder')"
            class="h-8 w-56 pl-7 pr-2 rounded-md text-xs bg-surface-hover/60 border border-border text-foreground placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-primary"
          />
        </div>

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
        <button
          class="flex items-center gap-1 px-2.5 h-8 text-xs rounded-md border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
          @click="sortKey = sortKey === 'name' ? 'created' : 'name'"
        >
          <ArrowsUpDownIcon class="w-3.5 h-3.5" aria-hidden="true" />
          {{ t('browser.sort') }}: {{ sortKey === 'name' ? t('packages.column.name') : t('packages.column.uploaded') }}
        </button>

        <!-- Refresh -->
        <button
          class="flex items-center justify-center w-8 h-8 rounded-md border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50"
          :disabled="loading"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
          @click="load"
        >
          <ArrowPathIcon class="w-3.5 h-3.5" :class="{ 'animate-spin': loading }" aria-hidden="true" />
        </button>
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
      <button
        class="mt-2 px-4 py-2 text-sm rounded-md bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
        @click="load"
      >
        {{ t('browser.retry') }}
      </button>
    </div>

    <!-- Empty state -->
    <EmptyState
      v-else-if="!loading && filteredItems.length === 0"
      :title="searchQuery ? t('browser.noMatchTitle') : t('browser.emptyTitle')"
      :description="searchQuery ? t('browser.noMatchHint') : t('browser.emptyHint')"
    />

    <!-- List view -->
    <div v-else-if="viewMode === 'list'" class="bg-surface rounded-lg border border-border overflow-hidden">
      <!-- List header -->
      <div
        class="grid grid-cols-[28px_minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_120px] items-center gap-3 px-4 py-2.5 border-b border-border bg-surface-hover text-xs font-semibold text-muted uppercase tracking-wider"
      >
        <div>
          <input
            type="checkbox"
            :checked="allPageSelected"
            :indeterminate="somePageSelected && !allPageSelected"
            class="w-4 h-4 rounded border-border text-primary focus:ring-primary cursor-pointer"
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
          v-for="item in paginatedItems"
          :key="`${item.name}:${item.version}:${item.format}`"
          class="grid grid-cols-[28px_minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_120px] items-center gap-3 px-4 py-2.5 transition-colors group"
          :class="{ 'bg-primary-soft/40': selectedIds.has(itemKey(item)) }"
        >
          <!-- Checkbox -->
          <div>
            <input
              type="checkbox"
              :checked="selectedIds.has(itemKey(item))"
              class="w-4 h-4 rounded border-border text-primary focus:ring-primary cursor-pointer opacity-0 group-hover:opacity-100 transition-opacity"
              :class="{ 'opacity-100': selectedIds.has(itemKey(item)) }"
              :aria-label="`Select ${item.name}`"
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
            <FormatBadge :format="item.format" class="shrink-0" />
            <router-link
              :to="`/repos/${repoName}/packages/${encodeURIComponent(item.format)}/${encodeURIComponent(item.name)}`"
              class="text-sm font-medium text-foreground hover:text-primary transition-colors truncate"
            >{{ item.name }}</router-link>
            <span
              v-if="item.version === 'latest'"
              class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-primary-soft text-primary shrink-0"
            >latest</span>
          </div>

          <!-- Version -->
          <div class="font-mono text-xs truncate">
            {{ item.version || '—' }}
          </div>

          <!-- Size -->
          <div class="font-mono text-xs tabular-nums text-muted text-right">
            {{ formatBytes(item.size) }}
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

      <Pagination
        v-if="filteredItems.length > limit"
        :total="filteredItems.length"
        :page="page"
        :limit="limit"
        @change="page = $event"
      />
    </div>

    <!-- Grid view -->
    <div v-else class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-3 p-4 bg-surface rounded-lg border border-border">
      <div
        v-for="item in paginatedItems"
        :key="`${item.name}:${item.version}:${item.format}`"
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
            class="w-4 h-4 rounded border-border text-primary focus:ring-primary cursor-pointer opacity-0 group-hover:opacity-100 transition-opacity"
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
        <FormatBadge :format="item.format" class="mt-2" />
        <div class="mt-3 flex items-center justify-between text-xs text-muted">
          <span class="font-mono tabular-nums">{{ formatBytes(item.size) }}</span>
          <span>{{ formatDate(item.created_at) }}</span>
        </div>
      </div>
    </div>

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
import { ref, computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import { listComponents, getRepository } from '../api/repositories'
import type { Repository, ComponentItem } from '../api/repositories'
import EmptyState from '../components/EmptyState.vue'
import Pagination from '../components/Pagination.vue'
import BulkActionsBar from '../components/BulkActionsBar.vue'
import FormatBadge from '../components/FormatBadge.vue'
import { useToastStore } from '../stores/toast'
import {
  MagnifyingGlassIcon,
  ArchiveBoxIcon,
  ArrowPathIcon,
  ChevronRightIcon,
  ExclamationTriangleIcon,
  Bars3Icon,
  Squares2X2Icon,
  ArrowsUpDownIcon,
  CubeIcon,
} from '@heroicons/vue/24/outline'
import { t } from '../i18n/en'

const route = useRoute()
const repoName = computed(() => route.params.name as string)
const toasts = useToastStore()

const repo = ref<Repository | null>(null)
const items = ref<ComponentItem[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const searchQuery = ref('')
const page = ref(1)
const limit = 25
const viewMode = ref<'list' | 'grid'>('list')

// ── Selection state ────────────────────────────────────────────────────────
const selectedIds = ref<Set<string>>(new Set())

function itemKey(item: ComponentItem): string {
  return item.id
}

const selectedItemKeys = computed(() => [...selectedIds.value])

const allPageSelected = computed(() =>
  paginatedItems.value.length > 0 &&
  paginatedItems.value.every((i) => selectedIds.value.has(itemKey(i))),
)

const somePageSelected = computed(() =>
  paginatedItems.value.some((i) => selectedIds.value.has(itemKey(i))),
)

function toggleItem(item: ComponentItem): void {
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
    paginatedItems.value.forEach((i) => next.delete(itemKey(i)))
    selectedIds.value = next
  } else {
    const next = new Set(selectedIds.value)
    paginatedItems.value.forEach((i) => next.add(itemKey(i)))
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
    return 'bg-red-500/15 text-red-600 dark:text-red-300'
  }
  if (lower.endsWith('.jar') || lower.endsWith('.war') || lower.endsWith('.ear')) {
    return 'bg-rose-500/15 text-rose-600 dark:text-rose-300'
  }
  if (lower.endsWith('.tgz') || lower.endsWith('.tar.gz') || lower.endsWith('.tar')) {
    return 'bg-blue-500/15 text-blue-600 dark:text-blue-300'
  }
  if (lower.endsWith('.whl') || lower.endsWith('.egg')) {
    return 'bg-sky-500/15 text-sky-600 dark:text-sky-300'
  }
  return 'bg-gray-500/15 text-gray-600 dark:text-gray-300'
}

// ── Bulk action handlers ───────────────────────────────────────────────────
function onDeleted(ids: string[]): void {
  const deleted = new Set(ids)
  items.value = items.value.filter((i) => !deleted.has(itemKey(i)))
  clearSelection()
  toasts.success(t('bulk.deleteSuccess').replace('{n}', String(ids.length)))
}

function onBulkError(msg: string): void {
  toasts.error(t('bulk.deleteFailed'), msg)
}

// ─────────────────────────────────────────────────────────────────────────

watch(searchQuery, () => {
  page.value = 1
})

const filteredItems = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return items.value
  return items.value.filter(
    (i) =>
      i.name.toLowerCase().includes(q) ||
      (i.version ?? '').toLowerCase().includes(q) ||
      i.format.toLowerCase().includes(q),
  )
})

const sortKey = ref<'name' | 'created'>('name')

const sortedItems = computed(() => {
  const list = [...filteredItems.value]
  if (sortKey.value === 'name') {
    list.sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version))
  } else {
    list.sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''))
  }
  return list
})

const paginatedItems = computed(() => {
  const start = (page.value - 1) * limit
  return sortedItems.value.slice(start, start + limit)
})

function formatBytes(bytes?: number): string {
  if (bytes == null || bytes === 0) return '—'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function formatDate(ts?: string): string {
  if (!ts) return '—'
  try {
    return new Date(ts).toLocaleDateString(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    })
  } catch {
    return ts
  }
}

let loadGeneration = 0

async function load(): Promise<void> {
  const generation = ++loadGeneration
  loading.value = true
  error.value = null
  try {
    const [repoData, componentsData] = await Promise.all([
      getRepository(repoName.value),
      listComponents(repoName.value),
    ])
    if (generation !== loadGeneration) return
    repo.value = repoData
    items.value = componentsData.items
    selectedIds.value = new Set()
    page.value = 1
  } catch (err) {
    if (generation !== loadGeneration) return
    error.value = err instanceof Error ? err.message : t('browser.loadFailed')
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

watch(repoName, load, { immediate: true })
</script>

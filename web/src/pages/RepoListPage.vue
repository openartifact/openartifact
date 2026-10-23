<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('repos.title')">
      <template #actions>
        <div class="relative w-full sm:w-auto">
          <MagnifyingGlassIcon class="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted pointer-events-none" aria-hidden="true" />
          <input
            v-model="searchQuery"
            type="search"
            :placeholder="t('repos.search')"
            :aria-label="t('repos.search')"
            class="h-9 w-full sm:w-56 pl-8 pr-2 rounded-lg text-sm bg-surface-hover/60 border border-border-strong text-foreground placeholder:text-muted focus:border-primary"
          />
        </div>
        <AppButton v-if="session.canCreateRepo" @click="showNewModal = true">
          {{ t('repos.new') }}
        </AppButton>
      </template>
    </PageHeader>

    <!-- Format Filter Pills + View Toggle -->
    <div class="flex flex-wrap items-center justify-between gap-4">
      <div class="flex flex-wrap gap-2">
        <button
          :aria-pressed="activeFormat === null"
          :class="[
            'px-3 py-1.5 rounded-full text-xs font-medium border transition-colors cursor-pointer inline-flex items-center gap-1.5',
            activeFormat === null
              ? 'border-primary/40 bg-primary-soft text-primary'
              : 'border-border bg-surface hover:bg-surface-hover text-foreground/75'
          ]"
          @click="activeFormat = null"
        >
          {{ t('repos.filter.all') }}
          <span class="text-micro font-mono tabular-nums">{{ repos.length }}</span>
        </button>

        <button
          v-for="[format, count] of formatCounts"
          :key="format"
          :aria-pressed="activeFormat === format"
          :class="[
            'px-2 py-1 rounded-full border transition-colors cursor-pointer inline-flex items-center gap-1.5',
            activeFormat === format
              ? 'border-primary/40 bg-primary-soft'
              : 'border-border bg-surface hover:bg-surface-hover'
          ]"
          @click="activeFormat = format"
        >
          <FormatBadge :format="format" size="sm" />
          <span class="text-micro font-mono tabular-nums text-foreground/75">{{ count }}</span>
        </button>
      </div>

      <div class="flex items-center gap-1 bg-surface-hover rounded-lg p-0.5">
        <button
          :class="['p-1.5 rounded-md transition-colors', view === 'grid' ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground']"
          :aria-label="t('browser.gridView')"
          :aria-pressed="view === 'grid'"
          :title="t('browser.gridView')"
          @click="view = 'grid'"
        >
          <Squares2X2Icon class="w-4 h-4" aria-hidden="true" />
        </button>
        <button
          :class="['p-1.5 rounded-md transition-colors', view === 'list' ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground']"
          :aria-label="t('browser.listView')"
          :aria-pressed="view === 'list'"
          :title="t('browser.listView')"
          @click="view = 'list'"
        >
          <Bars3Icon class="w-4 h-4" aria-hidden="true" />
        </button>
      </div>
    </div>

    <!-- Loading State -->
    <template v-if="loading">
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 mt-4">
        <div v-for="i in 6" :key="i" class="rounded-xl border border-border bg-surface p-5 animate-pulse">
          <div class="flex items-center gap-2 mb-3">
            <div class="h-4 w-32 bg-surface-hover rounded" />
            <div class="h-5 w-14 bg-surface-hover rounded" />
          </div>
          <div class="h-3 w-full bg-surface-hover rounded mb-1.5" />
          <div class="h-3 w-2/3 bg-surface-hover rounded" />
        </div>
      </div>
    </template>

    <!-- Error State -->
    <div v-else-if="loadError" class="mt-4 rounded-xl border border-danger/30 bg-surface p-8 text-center" role="alert">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="fetchRepos">
        {{ t('browser.retry') }}
      </AppButton>
    </div>

    <!-- Empty State -->
    <template v-else-if="repos.length === 0">
      <EmptyState :title="t('repos.empty')" :description="t('repos.emptyHint')" />
    </template>

    <!-- Grid View -->
    <template v-else-if="view === 'grid'">
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 mt-4">
        <div
          v-for="repo of filteredRepos"
          :key="repo.id"
          class="group relative text-left rounded-xl border bg-surface p-5 transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:shadow-black/5 dark:hover:shadow-black/40"
          :class="selectedIds.has(repo.name) ? 'border-primary bg-primary-soft/20' : 'border-border hover:border-primary/30'"
        >
          <div class="flex items-start justify-between gap-3 mb-3">
            <div class="flex items-center gap-2 flex-1 min-w-0">
              <input
                v-if="session.isAdmin"
                type="checkbox"
                :checked="selectedIds.has(repo.name)"
                :aria-label="`${t('repoDetail.select')} ${repo.name}`"
                class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer shrink-0"
                :class="selectedIds.has(repo.name) ? '' : 'sr-only group-hover:not-sr-only focus:not-sr-only'"
                @change="toggleRepo(repo.name)"
              />
              <router-link
                :to="repoLink(repo.name)"
                class="font-mono text-sm font-semibold flex-1 truncate hover:text-primary transition-colors"
              >{{ repo.name }}</router-link>
            </div>
            <FormatBadge :format="repo.format" />
          </div>

          <div class="flex flex-wrap gap-2 mb-3">
            <span class="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-md bg-surface-hover text-muted">
              {{ repo.repo_type || 'hosted' }}
            </span>
            <span class="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-md bg-surface-hover text-muted">
              <EyeIcon v-if="repo.public" class="w-3 h-3" aria-hidden="true" />
              <EyeSlashIcon v-else class="w-3 h-3" aria-hidden="true" />
              {{ repo.public ? t('repos.public') : t('repos.private') }}
            </span>
            <span
              v-if="repo.federated"
              class="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-md bg-surface-hover text-muted"
              :title="t('federation.badgeTitle')"
            >
              <ArrowsRightLeftIcon class="w-3 h-3" aria-hidden="true" />
              {{ t('federation.badge') }}
            </span>
          </div>

          <p class="text-sm text-muted leading-relaxed line-clamp-2" :class="{ italic: !repo.description }">
            {{ repo.description || t('repos.noDescription') }}
          </p>
        </div>
      </div>
      <p v-if="!filteredRepos.length" class="mt-6 text-center text-sm text-muted">{{ t('repos.noMatch') }}</p>
    </template>

    <!-- List View -->
    <template v-else>
      <div class="bg-surface rounded-xl border border-border overflow-hidden mt-4">
        <div class="grid grid-cols-[28px_2fr_1fr_1fr_1fr] gap-4 bg-surface-hover/50 px-5 py-3 text-xs font-medium text-muted border-b border-border">
          <div>
            <input
              v-if="session.isAdmin"
              type="checkbox"
              :checked="allSelected"
              :indeterminate="someSelected && !allSelected"
              :aria-label="t('repoDetail.selectAll')"
              class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer"
              @change="toggleAll"
            />
          </div>
          <div>{{ t('repos.column.name') }}</div>
          <div>{{ t('repos.column.format') }}</div>
          <div>{{ t('repos.column.type') }}</div>
          <div>{{ t('repos.column.visibility') }}</div>
        </div>

        <div
          v-for="repo of filteredRepos"
          :key="repo.id"
          class="grid grid-cols-[28px_2fr_1fr_1fr_1fr] gap-4 px-5 py-4 border-t border-border hover:bg-surface-hover transition-colors items-center group"
          :class="{ 'bg-primary-soft/20': selectedIds.has(repo.name) }"
        >
          <div>
            <input
              v-if="session.isAdmin"
              type="checkbox"
              :checked="selectedIds.has(repo.name)"
              :aria-label="`${t('repoDetail.select')} ${repo.name}`"
              class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer opacity-0 group-hover:opacity-100 focus:opacity-100 transition-opacity"
              :class="{ 'opacity-100': selectedIds.has(repo.name) }"
              @change="toggleRepo(repo.name)"
            />
          </div>
          <router-link :to="repoLink(repo.name)" class="font-medium text-foreground truncate hover:text-primary">{{ repo.name }}</router-link>
          <div><FormatBadge :format="repo.format" /></div>
          <div class="flex flex-wrap items-center gap-1.5">
            <span class="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-md bg-surface-hover text-muted">
              {{ repo.repo_type || 'hosted' }}
            </span>
            <span
              v-if="repo.federated"
              class="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-md bg-surface-hover text-muted"
              :title="t('federation.badgeTitle')"
            >
              <ArrowsRightLeftIcon class="w-3 h-3" aria-hidden="true" />
              {{ t('federation.badge') }}
            </span>
          </div>
          <div class="text-xs text-muted">{{ repo.public ? t('repos.public') : t('repos.private') }}</div>
        </div>
        <p v-if="!filteredRepos.length" class="px-5 py-6 text-center text-sm text-muted">{{ t('repos.noMatch') }}</p>
      </div>
    </template>

    <NewRepoModal v-model="showNewModal" @created="fetchRepos" />

    <!-- Bulk Actions Bar -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition-all duration-300 ease-out"
        enter-from-class="translate-y-full opacity-0"
        enter-to-class="translate-y-0 opacity-100"
        leave-active-class="transition-all duration-200 ease-in"
        leave-from-class="translate-y-0 opacity-100"
        leave-to-class="translate-y-full opacity-0"
      >
        <div
          v-if="selectedIds.size > 0"
          class="fixed bottom-6 left-1/2 -translate-x-1/2 z-40 w-full max-w-2xl px-4"
        >
          <div class="flex items-center gap-2 px-4 py-3 rounded-xl border border-border bg-surface shadow-2xl shadow-black/20 ring-1 ring-black/5">
            <div class="flex items-center gap-2 shrink-0">
              <span class="inline-flex items-center justify-center w-6 h-6 rounded-full bg-primary text-primary-contrast text-xs font-bold">
                {{ selectedIds.size > 99 ? '99+' : selectedIds.size }}
              </span>
              <span class="text-sm font-medium text-foreground whitespace-nowrap">
                {{ t('repos.selected').replace('{n}', String(selectedIds.size)) }}
              </span>
            </div>

            <div class="w-px h-5 bg-border mx-1 shrink-0" />

            <AppButton variant="danger-outline" size="sm" :disabled="bulkBusy" @click="confirmBulkDelete = true">
              <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('repos.deleteSelected') }}
            </AppButton>

            <button
              class="shrink-0 w-7 h-7 flex items-center justify-center rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors ml-auto"
              :title="t('repos.clearSelection')"
              :aria-label="t('repos.clearSelection')"
              @click="clearSelection"
            >
              <XMarkIcon class="w-4 h-4" aria-hidden="true" />
            </button>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- Delete confirmation -->
    <ConfirmDialog
      :open="confirmBulkDelete"
      :title="t('repos.confirmDeleteTitle').replace('{n}', String(selectedIds.size))"
      :description="t('repos.confirmDeleteBody')"
      :confirm-label="t('common.delete')"
      :busy-label="t('repoDetail.deleting')"
      :busy="bulkBusy"
      @confirm="doBulkDelete"
      @close="closeConfirm"
    >
      <div v-if="bulkBusy" class="flex items-center gap-2">
        <div class="flex-1 h-1.5 bg-border rounded-full overflow-hidden">
          <div class="h-full bg-primary rounded-full transition-all duration-300" :style="{ width: `${bulkProgress}%` }" />
        </div>
        <span class="text-xs text-muted tabular-nums">{{ bulkProgress }}%</span>
      </div>
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../components/PageHeader.vue'
import { ref, computed, onMounted } from 'vue'
import {
  ArrowsRightLeftIcon, Squares2X2Icon, Bars3Icon, EyeIcon, EyeSlashIcon,
  MagnifyingGlassIcon, TrashIcon, XMarkIcon,
} from '@heroicons/vue/24/outline'
import { listAllRepositories, deleteRepository } from '../api/repositories'
import type { Repository } from '../api/repositories'
import AppButton from '../components/AppButton.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import FormatBadge from '../components/FormatBadge.vue'
import EmptyState from '../components/EmptyState.vue'
import NewRepoModal from '../components/NewRepoModal.vue'
import { useToastStore } from '../stores/toast'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n/en'

const toasts = useToastStore()
const session = useSessionStore()
const repos = ref<Repository[]>([])
const loading = ref(true)
const loadError = ref('')
const view = ref<'grid' | 'list'>('grid')
const activeFormat = ref<string | null>(null)
const showNewModal = ref(false)
const searchQuery = ref('')

function repoLink(name: string): string {
  return `/repos/${encodeURIComponent(name)}`
}

// ── Selection ──────────────────────────────────────────────────────────────
const selectedIds = ref<Set<string>>(new Set())

const allSelected = computed(() =>
  filteredRepos.value.length > 0 && filteredRepos.value.every((r) => selectedIds.value.has(r.name)),
)
const someSelected = computed(() => filteredRepos.value.some((r) => selectedIds.value.has(r.name)))

function toggleRepo(name: string): void {
  const next = new Set(selectedIds.value)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  selectedIds.value = next
}

function toggleAll(): void {
  const next = new Set(selectedIds.value)
  if (allSelected.value) filteredRepos.value.forEach((r) => next.delete(r.name))
  else filteredRepos.value.forEach((r) => next.add(r.name))
  selectedIds.value = next
}

function clearSelection(): void {
  selectedIds.value = new Set()
}

// ── Bulk delete ────────────────────────────────────────────────────────────
const confirmBulkDelete = ref(false)
const bulkBusy = ref(false)
const bulkProgress = ref(0)

function closeConfirm(): void {
  if (!bulkBusy.value) confirmBulkDelete.value = false
}

async function doBulkDelete(): Promise<void> {
  bulkBusy.value = true
  bulkProgress.value = 0
  const toDelete = [...selectedIds.value]
  let succeeded = 0
  try {
    for (let i = 0; i < toDelete.length; i++) {
      try {
        await deleteRepository(toDelete[i])
        succeeded++
      } catch {
        // counted as failed below
      }
      bulkProgress.value = Math.round(((i + 1) / toDelete.length) * 100)
    }
    await fetchRepos()
  } finally {
    // Always release the modal, even when the refetch fails.
    clearSelection()
    confirmBulkDelete.value = false
    bulkBusy.value = false
    bulkProgress.value = 0
  }

  if (succeeded > 0) toasts.success(t('repos.deletedCount').replace('{n}', String(succeeded)))
  if (succeeded < toDelete.length) toasts.error(t('repos.deleteSomeFailed'))
}

const formatCounts = computed(() => {
  const counts = new Map<string, number>()
  repos.value.forEach((r) => counts.set(r.format, (counts.get(r.format) || 0) + 1))
  return counts
})

const filteredRepos = computed(() => {
  let result = repos.value
  if (activeFormat.value) result = result.filter((r) => r.format === activeFormat.value)
  const q = searchQuery.value.trim().toLowerCase()
  if (q) result = result.filter((r) => r.name.toLowerCase().includes(q) || (r.description ?? '').toLowerCase().includes(q))
  return result
})

let fetchGeneration = 0

async function fetchRepos(): Promise<void> {
  const generation = ++fetchGeneration
  loading.value = true
  loadError.value = ''
  try {
    const all = await listAllRepositories()
    if (generation !== fetchGeneration) return
    repos.value = all
    if (activeFormat.value && !all.some((r) => r.format === activeFormat.value)) activeFormat.value = null
  } catch (err) {
    if (generation !== fetchGeneration) return
    loadError.value = err instanceof Error ? err.message : t('repos.loadFailed')
  } finally {
    if (generation === fetchGeneration) loading.value = false
  }
}

onMounted(fetchRepos)
</script>

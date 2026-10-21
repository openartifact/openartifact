<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Loading skeleton -->
    <div v-if="loading" class="bg-surface rounded-xl border border-border p-5 space-y-4">
      <div class="h-4 w-40 rounded skel" />
      <div class="h-3 w-full rounded skel" />
      <div class="h-8 w-full rounded skel" />
    </div>

    <!-- No quota configured -->
    <div
      v-else-if="!quota"
      class="bg-surface rounded-xl border border-border p-8 text-center"
    >
      <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
        <CircleStackIcon class="w-6 h-6 text-muted" />
      </div>
      <p class="text-sm font-semibold text-foreground">{{ t('quota.none') }}</p>
      <p class="text-xs text-muted mt-1 mb-4 max-w-xs mx-auto">
        This repository has no storage limit. Set a quota to control how much space it can use.
      </p>
      <button
        type="button"
        class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
        @click="showForm = true"
      >
        <PlusIcon class="w-4 h-4" />
        {{ t('quota.set') }}
      </button>
    </div>

    <!-- Quota display -->
    <template v-else>
      <!-- Usage card -->
      <div class="bg-surface rounded-xl border border-border p-5">
        <div class="flex items-start justify-between gap-3 mb-4">
          <div>
            <p class="text-[10px] font-mono font-semibold text-muted">{{ t('quota.usage') }}</p>
            <p class="mt-1 text-xl font-bold tabular-nums text-foreground">
              {{ formatBytes(quota.current_size_bytes) }}
              <span class="text-sm font-normal text-muted">/ {{ formatBytes(quota.max_size_bytes) }}</span>
            </p>
          </div>
          <div class="flex items-center gap-1.5 shrink-0">
            <span
              class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] font-medium"
              :class="quota.is_exceeded ? 'bg-danger-soft text-danger' : 'bg-success-soft text-success'"
            >
              <span class="w-1.5 h-1.5 rounded-full" :class="quota.is_exceeded ? 'bg-danger' : 'bg-success'" />
              {{ quota.is_exceeded ? t('quota.exceeded') : t('quota.within') }}
            </span>
          </div>
        </div>

        <!-- Progress bar -->
        <div class="relative h-2.5 rounded-full bg-surface-hover overflow-hidden">
          <div
            class="absolute left-0 top-0 bottom-0 rounded-full transition-all duration-500"
            :class="progressBarColor"
            :style="{ width: `${Math.min(usagePercent, 100)}%` }"
          />
        </div>
        <div class="flex items-center justify-between mt-1.5">
          <span class="text-[11px] text-muted font-mono tabular-nums">
            {{ usagePercent.toFixed(1) }}% used
          </span>
          <span class="text-[11px] text-muted font-mono tabular-nums">
            {{ formatBytes(quota.max_size_bytes - quota.current_size_bytes) }} free
          </span>
        </div>
      </div>

      <!-- Edit form (toggle) -->
      <div class="bg-surface rounded-xl border border-border overflow-hidden">
        <button
          type="button"
          class="w-full flex items-center justify-between px-5 py-4 text-left hover:bg-surface-hover/40 transition-colors"
          @click="showForm = !showForm"
        >
          <span class="text-sm font-semibold text-foreground">{{ t('quota.edit') }}</span>
          <ChevronDownIcon
            class="w-4 h-4 text-muted transition-transform duration-200"
            :class="showForm ? 'rotate-180' : ''"
          />
        </button>

        <div v-if="showForm" class="px-5 pb-5 space-y-4 border-t border-border pt-4">
          <!-- Max size input -->
          <div>
            <label class="block text-xs font-semibold text-foreground mb-1.5">
              Maximum size <span class="text-danger ml-0.5">*</span>
            </label>
            <div class="flex items-center gap-2">
              <input
                v-model.number="formSize"
                type="number"
                min="1"
                step="1"
                :placeholder="t('quota.placeholder')"
                class="flex-1 px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
              />
              <div class="inline-flex p-0.5 rounded-lg bg-surface-hover border border-border shrink-0">
                <button
                  v-for="unit in SIZE_UNITS"
                  :key="unit.label"
                  type="button"
                  class="px-2.5 py-1 rounded-md text-xs font-medium transition-all"
                  :class="
                    formUnit === unit.label
                      ? 'bg-surface text-foreground shadow-sm'
                      : 'text-muted hover:text-foreground'
                  "
                  @click="formUnit = unit.label"
                >
                  {{ unit.label }}
                </button>
              </div>
            </div>
          </div>


          <!-- Actions -->
          <div class="flex items-center gap-2">
            <button
              type="button"
              :disabled="saving || !formSize"
              class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed"
              @click="saveQuota"
            >
              <ArrowPathIcon v-if="saving" class="w-4 h-4 animate-spin" />
              {{ t('common.save') }}
            </button>
            <button
              type="button"
              class="px-3 py-1.5 rounded-lg text-sm font-medium text-danger hover:bg-danger/10 transition-colors"
              @click="openRemove"
            >
              <TrashIcon class="w-4 h-4 inline mr-1" />
              {{ t('quota.remove') }}
            </button>
          </div>
        </div>
      </div>
    </template>

    <!-- Set quota form (when no quota) -->
    <div v-if="!quota && showForm" class="bg-surface rounded-xl border border-border p-5 space-y-4">
      <h3 class="text-sm font-semibold text-foreground">{{ t('quota.configure') }}</h3>

      <!-- Max size input -->
      <div>
        <label class="block text-xs font-semibold text-foreground mb-1.5">
          Maximum size <span class="text-danger ml-0.5">*</span>
        </label>
        <div class="flex items-center gap-2">
          <input
            v-model.number="formSize"
            type="number"
            min="1"
            step="1"
            :placeholder="t('quota.placeholder')"
            class="flex-1 px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
          />
          <div class="inline-flex p-0.5 rounded-lg bg-surface-hover border border-border shrink-0">
            <button
              v-for="unit in SIZE_UNITS"
              :key="unit.label"
              type="button"
              class="px-2.5 py-1 rounded-md text-xs font-medium transition-all"
              :class="
                formUnit === unit.label
                  ? 'bg-surface text-foreground shadow-sm'
                  : 'text-muted hover:text-foreground'
              "
              @click="formUnit = unit.label"
            >
              {{ unit.label }}
            </button>
          </div>
        </div>
      </div>


      <!-- Actions -->
      <div class="flex items-center gap-2">
        <button
          type="button"
          :disabled="saving || !formSize"
          class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed"
          @click="saveQuota"
        >
          <ArrowPathIcon v-if="saving" class="w-4 h-4 animate-spin" />
          {{ t('quota.set') }}
        </button>
        <button
          type="button"
          class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
          @click="showForm = false"
        >
          {{ t('common.cancel') }}
        </button>
      </div>
    </div>

    <!-- Remove confirmation modal -->
    <Teleport to="body">
      <div
        v-if="removeModal"
        class="fixed inset-0 z-50 flex items-center justify-center p-4 animate-fade-in"
      >
        <div class="absolute inset-0 bg-black/50" @click="removeModal = false" />
        <div class="relative bg-surface border border-border rounded-xl shadow-2xl w-full max-w-sm p-6">
          <h2 class="text-base font-bold text-foreground tracking-tight">Remove quota limit?</h2>
          <p class="text-sm text-muted mt-1 mb-5">
            The repository will have no storage limit. This can be configured again at any time.
          </p>
          <div class="flex items-center gap-2 justify-end">
            <button
              type="button"
              class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
              @click="removeModal = false"
            >
              {{ t('common.cancel') }}
            </button>
            <button
              type="button"
              :disabled="removing"
              class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-danger text-white hover:opacity-90 transition-opacity disabled:opacity-40 disabled:cursor-not-allowed"
              @click="removeQuota"
            >
              <ArrowPathIcon v-if="removing" class="w-4 h-4 animate-spin" />
              <TrashIcon v-else class="w-4 h-4" />
              {{ t('quota.remove') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { request, ApiError } from '../api/client'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'
import {
  CircleStackIcon,
  PlusIcon,
  ChevronDownIcon,
  ArrowPathIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'

// ─── Props ─────────────────────────────────────────────────────────────────────

const props = defineProps<{ repoName: string }>()

// ─── Types ─────────────────────────────────────────────────────────────────────

interface Quota {
  id?: string
  repo_id?: string
  max_size_bytes: number
  current_size_bytes: number
  is_exceeded?: boolean
  created_at?: string
  updated_at?: string
}

// ─── Constants ─────────────────────────────────────────────────────────────────

const SIZE_UNITS = [
  { label: 'MB', bytes: 1024 * 1024 },
  { label: 'GB', bytes: 1024 * 1024 * 1024 },
  { label: 'TB', bytes: 1024 * 1024 * 1024 * 1024 },
]

// ─── State ─────────────────────────────────────────────────────────────────────

const toasts = useToastStore()
const quota = ref<Quota | null>(null)
const loading = ref(true)
const saving = ref(false)
const removing = ref(false)
const showForm = ref(false)
const removeModal = ref(false)

const formSize = ref<number>(10)
const formUnit = ref<string>('GB')

// ─── Computed ──────────────────────────────────────────────────────────────────

const usagePercent = computed(() => {
  if (!quota.value || quota.value.max_size_bytes === 0) return 0
  return (quota.value.current_size_bytes / quota.value.max_size_bytes) * 100
})

const progressBarColor = computed(() => {
  const pct = usagePercent.value
  if (pct >= 90) return 'bg-danger'
  if (pct >= 70) return 'bg-warning'
  return 'bg-success'
})

// ─── Helpers ───────────────────────────────────────────────────────────────────

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024 * 1024 * 1024)).toFixed(1)} TB`
  if (bytes >= 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${bytes} B`
}

function bytesFromForm(): number {
  const unit = SIZE_UNITS.find((u) => u.label === formUnit.value)
  return (formSize.value || 0) * (unit?.bytes ?? 1024 * 1024 * 1024)
}

function populateForm(q: Quota): void {
  const bytes = q.max_size_bytes
  if (bytes >= 1024 * 1024 * 1024 * 1024) {
    formSize.value = parseFloat((bytes / (1024 * 1024 * 1024 * 1024)).toFixed(2))
    formUnit.value = 'TB'
  } else if (bytes >= 1024 * 1024 * 1024) {
    formSize.value = parseFloat((bytes / (1024 * 1024 * 1024)).toFixed(2))
    formUnit.value = 'GB'
  } else {
    formSize.value = parseFloat((bytes / (1024 * 1024)).toFixed(2))
    formUnit.value = 'MB'
  }
}

// ─── API ───────────────────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await request<Quota>(
      `/api/v1/repositories/${encodeURIComponent(props.repoName)}/quota`,
    )
    // The backend always answers 200; max_size_bytes === 0 means "unlimited".
    if (res && res.max_size_bytes > 0) {
      quota.value = res
      populateForm(res)
    } else {
      quota.value = null
    }
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 204)) {
      quota.value = null
    } else {
      toasts.error(t('quota.loadFailed'))
    }
  } finally {
    loading.value = false
  }
}

async function saveQuota(): Promise<void> {
  if (!formSize.value) return
  saving.value = true
  try {
    const res = await request<Quota>(
      `/api/v1/repositories/${encodeURIComponent(props.repoName)}/quota`,
      {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          max_size_bytes: bytesFromForm(),
        }),
      },
    )
    quota.value = res && res.max_size_bytes > 0 ? res : null
    showForm.value = false
    toasts.success(t('quota.saved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('quota.saveFailed'))
  } finally {
    saving.value = false
  }
}

function openRemove(): void {
  removeModal.value = true
}

async function removeQuota(): Promise<void> {
  removing.value = true
  try {
    await request<void>(
      `/api/v1/repositories/${encodeURIComponent(props.repoName)}/quota`,
      { method: 'DELETE' },
    )
    quota.value = null
    removeModal.value = false
    showForm.value = false
    toasts.success(t('quota.removed'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('quota.removeFailed'))
  } finally {
    removing.value = false
  }
}

// ─── Lifecycle ─────────────────────────────────────────────────────────────────

onMounted(load)
</script>

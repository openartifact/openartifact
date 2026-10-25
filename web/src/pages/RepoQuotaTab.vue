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
      v-else-if="!configured"
      class="bg-surface rounded-xl border border-border p-8 text-center"
    >
      <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
        <CircleStackIcon class="w-6 h-6 text-muted" aria-hidden="true" />
      </div>
      <p class="text-sm font-semibold text-foreground">{{ t('quota.none') }}</p>
      <p class="text-xs text-muted mt-1 mb-4 max-w-xs mx-auto">
        {{ t('quota.noneHint') }}
      </p>
      <AppButton @click="showForm = true">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('quota.set') }}
      </AppButton>
    </div>

    <!-- Quota display -->
    <template v-else-if="quota">
      <!-- Usage card -->
      <div class="bg-surface rounded-xl border border-border p-5 space-y-5">
        <div class="flex items-start justify-between gap-3">
          <p class="text-micro font-mono font-semibold text-muted">{{ t('quota.usage') }}</p>
          <span
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium shrink-0"
            :class="quota.is_exceeded ? 'bg-danger-soft text-danger' : 'bg-success-soft text-success'"
          >
            <span class="w-1.5 h-1.5 rounded-full" :class="quota.is_exceeded ? 'bg-danger' : 'bg-success'" />
            {{ quota.is_exceeded ? t('quota.exceeded') : t('quota.within') }}
          </span>
        </div>

        <!-- Size limit -->
        <div>
          <p class="text-xl font-bold tabular-nums text-foreground">
            {{ formatBytes(quota.current_size_bytes) }}
            <span class="text-sm font-normal text-muted">
              / {{ sizeLimited ? formatBytes(quota.max_size_bytes) : t('quota.unlimited') }}
            </span>
          </p>
          <template v-if="sizeLimited">
            <div
              class="relative mt-3 h-2.5 rounded-full bg-surface-hover overflow-hidden"
              role="progressbar"
              :aria-label="t('quota.usage')"
              aria-valuemin="0"
              aria-valuemax="100"
              :aria-valuenow="Math.round(Math.min(usagePercent, 100))"
            >
              <div
                class="absolute left-0 top-0 bottom-0 rounded-full transition-all duration-500"
                :class="barColor(usagePercent)"
                :style="{ width: `${Math.min(usagePercent, 100)}%` }"
              />
            </div>
            <div class="flex items-center justify-between mt-1.5">
              <span class="text-caption text-muted font-mono tabular-nums">
                {{ t('quota.percentUsed').replace('{n}', usagePercent.toFixed(1)) }}
              </span>
              <span class="text-caption text-muted font-mono tabular-nums">
                {{ t('quota.free').replace('{size}', formatBytes(Math.max(quota.max_size_bytes - quota.current_size_bytes, 0))) }}
              </span>
            </div>
          </template>
        </div>

        <!-- Artifact count limit -->
        <div v-if="countLimited">
          <p class="text-micro font-mono font-semibold text-muted">{{ t('quota.countUsage') }}</p>
          <p class="mt-1 text-xl font-bold tabular-nums text-foreground">
            {{ quota.current_artifact_count ?? 0 }}
            <span class="text-sm font-normal text-muted">/ {{ quota.max_artifact_count }}</span>
          </p>
          <div
            class="relative mt-3 h-2.5 rounded-full bg-surface-hover overflow-hidden"
            role="progressbar"
            :aria-label="t('quota.countUsage')"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="Math.round(Math.min(countPercent, 100))"
          >
            <div
              class="absolute left-0 top-0 bottom-0 rounded-full transition-all duration-500"
              :class="barColor(countPercent)"
              :style="{ width: `${Math.min(countPercent, 100)}%` }"
            />
          </div>
          <div class="mt-1.5">
            <span class="text-caption text-muted font-mono tabular-nums">
              {{ t('quota.percentUsed').replace('{n}', countPercent.toFixed(1)) }}
            </span>
          </div>
        </div>
      </div>

      <!-- Edit form (toggle) -->
      <div class="bg-surface rounded-xl border border-border overflow-hidden">
        <button
          type="button"
          :aria-expanded="showForm"
          class="w-full flex items-center justify-between px-5 py-4 text-left hover:bg-surface-hover/40 transition-colors"
          @click="showForm = !showForm"
        >
          <span class="text-sm font-semibold text-foreground">{{ t('quota.edit') }}</span>
          <ChevronDownIcon
            class="w-4 h-4 text-muted transition-transform duration-200"
            :class="showForm ? 'rotate-180' : ''"
            aria-hidden="true"
          />
        </button>

        <div v-if="showForm" class="px-5 pb-5 space-y-4 border-t border-border pt-4">
          <div>
            <label for="quota-max-size" class="block text-xs font-semibold text-foreground mb-1.5">
              {{ t('quota.maxSize') }} <span class="text-danger ml-0.5">*</span>
            </label>
            <div class="flex flex-wrap items-center gap-2">
              <input
                id="quota-max-size"
                v-model.number="formSize"
                type="number"
                min="1"
                step="1"
                :placeholder="t('quota.placeholder')"
                class="flex-1 min-w-32 h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
              />
              <div class="inline-flex p-0.5 rounded-lg bg-surface-hover border border-border shrink-0">
                <button
                  v-for="unit in SIZE_UNITS"
                  :key="unit.label"
                  type="button"
                  :aria-pressed="formUnit === unit.label"
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

          <div class="flex flex-wrap items-center gap-2">
            <AppButton :disabled="!formSize" :loading="saving" @click="saveQuota">
              {{ t('common.save') }}
            </AppButton>
            <AppButton variant="danger-outline" @click="removeModal = true">
              <TrashIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('quota.remove') }}
            </AppButton>
          </div>
        </div>
      </div>
    </template>

    <!-- Set quota form (when no quota) -->
    <div v-if="!configured && !loading && showForm" class="bg-surface rounded-xl border border-border p-5 space-y-4">
      <h3 class="text-sm font-semibold text-foreground">{{ t('quota.configure') }}</h3>

      <div>
        <label for="quota-max-size" class="block text-xs font-semibold text-foreground mb-1.5">
          {{ t('quota.maxSize') }} <span class="text-danger ml-0.5">*</span>
        </label>
        <div class="flex flex-wrap items-center gap-2">
          <input
            id="quota-max-size"
            v-model.number="formSize"
            type="number"
            min="1"
            step="1"
            :placeholder="t('quota.placeholder')"
            class="flex-1 min-w-32 h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
          />
          <div class="inline-flex p-0.5 rounded-lg bg-surface-hover border border-border shrink-0">
            <button
              v-for="unit in SIZE_UNITS"
              :key="unit.label"
              type="button"
              :aria-pressed="formUnit === unit.label"
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

      <div class="flex flex-wrap items-center gap-2">
        <AppButton :disabled="!formSize" :loading="saving" @click="saveQuota">
          {{ t('quota.set') }}
        </AppButton>
        <AppButton variant="ghost" @click="showForm = false">
          {{ t('common.cancel') }}
        </AppButton>
      </div>
    </div>

    <ConfirmDialog
      :open="removeModal"
      :title="t('quota.removeTitle')"
      :description="t('quota.removeBody')"
      :confirm-label="t('quota.remove')"
      :busy="removing"
      @confirm="removeQuota"
      @close="removeModal = false"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { request, ApiError, JSON_HEADERS } from '../api/client'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'
import {
  CircleStackIcon,
  PlusIcon,
  ChevronDownIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { formatBytes } from '../lib/quota'

// ─── Props ─────────────────────────────────────────────────────────────────────

const props = defineProps<{ repoName: string }>()

// ─── Types ─────────────────────────────────────────────────────────────────────

interface Quota {
  id?: string
  repo_id?: string
  max_size_bytes: number
  max_artifact_count?: number
  current_size_bytes: number
  current_artifact_count?: number
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

const sizeLimited = computed(() => (quota.value?.max_size_bytes ?? 0) > 0)
const countLimited = computed(() => (quota.value?.max_artifact_count ?? 0) > 0)
// The backend always answers 200; a limit of 0 means "unlimited", so a quota
// is configured when either limit is set.
const configured = computed(() => sizeLimited.value || countLimited.value)

const usagePercent = computed(() => {
  if (!quota.value || !sizeLimited.value) return 0
  return (quota.value.current_size_bytes / quota.value.max_size_bytes) * 100
})

const countPercent = computed(() => {
  if (!quota.value || !countLimited.value) return 0
  return ((quota.value.current_artifact_count ?? 0) / (quota.value.max_artifact_count ?? 1)) * 100
})

function barColor(pct: number): string {
  if (pct >= 90) return 'bg-danger'
  if (pct >= 70) return 'bg-warning'
  return 'bg-success'
}

// ─── Helpers ───────────────────────────────────────────────────────────────────


function bytesFromForm(): number {
  const unit = SIZE_UNITS.find((u) => u.label === formUnit.value)
  return (formSize.value || 0) * (unit?.bytes ?? 1024 * 1024 * 1024)
}

function populateForm(q: Quota): void {
  const bytes = q.max_size_bytes
  if (bytes <= 0) return
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
    quota.value = res ?? null
    if (res) populateForm(res)
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
        headers: JSON_HEADERS,
        body: JSON.stringify({
          max_size_bytes: bytesFromForm(),
        }),
      },
    )
    quota.value = res ?? null
    showForm.value = false
    toasts.success(t('quota.saved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('quota.saveFailed'))
  } finally {
    saving.value = false
  }
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

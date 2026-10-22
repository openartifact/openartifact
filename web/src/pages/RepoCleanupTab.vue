<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">

    <!-- ─── Header row ──────────────────────────────────────────────────────── -->
    <div class="flex items-center justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-foreground">{{ t('cleanup.title') }}</h2>
        <p class="text-sm text-muted mt-0.5">{{ t('cleanup.subtitle') }}</p>
      </div>
      <AppButton size="sm" class="shrink-0" @click="openDrawer('create')">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('cleanup.add') }}
      </AppButton>
    </div>

    <!-- ─── Loading skeleton ────────────────────────────────────────────────── -->
    <div v-if="loading" class="bg-surface rounded-xl border border-border overflow-hidden">
      <div v-for="i in 3" :key="i" class="flex items-center gap-4 px-5 py-4 border-b border-border last:border-0">
        <div class="h-4 w-36 rounded skel" />
        <div class="h-5 w-24 rounded skel" />
        <div class="h-4 w-28 rounded skel" />
        <div class="flex-1" />
        <div class="w-8 h-5 rounded-full skel" />
        <div class="flex gap-1">
          <div class="w-7 h-7 rounded-md skel" />
          <div class="w-7 h-7 rounded-md skel" />
          <div class="w-7 h-7 rounded-md skel" />
        </div>
      </div>
    </div>

    <!-- ─── Table ───────────────────────────────────────────────────────────── -->
    <div v-else class="bg-surface rounded-xl border border-border overflow-hidden">
      <!-- Empty state -->
      <div v-if="items.length === 0" class="py-16 text-center">
        <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
          <TrashIcon class="w-6 h-6 text-muted" aria-hidden="true" />
        </div>
        <p class="text-sm font-medium text-foreground">{{ t('cleanup.empty') }}</p>
        <p class="text-xs text-muted mt-1 mb-4">{{ t('cleanup.emptyHint') }}</p>
        <AppButton size="sm" @click="openDrawer('create')">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('cleanup.add') }}
        </AppButton>
      </div>

      <!-- Data table -->
      <table v-else class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th class="text-left text-caption font-semibold text-muted px-5 py-2.5">
              {{ t('cleanup.col.name') }}
            </th>
            <th class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.type') }}
            </th>
            <th class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.rule') }}
            </th>
            <th class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.enabled') }}
            </th>
            <th class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.lastRun') }}
            </th>
            <th class="text-right text-caption font-semibold text-muted px-5 py-2.5">
              {{ t('cleanup.col.actions') }}
            </th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr
            v-for="policy in items"
            :key="policy.id"
            class="hover:bg-surface-hover/30 transition-colors"
          >
            <!-- Name -->
            <td class="px-5 py-3.5">
              <span class="font-medium text-sm text-foreground">{{ policy.name }}</span>
            </td>

            <!-- Type chip -->
            <td class="px-3 py-3.5">
              <span
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border border-border bg-surface-hover text-caption font-mono font-medium"
                :class="policy.type === 'keep-last-n' ? 'text-primary' : 'text-warning'"
              >
                <component :is="policy.type === 'keep-last-n' ? QueueListIcon : CalendarIcon" class="w-3.5 h-3.5" />
                {{ policy.type === 'keep-last-n' ? 'keep-last-n' : 'keep-by-age' }}
              </span>
            </td>

            <!-- Rule description -->
            <td class="px-3 py-3.5 text-sm text-muted">
              <span v-if="policy.keep_count != null">
                {{ t('cleanup.keepLast') }} <span class="font-mono font-semibold text-foreground">{{ policy.keep_count }}</span>
              </span>
              <span v-if="policy.keep_count != null && policy.keep_days != null"> · </span>
              <span v-if="policy.keep_days != null">
                {{ t('cleanup.keepFor') }} <span class="font-mono font-semibold text-foreground">{{ policy.keep_days }}</span> {{ t('cleanup.days') }}
              </span>
              <span v-if="policy.keep_count == null && policy.keep_days == null">—</span>
            </td>

            <!-- Enabled toggle -->
            <td class="px-3 py-3.5">
              <AppToggle
                size="sm"
                :model-value="policy.enabled"
                :label="t('cleanup.enable')"
                :disabled="togglingId === policy.id"
                @update:model-value="toggleEnabled(policy)"
              />
            </td>

            <!-- Last run -->
            <td class="px-3 py-3.5">
              <span v-if="policy.last_run_at" class="text-sm text-muted font-mono">
                {{ formatDateTime(policy.last_run_at) }}
              </span>
              <span v-else class="text-sm text-muted">{{ t('cleanup.never') }}</span>
            </td>

            <!-- Actions -->
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-0.5 justify-end">
                <!-- Run now -->
                <button
                  type="button"
                  :title="t('cleanup.run')" :aria-label="t('cleanup.run')"
                  :disabled="runningId === policy.id"
                  class="p-1.5 rounded-md text-muted hover:text-success hover:bg-success/10 transition-colors disabled:opacity-40"
                  @click="runPolicy(policy)"
                >
                  <ArrowPathIcon v-if="runningId === policy.id" class="w-4 h-4 animate-spin" />
                  <PlayIcon v-else class="w-4 h-4" />
                </button>
                <!-- Edit -->
                <button
                  type="button"
                  :title="t('cleanup.editTitle')" :aria-label="t('cleanup.editTitle')"
                  class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  @click="openDrawer('edit', policy)"
                >
                  <PencilIcon class="w-4 h-4" />
                </button>
                <!-- Delete -->
                <button
                  type="button"
                  :title="t('cleanup.deleteTitle')" :aria-label="t('cleanup.deleteTitle')"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
                  @click="openDeleteModal(policy)"
                >
                  <TrashIcon class="w-4 h-4" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- ═══════════════════════════════════════════════════════════════════════
         SLIDE-RIGHT DRAWER
    ════════════════════════════════════════════════════════════════════════════ -->
    <AdminDialog
      drawer
      max-width="max-w-md"
      :open="drawerOpen"
      :eyebrow="drawerMode === 'create' ? t('cleanup.newLabel') : t('cleanup.editTitle')"
      :title="drawerMode === 'create' ? t('cleanup.addTitle') : `${t('cleanup.editTitle')} · ${editingPolicy?.name}`"
      @close="closeDrawer"
    >
      <CleanupPolicyForm v-model="form" />
      <template #footer>
        <AppButton variant="ghost" @click="closeDrawer">{{ t('common.cancel') }}</AppButton>
        <AppButton :disabled="!formValid" :loading="savingDrawer" @click="saveDrawer">
          {{ drawerMode === 'create' ? t('cleanup.create') : t('common.save') }}
        </AppButton>
      </template>
    </AdminDialog>

    <!-- ═══════════════════════════════════════════════════════════════════════
         DELETE CONFIRMATION MODAL
    ════════════════════════════════════════════════════════════════════════════ -->
    <ConfirmDialog
      :open="!!deleteTarget"
      :title="t('cleanup.deleteTitle')"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="cancelDelete"
    >
      <p class="text-sm text-muted">
        <span class="font-mono font-semibold text-foreground">{{ deleteTarget?.name }}</span>
        {{ t('cleanup.deleteBody') }}
      </p>
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, reactive } from 'vue'
import AdminDialog from '../components/AdminDialog.vue'
import AppButton from '../components/AppButton.vue'
import AppToggle from '../components/AppToggle.vue'
import CleanupPolicyForm from '../components/CleanupPolicyForm.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { formatDateTime } from '../lib/datetime'
import {
  cleanupFormFrom,
  cleanupFormValid,
  cleanupPayload,
  cleanupPolicyFromApi,
  emptyCleanupForm,
  type ApiCleanupPolicy,
  type CleanupPolicyView as CleanupPolicy,
} from '../lib/cleanupPolicy'
import { request } from '../api/client'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'
import {
  PlusIcon,
  PencilIcon,
  TrashIcon,
  ArrowPathIcon,
  PlayIcon,
  QueueListIcon,
  CalendarIcon,
} from '@heroicons/vue/24/outline'

const props = defineProps<{
  repoName: string
}>()

const toasts = useToastStore()

const items = ref<CleanupPolicy[]>([])
const loading = ref(true)

// Running
const runningId = ref<string | null>(null)
const togglingId = ref<string | null>(null)

// Drawer
const drawerOpen = ref(false)
const drawerMode = ref<'create' | 'edit'>('create')
const editingPolicy = ref<CleanupPolicy | null>(null)
const savingDrawer = ref(false)

const form = reactive(emptyCleanupForm())

// Delete
const deleteTarget = ref<CleanupPolicy | null>(null)
const deleting = ref(false)

const formValid = computed(() => cleanupFormValid(form))

// ─── Base URL helper ───────────────────────────────────────────────────────────

function baseUrl(): string {
  return `/api/v1/repositories/${encodeURIComponent(props.repoName)}/cleanup-policies`
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

// ─── Data Loading ──────────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await request<{ policies: ApiCleanupPolicy[] }>(baseUrl())
    items.value = (res?.policies ?? []).map(cleanupPolicyFromApi)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.loadFailed'))
  } finally {
    loading.value = false
  }
}

// ─── Toggle Enabled ────────────────────────────────────────────────────────────

async function toggleEnabled(policy: CleanupPolicy): Promise<void> {
  togglingId.value = policy.id
  try {
    await request(`${baseUrl()}/${encodeURIComponent(policy.id)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: !policy.enabled }),
    })
    policy.enabled = !policy.enabled
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.updateFailed'))
  } finally {
    togglingId.value = null
  }
}

// ─── Run Now ───────────────────────────────────────────────────────────────────

async function runPolicy(policy: CleanupPolicy): Promise<void> {
  runningId.value = policy.id
  try {
    await request(`${baseUrl()}/${encodeURIComponent(policy.id)}/run`, { method: 'POST' })
    toasts.success(t('cleanup.runTriggered').replace('{name}', policy.name))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.runFailed'))
  } finally {
    runningId.value = null
  }
}

// ─── Drawer Actions ────────────────────────────────────────────────────────────



function openDrawer(mode: 'create' | 'edit', policy?: CleanupPolicy): void {
  drawerMode.value = mode
  if (mode === 'edit' && policy) {
    editingPolicy.value = policy
    Object.assign(form, cleanupFormFrom(policy))
  } else {
    Object.assign(form, emptyCleanupForm())
    editingPolicy.value = null
  }
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
  editingPolicy.value = null
}

async function saveDrawer(): Promise<void> {
  if (!formValid.value) return
  savingDrawer.value = true
  try {
    const payload = cleanupPayload(form)

    if (drawerMode.value === 'create') {
      await request(baseUrl(), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      toasts.success(t('cleanup.created').replace('{name}', form.name))
    } else if (editingPolicy.value) {
      await request(`${baseUrl()}/${encodeURIComponent(editingPolicy.value.id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      toasts.success(t('cleanup.updated').replace('{name}', form.name))
    }

    closeDrawer()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.saveFailed'))
  } finally {
    savingDrawer.value = false
  }
}

// ─── Delete Actions ────────────────────────────────────────────────────────────

function openDeleteModal(policy: CleanupPolicy): void {
  deleteTarget.value = policy
}

function cancelDelete(): void {
  deleteTarget.value = null
}

async function confirmDelete(): Promise<void> {
  if (!deleteTarget.value) return
  deleting.value = true
  try {
    await request(`${baseUrl()}/${encodeURIComponent(deleteTarget.value.id)}`, { method: 'DELETE' })
    toasts.success(t('cleanup.deleted').replace('{name}', deleteTarget.value.name))
    deleteTarget.value = null
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

// ─── Lifecycle ─────────────────────────────────────────────────────────────────

onMounted(load)
</script>

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
        <p class="text-xs text-muted mt-1 max-w-2xl" data-testid="cleanup-warning">{{ t('cleanup.deletesHint') }}</p>
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
      <TableScroller v-else>
      <table class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th scope="col" class="text-left text-caption font-semibold text-muted px-5 py-2.5">
              {{ t('cleanup.col.name') }}
            </th>
            <th scope="col" data-drop-order="2" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.type') }}
            </th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.rule') }}
            </th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.enabled') }}
            </th>
            <th scope="col" data-drop-order="1" class="text-left text-caption font-semibold text-muted px-3 py-2.5">
              {{ t('cleanup.col.lastRun') }}
            </th>
            <th scope="col" class="text-right text-caption font-semibold text-muted px-5 py-2.5" :class="ACTIONS_HEAD">
              {{ t('cleanup.col.actions') }}
            </th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr
            v-for="policy in items"
            :key="policy.id"
            class="group/row hover:bg-surface-hover/30 transition-colors"
          >
            <!-- Name -->
            <td class="px-5 py-3.5" :class="GROW_CELL">
              <span class="block truncate font-medium text-sm text-foreground" :title="policy.name">{{ policy.name }}</span>
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
            <td class="px-5 py-3.5" :class="ACTIONS_CELL">
              <RowActions :actions="policyActions(policy)" :subject="policy.name" :inline="4" @action="(key) => onPolicyAction(key, policy)" />
            </td>
          </tr>
        </tbody>
      </table>
      </TableScroller>
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
    <CleanupDryRunDialog
      :open="preview !== null"
      :title="preview?.name ?? ''"
      :repositories="preview?.repositories ?? []"
      @close="preview = null"
    />

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
import AdminDialog from '../AdminDialog.vue'
import AppButton from '../AppButton.vue'
import AppToggle from '../AppToggle.vue'
import CleanupPolicyForm from '../CleanupPolicyForm.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import CleanupDryRunDialog from '../CleanupDryRunDialog.vue'
import {
  createCleanupPolicy,
  deleteCleanupPolicy,
  dryRunCleanupPolicy,
  listCleanupPolicies,
  runCleanupPolicy,
  updateCleanupPolicy,
  type CleanupPreview,
} from '../../api/cleanup'
import { formatDateTime } from '../../lib/datetime'
import {
  cleanupFormFrom,
  cleanupFormValid,
  cleanupPayload,
  cleanupPolicyFromApi,
  emptyCleanupForm,
  type CleanupPolicyView as CleanupPolicy,
} from '../../lib/cleanupPolicy'
import { t, tp } from '../../i18n'
import { useToastStore } from '../../stores/toast'
import RowActions, { type RowAction } from '../ui/RowActions.vue'
import TableScroller from '../ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from '../ui/tableStyles'
import {
  PlusIcon,
  PencilSquareIcon,
  TrashIcon,
  PlayIcon,
  EyeIcon,
  QueueListIcon,
  CalendarIcon,
} from '@heroicons/vue/20/solid'

const props = defineProps<{
  repoName: string
}>()

const toasts = useToastStore()

const items = ref<CleanupPolicy[]>([])
const loading = ref(true)

// Running
const runningId = ref<string | null>(null)
const previewingId = ref<string | null>(null)
const preview = ref<{ name: string; repositories: CleanupPreview[] } | null>(null)
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

// ─── Helpers ───────────────────────────────────────────────────────────────────

// ─── Data Loading ──────────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loading.value = true
  try {
    items.value = (await listCleanupPolicies(props.repoName)).map(cleanupPolicyFromApi)
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
    await updateCleanupPolicy(props.repoName, policy.id, { enabled: !policy.enabled })
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
    await runCleanupPolicy(props.repoName, policy.id)
    toasts.success(tp('cleanup.runTriggered', { name: policy.name }))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.runFailed'))
  } finally {
    runningId.value = null
  }
}

async function previewPolicy(policy: CleanupPolicy): Promise<void> {
  previewingId.value = policy.id
  try {
    const res = await dryRunCleanupPolicy(props.repoName, policy.id)
    preview.value = { name: policy.name, repositories: [res] }
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('cleanup.dryRun.failed'))
  } finally {
    previewingId.value = null
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
      await createCleanupPolicy(props.repoName, payload)
      toasts.success(tp('cleanup.created', { name: form.name }))
    } else if (editingPolicy.value) {
      await updateCleanupPolicy(props.repoName, editingPolicy.value.id, payload)
      toasts.success(tp('cleanup.updated', { name: form.name }))
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
    await deleteCleanupPolicy(props.repoName, deleteTarget.value.id)
    toasts.success(tp('cleanup.deleted', { name: deleteTarget.value.name }))
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

function policyActions(policy: CleanupPolicy): RowAction[] {
  return [
    { key: 'run', label: t('cleanup.run'), icon: PlayIcon, loading: runningId.value === policy.id },
    { key: 'dry-run', label: t('cleanup.dryRun'), icon: EyeIcon, loading: previewingId.value === policy.id },
    { key: 'edit', label: t('cleanup.editTitle'), icon: PencilSquareIcon },
    { key: 'delete', label: t('cleanup.deleteTitle'), icon: TrashIcon, tone: 'danger' },
  ]
}

function onPolicyAction(key: string, policy: CleanupPolicy): void {
  if (key === 'run') void runPolicy(policy)
  else if (key === 'dry-run') void previewPolicy(policy)
  else if (key === 'edit') openDrawer('edit', policy)
  else if (key === 'delete') openDeleteModal(policy)
}
</script>

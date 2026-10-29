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
      <AppButton size="sm" class="shrink-0" @click="openDrawer()">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('cleanup.add') }}
      </AppButton>
    </div>

    <!-- ─── Loading skeleton ────────────────────────────────────────────────── -->
    <SkeletonRows v-if="loading" actions>
      <div class="h-4 w-36 rounded skel" />
      <div class="h-5 w-24 rounded skel" />
      <div class="h-4 w-28 rounded skel" />
      <div class="flex-1" />
      <div class="w-8 h-5 rounded-full skel" />
    </SkeletonRows>

    <!-- ─── Table ───────────────────────────────────────────────────────────── -->
    <div v-else class="bg-surface rounded-xl border border-border overflow-hidden">
      <!-- Empty state -->
      <IconEmptyState
        v-if="items.length === 0"
        :icon="TrashIcon"
        :title="t('cleanup.empty')"
        :hint="t('cleanup.emptyHint')"
        :action-label="t('cleanup.add')"
        @action="openDrawer()"
      />

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
              <RowActions :actions="rowActions(policy.id, runningId)" :subject="policy.name" :inline="4" @action="(key) => onPolicyAction(key, policy)" />
            </td>
          </tr>
        </tbody>
      </table>
      </TableScroller>
    </div>

    <CleanupPolicyDrawer
      v-model="form"
      :open="drawerOpen"
      :editing-name="editingPolicy?.name ?? null"
      :saving="savingDrawer"
      @close="closeDrawer"
      @save="saveDrawer"
    />

    <CleanupDryRunDialog
      :open="preview !== null"
      :title="preview?.name ?? ''"
      :repositories="preview?.repositories ?? []"
      @close="preview = null"
    />

    <CleanupDeleteDialog
      :name="deleteTarget?.name ?? null"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="cancelDelete"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import AppButton from '../AppButton.vue'
import AppToggle from '../AppToggle.vue'
import CleanupPolicyDrawer from '../CleanupPolicyDrawer.vue'
import CleanupDeleteDialog from '../CleanupDeleteDialog.vue'
import CleanupDryRunDialog from '../CleanupDryRunDialog.vue'
import {
  createCleanupPolicy,
  deleteCleanupPolicy,
  dryRunCleanupPolicy,
  listCleanupPolicies,
  runCleanupPolicy,
  updateCleanupPolicy,
} from '../../api/cleanup'
import { formatDateTime } from '../../lib/datetime'
import { cleanupPayload, cleanupPolicyFromApi, type CleanupPolicyView as CleanupPolicy } from '../../lib/cleanupPolicy'
import { useCleanupPolicyPanel } from '../../composables/useCleanupPolicyPanel'
import { t, tp } from '../../i18n'
import { useToastStore } from '../../stores/toast'
import RowActions from '../ui/RowActions.vue'
import IconEmptyState from '../ui/IconEmptyState.vue'
import SkeletonRows from '../ui/SkeletonRows.vue'
import TableScroller from '../ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from '../ui/tableStyles'
import { PlusIcon, TrashIcon, QueueListIcon, CalendarIcon } from '@heroicons/vue/20/solid'

const props = defineProps<{
  repoName: string
}>()

const toasts = useToastStore()

const items = ref<CleanupPolicy[]>([])
const loading = ref(true)

const {
  drawerOpen,
  editing: editingPolicy,
  form,
  formValid,
  saving: savingDrawer,
  togglingId,
  deleteTarget,
  deleting,
  previewingId,
  preview,
  openDrawer,
  closeDrawer,
  rowActions,
} = useCleanupPolicyPanel()
const runningId = ref<string | null>(null)

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

async function saveDrawer(): Promise<void> {
  if (!formValid.value) return
  savingDrawer.value = true
  try {
    const payload = cleanupPayload(form.value)

    if (!editingPolicy.value) {
      await createCleanupPolicy(props.repoName, payload)
      toasts.success(tp('cleanup.created', { name: form.value.name }))
    } else {
      await updateCleanupPolicy(props.repoName, editingPolicy.value.id, payload)
      toasts.success(tp('cleanup.updated', { name: form.value.name }))
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

function onPolicyAction(key: string, policy: CleanupPolicy): void {
  if (key === 'run') void runPolicy(policy)
  else if (key === 'dry-run') void previewPolicy(policy)
  else if (key === 'edit') openDrawer(policy)
  else if (key === 'delete') openDeleteModal(policy)
}
</script>

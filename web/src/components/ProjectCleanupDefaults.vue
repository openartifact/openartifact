<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Default cleanup policies of a project. Every member can read them; owners and
  global administrators create, edit and delete. A hosted repository with an
  enabled policy of its own ignores these defaults.
-->
<template>
  <section class="rounded-xl border border-border bg-surface p-4 sm:p-5" :aria-label="t('projects.cleanup.title')" data-testid="project-cleanup">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h2 class="text-sm font-semibold text-foreground">{{ t('projects.cleanup.title') }}</h2>
        <p class="text-xs text-muted mt-0.5 max-w-2xl" data-testid="cleanup-override-note">{{ t('projects.cleanup.hint') }}</p>
      </div>
      <AppButton v-if="canEdit" size="sm" class="shrink-0" data-testid="cleanup-add" @click="openDrawer()">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('cleanup.add') }}
      </AppButton>
    </div>

    <p v-if="loading" class="mt-4 text-sm text-muted">{{ t('common.loading') }}</p>
    <p v-else-if="failed" class="mt-4 text-sm text-danger" role="alert">{{ t('projects.cleanup.loadFailed') }}</p>
    <div v-else-if="items.length === 0" class="mt-4 py-8 text-center">
      <p class="text-sm font-medium text-foreground">{{ t('projects.cleanup.empty') }}</p>
      <p v-if="canEdit" class="text-xs text-muted mt-1">{{ t('projects.cleanup.emptyHint') }}</p>
    </div>

    <TableScroller v-else class="mt-4 rounded-lg border border-border">
      <table class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th scope="col" class="text-left text-caption font-semibold text-muted px-4 py-2.5">{{ t('cleanup.col.name') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('cleanup.col.rule') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('cleanup.col.enabled') }}</th>
            <th v-if="canEdit" scope="col" class="text-right text-caption font-semibold text-muted px-4 py-2.5" :class="ACTIONS_HEAD">{{ t('cleanup.col.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="p in items" :key="p.id" class="group/row" data-testid="cleanup-row">
            <td class="truncate px-4 py-3 text-sm font-medium text-foreground" :class="GROW_CELL" :title="p.name">{{ p.name }}</td>
            <td class="px-3 py-3 text-sm text-muted">
              <span v-if="p.keep_count != null">{{ t('cleanup.keepLast') }} <span class="font-mono font-semibold text-foreground">{{ p.keep_count }}</span></span>
              <span v-else-if="p.keep_days != null">{{ t('cleanup.keepFor') }} <span class="font-mono font-semibold text-foreground">{{ p.keep_days }}</span> {{ t('cleanup.days') }}</span>
              <span v-else>—</span>
            </td>
            <td class="px-3 py-3">
              <AppToggle
                v-if="canEdit"
                size="sm"
                :model-value="p.enabled"
                :label="t('cleanup.enable')"
                :disabled="togglingId === p.id"
                @update:model-value="toggle(p)"
              />
              <span v-else class="text-sm text-muted">{{ p.enabled ? t('projects.cleanup.enabled') : t('projects.cleanup.disabled') }}</span>
            </td>
            <td v-if="canEdit" class="px-4 py-3" :class="ACTIONS_CELL">
              <RowActions :actions="rowActions(p.id)" :subject="p.name" @action="(key) => onPolicyAction(key, p)" />
            </td>
          </tr>
        </tbody>
      </table>
    </TableScroller>

    <p v-if="!canEdit" class="mt-3 text-xs text-muted" data-testid="cleanup-readonly">{{ t('projects.cleanup.readOnly') }}</p>

    <CleanupPolicyDrawer
      v-model="form"
      :open="drawerOpen"
      :editing-name="editing?.name ?? null"
      :saving="saving"
      @close="closeDrawer"
      @save="save"
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
      @close="deleteTarget = null"
    />
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { PlusIcon } from '@heroicons/vue/20/solid'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import CleanupPolicyDrawer from './CleanupPolicyDrawer.vue'
import CleanupDryRunDialog from './CleanupDryRunDialog.vue'
import CleanupDeleteDialog from './CleanupDeleteDialog.vue'
import RowActions from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import {
  createProjectCleanupPolicy,
  deleteProjectCleanupPolicy,
  dryRunProjectCleanupPolicy,
  listProjectCleanupPolicies,
  updateProjectCleanupPolicy,
} from '../api/projects'
import { cleanupPayload, cleanupPolicyFromApi, type CleanupPolicyView } from '../lib/cleanupPolicy'
import { useCleanupPolicyPanel } from '../composables/useCleanupPolicyPanel'
import { errorStatus } from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n'

const props = defineProps<{ project: string; canEdit: boolean }>()

const toasts = useToastStore()
const items = ref<CleanupPolicyView[]>([])
const loading = ref(true)
const failed = ref(false)

const {
  drawerOpen,
  editing,
  form,
  formValid,
  saving,
  togglingId,
  deleteTarget,
  deleting,
  previewingId,
  preview,
  openDrawer,
  closeDrawer,
  rowActions,
} = useCleanupPolicyPanel()

let generation = 0

async function load(): Promise<void> {
  const mine = ++generation
  loading.value = true
  failed.value = false
  try {
    const list = await listProjectCleanupPolicies(props.project)
    if (mine === generation) items.value = list.map(cleanupPolicyFromApi)
  } catch {
    if (mine === generation) failed.value = true
  } finally {
    if (mine === generation) loading.value = false
  }
}

function failure(err: unknown, fallback: string): string {
  return errorStatus(err) === 403 ? t('projects.cleanup.forbidden') : fallback
}

async function save(): Promise<void> {
  if (!formValid.value || saving.value) return
  saving.value = true
  try {
    const payload = cleanupPayload(form.value)
    if (editing.value) {
      await updateProjectCleanupPolicy(props.project, editing.value.id, payload)
      toasts.success(tp('cleanup.updated', { name: form.value.name }))
    } else {
      await createProjectCleanupPolicy(props.project, payload)
      toasts.success(tp('cleanup.created', { name: form.value.name }))
    }
    closeDrawer()
    await load()
  } catch (err) {
    toasts.error(failure(err, t('projects.cleanup.saveFailed')))
  } finally {
    saving.value = false
  }
}

async function toggle(p: CleanupPolicyView): Promise<void> {
  togglingId.value = p.id
  try {
    await updateProjectCleanupPolicy(props.project, p.id, { enabled: !p.enabled })
    p.enabled = !p.enabled
  } catch (err) {
    toasts.error(failure(err, t('cleanup.updateFailed')))
  } finally {
    togglingId.value = null
  }
}

async function previewPolicy(p: CleanupPolicyView): Promise<void> {
  previewingId.value = p.id
  try {
    const res = await dryRunProjectCleanupPolicy(props.project, p.id)
    preview.value = { name: p.name, repositories: res.repositories }
  } catch (err) {
    toasts.error(failure(err, t('cleanup.dryRun.failed')))
  } finally {
    previewingId.value = null
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  try {
    await deleteProjectCleanupPolicy(props.project, target.id)
    toasts.success(tp('cleanup.deleted', { name: target.name }))
    deleteTarget.value = null
    await load()
  } catch (err) {
    toasts.error(failure(err, t('projects.cleanup.deleteFailed')))
  } finally {
    deleting.value = false
  }
}

watch(() => props.project, load, { immediate: true })

function onPolicyAction(key: string, p: CleanupPolicyView): void {
  if (key === 'dry-run') void previewPolicy(p)
  else if (key === 'edit') openDrawer(p)
  else if (key === 'delete') deleteTarget.value = p
}
</script>

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

    <div v-else class="mt-4 overflow-x-auto rounded-lg border border-border">
      <table class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th scope="col" class="text-left text-caption font-semibold text-muted px-4 py-2.5">{{ t('cleanup.col.name') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('cleanup.col.rule') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('cleanup.col.enabled') }}</th>
            <th v-if="canEdit" scope="col" class="text-right text-caption font-semibold text-muted px-4 py-2.5">{{ t('cleanup.col.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="p in items" :key="p.id" data-testid="cleanup-row">
            <td class="px-4 py-3 text-sm font-medium text-foreground">{{ p.name }}</td>
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
            <td v-if="canEdit" class="px-4 py-3">
              <div class="flex items-center gap-0.5 justify-end">
                <button
                  type="button"
                  :title="t('cleanup.dryRun')"
                  :aria-label="t('cleanup.dryRun')"
                  :disabled="previewingId === p.id"
                  class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors disabled:opacity-40"
                  data-testid="cleanup-dry-run"
                  @click="previewPolicy(p)"
                >
                  <EyeIcon class="w-4 h-4" />
                </button>
                <button
                  type="button"
                  :title="t('cleanup.editTitle')"
                  :aria-label="t('cleanup.editTitle')"
                  class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  @click="openDrawer(p)"
                >
                  <PencilIcon class="w-4 h-4" />
                </button>
                <button
                  type="button"
                  :title="t('cleanup.deleteTitle')"
                  :aria-label="t('cleanup.deleteTitle')"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
                  @click="deleteTarget = p"
                >
                  <TrashIcon class="w-4 h-4" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p v-if="!canEdit" class="mt-3 text-xs text-muted" data-testid="cleanup-readonly">{{ t('projects.cleanup.readOnly') }}</p>

    <AdminDialog
      drawer
      max-width="max-w-md"
      :open="drawerOpen"
      :eyebrow="editing ? t('cleanup.editTitle') : t('cleanup.newLabel')"
      :title="editing ? `${t('cleanup.editTitle')} · ${editing.name}` : t('cleanup.addTitle')"
      @close="closeDrawer"
    >
      <CleanupPolicyForm v-model="form" />
      <template #footer>
        <AppButton variant="ghost" @click="closeDrawer">{{ t('common.cancel') }}</AppButton>
        <AppButton :disabled="!formValid" :loading="saving" @click="save">
          {{ editing ? t('common.save') : t('cleanup.create') }}
        </AppButton>
      </template>
    </AdminDialog>

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
      @close="deleteTarget = null"
    >
      <p class="text-sm text-muted">
        <span class="font-mono font-semibold text-foreground">{{ deleteTarget?.name }}</span>
        {{ t('cleanup.deleteBody') }}
      </p>
    </ConfirmDialog>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { EyeIcon, PencilIcon, PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import AppToggle from './AppToggle.vue'
import CleanupPolicyForm from './CleanupPolicyForm.vue'
import CleanupDryRunDialog from './CleanupDryRunDialog.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import type { CleanupPreview } from '../api/generated'
import {
  createProjectCleanupPolicy,
  deleteProjectCleanupPolicy,
  dryRunProjectCleanupPolicy,
  listProjectCleanupPolicies,
  updateProjectCleanupPolicy,
} from '../api/projects'
import {
  cleanupFormFrom,
  cleanupFormValid,
  cleanupPayload,
  cleanupPolicyFromApi,
  emptyCleanupForm,
  type CleanupPolicyForm as FormState,
  type CleanupPolicyView,
} from '../lib/cleanupPolicy'
import { errorStatus } from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n/en'

const props = defineProps<{ project: string; canEdit: boolean }>()

const toasts = useToastStore()
const items = ref<CleanupPolicyView[]>([])
const loading = ref(true)
const failed = ref(false)

const drawerOpen = ref(false)
const editing = ref<CleanupPolicyView | null>(null)
const form = ref<FormState>(emptyCleanupForm())
const saving = ref(false)
const togglingId = ref<string | null>(null)
const deleteTarget = ref<CleanupPolicyView | null>(null)
const deleting = ref(false)
const previewingId = ref<string | null>(null)
const preview = ref<{ name: string; repositories: CleanupPreview[] } | null>(null)

const formValid = computed(() => cleanupFormValid(form.value))

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

function openDrawer(policy?: CleanupPolicyView): void {
  editing.value = policy ?? null
  form.value = policy ? cleanupFormFrom(policy) : emptyCleanupForm()
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
  editing.value = null
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
</script>

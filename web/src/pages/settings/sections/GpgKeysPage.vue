<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <SectionActions>
        <div class="flex items-center gap-2">
          <AppButton variant="secondary" size="sm" :disabled="!canStoreKeys" @click="openImport">
            {{ t('admin.gpg.import') }}
          </AppButton>
          <AppButton variant="primary" size="sm" :disabled="!canStoreKeys" @click="openGenerate">
            {{ t('admin.gpg.generate') }}
          </AppButton>
        </div>
      </SectionActions>

    <div
      v-if="loaded && !canStoreKeys"
      class="rounded-lg border border-warning/30 bg-warning-soft px-4 py-3 text-sm text-warning"
      role="status"
    >
      {{ t('adm.gpg.noSecretKey') }}
    </div>

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="text-danger" role="alert">{{ loadError }}</div>
    <div v-else-if="keys.length === 0" class="rounded-lg border border-border bg-surface">
      <EmptyState :title="t('adm.gpg.empty.title')" :description="t('adm.gpg.empty.hint')" />
    </div>
    <ListTable v-else :columns="columns" :caption="t('adm.gpg.table')" :rows="keys" row-key="id">
      <template #cell-name="{ row }">
        <div class="flex items-center gap-2">
          <span class="truncate font-medium text-foreground" :title="(row as GpgKey).name">{{ (row as GpgKey).name }}</span>
          <span
            v-if="(row as GpgKey).read_only"
            class="inline-flex px-2 py-0.5 rounded-full text-caption font-medium bg-surface-hover text-muted"
          >
            {{ t('adm.gpg.readOnly') }}
          </span>
        </div>
        <div v-if="(row as GpgKey).user_id" class="truncate text-xs text-muted" :title="(row as GpgKey).user_id">{{ (row as GpgKey).user_id }}</div>
      </template>
      <template #cell-key_id="{ row }">
        <button
          type="button"
          class="font-mono text-xs text-foreground hover:text-primary focus-visible:ring-2 focus-visible:ring-primary rounded"
          :title="(row as GpgKey).fingerprint"
          :aria-label="`${t('adm.gpg.copyFingerprint')}: ${(row as GpgKey).fingerprint ?? ''}`"
          @click="copyFingerprint((row as GpgKey).fingerprint)"
        >
          {{ (row as GpgKey).key_id }}
        </button>
        <div class="max-w-56 font-mono text-caption text-muted break-all select-all">{{ (row as GpgKey).fingerprint }}</div>
      </template>
      <template #cell-algorithm="{ row }">
        <span class="font-mono text-xs">{{ (row as GpgKey).algorithm }}</span>
      </template>
      <template #cell-source="{ row }">
        <span class="inline-flex px-2 py-0.5 rounded-full text-caption font-medium bg-primary-soft text-primary">
          {{ sourceLabel((row as GpgKey).source) }}
        </span>
      </template>
      <template #cell-active="{ row }">
        <div class="flex flex-wrap items-center gap-1.5">
          <span
            v-if="(row as GpgKey).active_global"
            class="inline-flex px-2 py-0.5 rounded-full text-caption font-medium bg-success-soft/40 text-success"
          >
            {{ t('adm.gpg.global') }}
          </span>
          <span
            v-for="repo in (row as GpgKey).repositories ?? []"
            :key="repo"
            class="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-full text-caption font-medium bg-surface-hover text-foreground"
          >
            {{ repo }}
            <button
              type="button"
              class="rounded-full px-1 text-muted hover:text-danger focus-visible:ring-2 focus-visible:ring-primary"
              :aria-label="`${t('adm.gpg.deactivateRepo')}: ${repo}`"
              :disabled="busy"
              @click="deactivate((row as GpgKey), repo)"
            >
              ×
            </button>
          </span>
          <span v-if="!(row as GpgKey).active_global && !((row as GpgKey).repositories ?? []).length" class="text-xs text-muted">
            {{ t('adm.gpg.notActive') }}
          </span>
        </div>
      </template>
      <template #cell-created_at="{ row }">
        <span class="text-xs text-muted font-mono">{{ formatDate((row as GpgKey).created_at) }}</span>
      </template>
      <template #cell-expires_at="{ row }">
        <span class="text-xs text-muted font-mono">{{ formatDate((row as GpgKey).expires_at, t('adm.gpg.never')) }}</span>
      </template>
      <template #cell-actions="{ row }">
        <RowActions :actions="rowActions((row as GpgKey))" :subject="(row as GpgKey).name" @action="(key) => onAction(key, (row as GpgKey))" />
      </template>
    </ListTable>

    <GpgImportDialog :open="importOpen" :busy="busy" @close="importOpen = false" @done="onImported" />

    <GpgGenerateDialog :open="generateOpen" :busy="busy" @close="generateOpen = false" @done="onGenerated" />

    <!-- Activate for one repository -->
    <AdminDialog :open="repoTarget !== null" :eyebrow="t('adm.gpg.eyebrow')" :title="t('adm.gpg.activateRepo')" :description="repoTarget?.name" @close="repoTarget = null">
      <form id="gpg-repo-form" class="space-y-4" @submit.prevent="submitRepoActivate">
        <FormField id="gpg-repo" :label="t('adm.gpg.repository')" :hint="repoOptions.length === 0 && !reposLoading ? t('adm.gpg.noRepositories') : undefined">
          <select id="gpg-repo" v-model="repoChoice" required :disabled="reposLoading" :class="inputClass">
            <option value="" disabled>{{ t('adm.gpg.repositoryPlaceholder') }}</option>
            <option v-for="name in repoOptions" :key="name" :value="name">{{ name }}</option>
          </select>
        </FormField>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="repoTarget = null">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="gpg-repo-form" :loading="busy" :disabled="!repoChoice">
          {{ t('adm.gpg.activateRepo') }}
        </AppButton>
      </template>
    </AdminDialog>

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('adm.gpg.confirmDelete')"
      :description="forceNeeded ? t('adm.gpg.inUse') : (deleteTarget?.name ?? '')"
      :confirm-label="forceNeeded ? t('adm.gpg.deleteAnyway') : t('common.delete')"
      :busy="busy"
      @confirm="confirmDelete"
      @close="closeDelete"
    />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import GpgGenerateDialog from './GpgGenerateDialog.vue'
import GpgImportDialog from './GpgImportDialog.vue'
import AdminDialog from '../../../components/AdminDialog.vue'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import EmptyState from '../../../components/EmptyState.vue'
import FormField from '../../../components/FormField.vue'
import SectionActions from '../../../components/SectionActions.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import RowActions, { type RowAction } from '../../../components/ui/RowActions.vue'
import { ArrowDownTrayIcon, CheckBadgeIcon, FolderPlusIcon, TrashIcon, XCircleIcon } from '@heroicons/vue/20/solid'
import { GPG_SIGNED_FORMATS } from '../../../lib/formats.generated'
import {
  activateGpgKey,
  deactivateGpgKey,
  deleteGpgKey,
  exportGpgKey,
  listGpgKeys,
  type GpgKey,
} from '../../../api/gpg'
import { listAllRepositories } from '../../../api/repositories'
import { ApiError } from '../../../api/client'
import { errorMessage, inputClass } from '../../../lib/gpgForm'
import { formatDate } from '../../../lib/datetime'
import { useToastStore } from '../../../stores/toast'
import { t, tDynamic } from '../../../i18n'


const toasts = useToastStore()

const keys = ref<GpgKey[]>([])
const canStoreKeys = ref(false)
const loaded = ref(false)
const loading = ref(false)
const loadError = ref('')
const busy = ref(false)

const columns: ListColumn[] = [
  { key: 'name', label: t('admin.gpg.column.name'), grow: true },
  { key: 'key_id', label: t('admin.gpg.column.keyId') },
  { key: 'algorithm', label: t('admin.gpg.column.algorithm'), dropOrder: 3 },
  { key: 'source', label: t('admin.gpg.column.source'), dropOrder: 4 },
  { key: 'active', label: t('admin.gpg.column.active') },
  { key: 'created_at', label: t('admin.gpg.column.created'), dropOrder: 1 },
  { key: 'expires_at', label: t('admin.gpg.column.expires'), dropOrder: 2 },
  { key: 'actions', label: t('common.actions'), actions: true },
]

function rowActions(key: GpgKey): RowAction[] {
  const editable = !key.read_only
  return [
    editable && !key.active_global
      ? { key: 'activate', label: t('adm.gpg.activateGlobal'), icon: CheckBadgeIcon, disabled: busy.value }
      : { key: 'deactivate', label: t('adm.gpg.deactivateGlobal'), icon: XCircleIcon, disabled: busy.value, hidden: !editable },
    { key: 'activateRepo', label: t('adm.gpg.activateRepo'), icon: FolderPlusIcon, disabled: busy.value, hidden: !editable },
    { key: 'export', label: t('adm.gpg.export'), icon: ArrowDownTrayIcon, disabled: busy.value },
    { key: 'delete', label: t('common.delete'), icon: TrashIcon, tone: 'danger', disabled: busy.value, hidden: !editable },
  ]
}

function onAction(action: string, key: GpgKey): void {
  if (action === 'activate') void activate(key)
  else if (action === 'deactivate') void deactivate(key)
  else if (action === 'activateRepo') openRepoActivate(key)
  else if (action === 'export') void exportKey(key)
  else if (action === 'delete') askDelete(key)
}

function sourceLabel(source: GpgKey['source']): string {
  return source ? tDynamic(`adm.gpg.source.${source}`) : ''
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listGpgKeys()
    keys.value = res.keys
    canStoreKeys.value = res.can_store_keys
    loaded.value = true
  } catch (err) {
    loadError.value = errorMessage(err, t('adm.gpg.loadFailed'))
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

async function copyFingerprint(fingerprint: string | undefined): Promise<void> {
  if (!fingerprint) return
  try {
    await navigator.clipboard.writeText(fingerprint)
    toasts.success(t('common.copied'))
  } catch {
    // clipboard unavailable (insecure context): the fingerprint is selectable in the table
  }
}

// ---- import / generate (the dialogs own their form state) ----

const importOpen = ref(false)
const generateOpen = ref(false)

function openImport(): void {
  importOpen.value = true
}

function openGenerate(): void {
  generateOpen.value = true
}

async function onImported(): Promise<void> {
  importOpen.value = false
  await load()
}

async function onGenerated(): Promise<void> {
  generateOpen.value = false
  await load()
}

// ---- activate / deactivate ----

async function activate(key: GpgKey, repository?: string): Promise<boolean> {
  if (!key.id || busy.value) return false
  busy.value = true
  try {
    await activateGpgKey(key.id, repository)
    toasts.success(t('adm.gpg.activated'))
    await load()
    return true
  } catch (err) {
    toasts.error(errorMessage(err, t('adm.gpg.activateFailed')))
    return false
  } finally {
    busy.value = false
  }
}

async function deactivate(key: GpgKey, repository?: string): Promise<void> {
  if (!key.id || busy.value) return
  busy.value = true
  try {
    await deactivateGpgKey(key.id, repository)
    toasts.success(t('adm.gpg.deactivated'))
    await load()
  } catch (err) {
    toasts.error(errorMessage(err, t('adm.gpg.deactivateFailed')))
  } finally {
    busy.value = false
  }
}

const repoTarget = ref<GpgKey | null>(null)
const repoChoice = ref('')
const repoOptions = ref<string[]>([])
const reposLoading = ref(false)

const SIGNED_FORMATS: ReadonlySet<string> = new Set(GPG_SIGNED_FORMATS)

async function openRepoActivate(key: GpgKey): Promise<void> {
  repoTarget.value = key
  repoChoice.value = ''
  reposLoading.value = true
  try {
    const repos = await listAllRepositories()
    repoOptions.value = repos
      .filter((r) => SIGNED_FORMATS.has(r.format) && (r.repo_type ?? 'hosted') === 'hosted')
      .map((r) => r.name)
      .sort()
  } catch (err) {
    repoOptions.value = []
    toasts.error(errorMessage(err, t('adm.gpg.loadFailed')))
  } finally {
    reposLoading.value = false
  }
}

async function submitRepoActivate(): Promise<void> {
  const key = repoTarget.value
  if (!key || !repoChoice.value) return
  if (await activate(key, repoChoice.value)) repoTarget.value = null
}

// ---- export ----

async function exportKey(key: GpgKey): Promise<void> {
  try {
    await exportGpgKey(key)
  } catch (err) {
    toasts.error(errorMessage(err, t('adm.gpg.exportFailed')))
  }
}

// ---- delete ----

const deleteTarget = ref<GpgKey | null>(null)
const forceNeeded = ref(false)

function askDelete(key: GpgKey): void {
  forceNeeded.value = false
  deleteTarget.value = key
}

function closeDelete(): void {
  deleteTarget.value = null
  forceNeeded.value = false
}

async function confirmDelete(): Promise<void> {
  const key = deleteTarget.value
  if (!key?.id || busy.value) return
  busy.value = true
  try {
    await deleteGpgKey(key.id, forceNeeded.value)
    toasts.success(t('adm.gpg.deleted'))
    closeDelete()
    await load()
  } catch (err) {
    if (err instanceof ApiError && err.status === 409 && err.code === 'key_in_use') {
      forceNeeded.value = true
    } else {
      toasts.error(errorMessage(err, t('adm.gpg.deleteFailed')))
      closeDelete()
    }
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

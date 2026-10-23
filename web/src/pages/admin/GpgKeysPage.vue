<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <PageHeader :title="t('admin.gpg.title')" :description="t('admin.gpg.description')">
      <template #actions>
        <div class="flex items-center gap-2">
          <AppButton variant="secondary" size="sm" :disabled="!canStoreKeys" @click="openImport">
            {{ t('admin.gpg.import') }}
          </AppButton>
          <AppButton variant="primary" size="sm" :disabled="!canStoreKeys" @click="openGenerate">
            {{ t('admin.gpg.generate') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <div
      v-if="loaded && !canStoreKeys"
      class="rounded-lg border border-warning/30 bg-warning-soft px-4 py-3 text-sm text-warning"
      role="status"
    >
      {{ t('adm.gpg.noSecretKey') }}
    </div>

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="text-danger" role="alert">{{ loadError }}</div>
    <EmptyState v-else-if="keys.length === 0" :title="t('common.empty')" />
    <DataTable v-else :columns="columns" :rows="keys as Record<string, unknown>[]" row-key="id">
      <template #cell-name="{ row }">
        <div class="flex items-center gap-2">
          <span class="font-medium text-foreground">{{ asKey(row).name }}</span>
          <span
            v-if="asKey(row).read_only"
            class="inline-flex px-2 py-0.5 rounded-full text-caption font-medium bg-surface-hover text-muted"
          >
            {{ t('adm.gpg.readOnly') }}
          </span>
        </div>
        <div v-if="asKey(row).user_id" class="text-xs text-muted">{{ asKey(row).user_id }}</div>
      </template>
      <template #cell-key_id="{ row }">
        <button
          type="button"
          class="font-mono text-xs text-foreground hover:text-primary focus-visible:ring-2 focus-visible:ring-primary rounded"
          :title="asKey(row).fingerprint"
          :aria-label="`${t('adm.gpg.copyFingerprint')}: ${asKey(row).fingerprint ?? ''}`"
          @click="copyFingerprint(asKey(row).fingerprint)"
        >
          {{ asKey(row).key_id }}
        </button>
        <div class="font-mono text-caption text-muted break-all select-all">{{ asKey(row).fingerprint }}</div>
      </template>
      <template #cell-algorithm="{ row }">
        <span class="font-mono text-xs">{{ asKey(row).algorithm }}</span>
      </template>
      <template #cell-source="{ row }">
        <span class="inline-flex px-2 py-0.5 rounded-full text-caption font-medium bg-primary-soft text-primary">
          {{ sourceLabel(asKey(row).source) }}
        </span>
      </template>
      <template #cell-active="{ row }">
        <div class="flex flex-wrap items-center gap-1.5">
          <span
            v-if="asKey(row).active_global"
            class="inline-flex px-2 py-0.5 rounded-full text-caption font-medium bg-success-soft/40 text-success"
          >
            {{ t('adm.gpg.global') }}
          </span>
          <span
            v-for="repo in asKey(row).repositories ?? []"
            :key="repo"
            class="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-full text-caption font-medium bg-surface-hover text-foreground"
          >
            {{ repo }}
            <button
              type="button"
              class="rounded-full px-1 text-muted hover:text-danger focus-visible:ring-2 focus-visible:ring-primary"
              :aria-label="`${t('adm.gpg.deactivateRepo')}: ${repo}`"
              :disabled="busy"
              @click="deactivate(asKey(row), repo)"
            >
              ×
            </button>
          </span>
          <span v-if="!asKey(row).active_global && !(asKey(row).repositories ?? []).length" class="text-xs text-muted">
            {{ t('adm.gpg.notActive') }}
          </span>
        </div>
      </template>
      <template #cell-created_at="{ row }">
        <span class="text-xs text-muted font-mono">{{ formatDate(asKey(row).created_at) }}</span>
      </template>
      <template #cell-expires_at="{ row }">
        <span class="text-xs text-muted font-mono">{{ formatDate(asKey(row).expires_at, t('adm.gpg.never')) }}</span>
      </template>
      <template #cell-actions="{ row }">
        <div class="flex flex-wrap gap-1.5" role="group" :aria-label="`${t('adm.gpg.actionsFor')} ${asKey(row).name}`">
          <template v-if="!asKey(row).read_only">
            <AppButton
              v-if="!asKey(row).active_global"
              variant="secondary"
              size="xs"
              :disabled="busy"
              :aria-label="`${t('adm.gpg.activateGlobal')}: ${asKey(row).name}`"
              @click="activate(asKey(row))"
            >
              {{ t('adm.gpg.activateGlobal') }}
            </AppButton>
            <AppButton
              v-else
              variant="secondary"
              size="xs"
              :disabled="busy"
              :aria-label="`${t('adm.gpg.deactivateGlobal')}: ${asKey(row).name}`"
              @click="deactivate(asKey(row))"
            >
              {{ t('adm.gpg.deactivateGlobal') }}
            </AppButton>
            <AppButton
              variant="secondary"
              size="xs"
              :disabled="busy"
              :aria-label="`${t('adm.gpg.activateRepo')}: ${asKey(row).name}`"
              @click="openRepoActivate(asKey(row))"
            >
              {{ t('adm.gpg.activateRepo') }}
            </AppButton>
          </template>
          <AppButton
            variant="secondary"
            size="xs"
            :disabled="busy"
            :aria-label="`${t('adm.gpg.export')}: ${asKey(row).name}`"
            @click="exportKey(asKey(row))"
          >
            {{ t('adm.gpg.export') }}
          </AppButton>
          <AppButton
            v-if="!asKey(row).read_only"
            variant="danger-outline"
            size="xs"
            :disabled="busy"
            :aria-label="`${t('common.delete')}: ${asKey(row).name}`"
            @click="askDelete(asKey(row))"
          >
            {{ t('common.delete') }}
          </AppButton>
        </div>
      </template>
    </DataTable>

    <!-- Import -->
    <AdminDialog drawer max-width="max-w-xl" :open="importOpen" :eyebrow="t('adm.gpg.eyebrow')" :title="t('admin.gpg.import')" @close="closeImport">
      <form id="gpg-import-form" class="space-y-5" autocomplete="off" @submit.prevent="submitImport">
        <FormField id="gpg-import-name" :label="`${t('adm.gpg.name')} (${t('common.optional')})`" :hint="t('adm.gpg.nameHint')">
          <input id="gpg-import-name" v-model="importForm.name" type="text" :class="inputClass" />
        </FormField>
        <FormField id="gpg-armored" :label="t('adm.gpg.armored')">
          <textarea
            id="gpg-armored"
            v-model="importForm.privateKey"
            rows="10"
            required
            spellcheck="false"
            autocomplete="off"
            class="w-full px-3 py-2 border border-border-strong rounded-lg text-xs font-mono bg-surface-hover/40 text-foreground focus:ring-2 focus:ring-primary/50"
          />
        </FormField>
        <div>
          <label for="gpg-armored-file" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.armoredFile') }}</label>
          <input
            id="gpg-armored-file"
            type="file"
            accept=".asc,.gpg,.key,.pgp,text/plain"
            class="block w-full text-sm text-muted"
            @change="loadKeyFile"
          />
        </div>
        <FormField
          v-if="passphraseNeeded"
          id="gpg-passphrase"
          :label="t('adm.gpg.passphrase')"
          :hint="t('adm.gpg.passphraseRequired')"
        >
          <input
            id="gpg-passphrase"
            v-model="importForm.passphrase"
            type="password"
            required
            autocomplete="new-password"
            :class="inputClass"
          />
        </FormField>
        <label class="flex items-center gap-2 text-sm text-foreground">
          <input v-model="importForm.activate" type="checkbox" class="rounded border-border-strong text-primary focus:ring-primary/50" />
          {{ t('adm.gpg.makeActive') }}
        </label>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="closeImport">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="gpg-import-form" :loading="busy">
          {{ t('admin.gpg.import') }}
        </AppButton>
      </template>
    </AdminDialog>

    <!-- Generate -->
    <AdminDialog drawer max-width="max-w-xl" :open="generateOpen" :eyebrow="t('adm.gpg.eyebrow')" :title="t('admin.gpg.generate')" @close="closeGenerate">
      <form id="gpg-generate-form" class="space-y-5" @submit.prevent="submitGenerate">
        <FormField id="gpg-name" :label="`${t('adm.gpg.name')} (${t('common.optional')})`" :hint="t('adm.gpg.nameHint')">
          <input id="gpg-name" v-model="genForm.name" type="text" :class="inputClass" />
        </FormField>
        <FormField id="gpg-algorithm" :label="t('adm.gpg.algorithm')">
          <select id="gpg-algorithm" v-model="genForm.algorithm" :class="inputClass">
            <option v-for="algo in ALGORITHMS" :key="algo" :value="algo">{{ algo }}</option>
          </select>
        </FormField>
        <FormField id="gpg-user-name" :label="`${t('adm.gpg.userName')} (${t('common.optional')})`">
          <input id="gpg-user-name" v-model="genForm.userName" type="text" :class="inputClass" />
        </FormField>
        <FormField id="gpg-email" :label="`${t('adm.gpg.email')} (${t('common.optional')})`">
          <input id="gpg-email" v-model="genForm.email" type="email" :class="inputClass" />
        </FormField>
        <FormField id="gpg-comment" :label="`${t('adm.gpg.comment')} (${t('common.optional')})`">
          <input id="gpg-comment" v-model="genForm.comment" type="text" :class="inputClass" />
        </FormField>
        <label class="flex items-center gap-2 text-sm text-foreground">
          <input v-model="genForm.activate" type="checkbox" class="rounded border-border-strong text-primary focus:ring-primary/50" />
          {{ t('adm.gpg.makeActive') }}
        </label>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="closeGenerate">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="gpg-generate-form" :loading="busy">
          {{ t('admin.gpg.generate') }}
        </AppButton>
      </template>
    </AdminDialog>

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
import AdminDialog from '../../components/AdminDialog.vue'
import AppButton from '../../components/AppButton.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import DataTable from '../../components/DataTable.vue'
import type { Column } from '../../components/DataTable.vue'
import EmptyState from '../../components/EmptyState.vue'
import FormField from '../../components/FormField.vue'
import PageHeader from '../../components/PageHeader.vue'
import {
  activateGpgKey,
  deactivateGpgKey,
  deleteGpgKey,
  exportGpgKey,
  generateGpgKey,
  importGpgKey,
  listGpgKeys,
  type GpgAlgorithm,
  type GpgKey,
} from '../../api/gpg'
import { listAllRepositories } from '../../api/repositories'
import { ApiError } from '../../api/client'
import { formatDate } from '../../lib/datetime'
import { useToastStore } from '../../stores/toast'
import { t, tDynamic } from '../../i18n/en'

const ALGORITHMS: GpgAlgorithm[] = ['ed25519', 'rsa4096']
const inputClass =
  'w-full px-3 py-2 border border-border-strong rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50'

const toasts = useToastStore()

const keys = ref<GpgKey[]>([])
const canStoreKeys = ref(false)
const loaded = ref(false)
const loading = ref(false)
const loadError = ref('')
const busy = ref(false)

const columns: Column[] = [
  { key: 'name', label: t('admin.gpg.column.name') },
  { key: 'key_id', label: t('admin.gpg.column.keyId') },
  { key: 'algorithm', label: t('admin.gpg.column.algorithm') },
  { key: 'source', label: t('admin.gpg.column.source') },
  { key: 'active', label: t('admin.gpg.column.active') },
  { key: 'created_at', label: t('admin.gpg.column.created') },
  { key: 'expires_at', label: t('admin.gpg.column.expires') },
  { key: 'actions', label: t('common.actions') },
]

function asKey(row: Record<string, unknown>): GpgKey {
  return row as GpgKey
}

function sourceLabel(source: GpgKey['source']): string {
  return source ? tDynamic(`adm.gpg.source.${source}`) : ''
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
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

// ---- import (private key material lives only in this form) ----

const emptyImport = () => ({ name: '', privateKey: '', passphrase: '', activate: false })
const importOpen = ref(false)
const importForm = ref(emptyImport())
const passphraseNeeded = ref(false)

function openImport(): void {
  importForm.value = emptyImport()
  passphraseNeeded.value = false
  importOpen.value = true
}

function closeImport(): void {
  importOpen.value = false
  importForm.value = emptyImport()
  passphraseNeeded.value = false
}

async function loadKeyFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (file) importForm.value.privateKey = await file.text()
  input.value = ''
}

async function submitImport(): Promise<void> {
  if (busy.value) return
  busy.value = true
  const { name, privateKey, passphrase, activate } = importForm.value
  try {
    await importGpgKey({
      name: name.trim() || undefined,
      private_key: privateKey,
      passphrase: passphrase || undefined,
      activate: activate || undefined,
    })
    toasts.success(t('adm.gpg.imported'))
    closeImport()
    await load()
  } catch (err) {
    importForm.value.passphrase = ''
    if (err instanceof ApiError && err.code === 'passphrase_required') {
      passphraseNeeded.value = true
    } else {
      toasts.error(errorMessage(err, t('adm.gpg.importFailed')))
    }
  } finally {
    busy.value = false
  }
}

// ---- generate ----

const emptyGenerate = () => ({ name: '', algorithm: 'ed25519' as GpgAlgorithm, userName: '', email: '', comment: '', activate: false })
const generateOpen = ref(false)
const genForm = ref(emptyGenerate())

function openGenerate(): void {
  genForm.value = emptyGenerate()
  generateOpen.value = true
}

function closeGenerate(): void {
  generateOpen.value = false
}

async function submitGenerate(): Promise<void> {
  if (busy.value) return
  busy.value = true
  const f = genForm.value
  try {
    await generateGpgKey({
      name: f.name.trim() || undefined,
      algorithm: f.algorithm,
      user_name: f.userName.trim() || undefined,
      email: f.email.trim() || undefined,
      comment: f.comment.trim() || undefined,
      activate: f.activate || undefined,
    })
    toasts.success(t('adm.gpg.generated'))
    closeGenerate()
    await load()
  } catch (err) {
    toasts.error(errorMessage(err, t('adm.gpg.generateFailed')))
  } finally {
    busy.value = false
  }
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

const SIGNED_FORMATS = new Set(['deb', 'rpm'])

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

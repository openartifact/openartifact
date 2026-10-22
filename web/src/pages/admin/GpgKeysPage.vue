<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('admin.gpg.title')" :description="t('admin.gpg.description')">
      <template #actions>
        <div class="flex items-center gap-2">
          <AppButton variant="secondary" size="sm" @click="importOpen = true">
            {{ t('admin.gpg.import') }}
          </AppButton>
          <AppButton variant="primary" size="sm" @click="generateOpen = true">
            {{ t('admin.gpg.generate') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <div v-if="unavailable" class="rounded-lg border border-warning/30 bg-warning-soft px-4 py-3 text-sm text-warning" role="status">
      {{ t('adm.gpg.unavailable') }}
    </div>

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="text-danger" role="alert">{{ loadError }}</div>
    <EmptyState v-else-if="keys.length === 0" :title="t('common.empty')" />
    <DataTable v-else :columns="columns" :rows="keys as Record<string, unknown>[]" row-key="fingerprint">
      <template #cell-fingerprint="{ row }">
        <span class="font-mono text-xs text-foreground">{{ row.fingerprint }}</span>
      </template>
      <template #cell-actions="{ row }">
        <AppButton
          variant="danger-outline"
          size="xs"
          :aria-label="`${t('admin.gpg.revoke')}: ${row.fingerprint}`"
          @click="revoke(String(row.fingerprint))"
        >
          {{ t('admin.gpg.revoke') }}
        </AppButton>
      </template>
    </DataTable>

    <!-- Import Drawer -->
    <AdminDialog drawer max-width="max-w-xl" :open="importOpen" :eyebrow="t('adm.gpg.eyebrow')" :title="t('admin.gpg.import')" @close="importOpen = false">
      <form id="gpg-import-form" class="space-y-5" @submit.prevent="submitImport">
        <div>
          <label for="gpg-armored" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.armored') }}</label>
          <textarea
            id="gpg-armored"
            v-model="importForm.armored"
            rows="10"
            required
            class="w-full px-3 py-2 border border-border-strong rounded-lg text-xs font-mono bg-surface-hover/40 text-foreground focus:ring-2 focus:ring-primary/50"
          />
        </div>
        <div>
          <label for="gpg-passphrase" class="block text-sm font-medium text-foreground mb-1.5">
            {{ t('adm.gpg.passphrase') }} ({{ t('common.optional') }})
          </label>
          <input
            id="gpg-passphrase"
            v-model="importForm.passphrase"
            type="password"
            autocomplete="new-password"
            class="w-full px-3 py-2 border border-border-strong rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
          />
        </div>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="importOpen = false">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="gpg-import-form" :loading="busy">
          {{ t('admin.gpg.import') }}
        </AppButton>
      </template>
    </AdminDialog>

    <!-- Generate Drawer -->
    <AdminDialog drawer max-width="max-w-xl" :open="generateOpen" :eyebrow="t('adm.gpg.eyebrow')" :title="t('admin.gpg.generate')" @close="generateOpen = false">
      <form id="gpg-generate-form" class="space-y-5" @submit.prevent="submitGenerate">
        <div>
          <label for="gpg-name" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.name') }}</label>
          <input id="gpg-name" v-model="genForm.name" required type="text" class="w-full px-3 py-2 border border-border-strong rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50" />
        </div>
        <div>
          <label for="gpg-email" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.email') }}</label>
          <input id="gpg-email" v-model="genForm.email" required type="email" class="w-full px-3 py-2 border border-border-strong rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50" />
        </div>
        <div>
          <label for="gpg-comment" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.comment') }}</label>
          <input id="gpg-comment" v-model="genForm.comment" type="text" class="w-full px-3 py-2 border border-border-strong rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50" />
        </div>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="generateOpen = false">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="gpg-generate-form" :loading="busy">
          {{ t('admin.gpg.generate') }}
        </AppButton>
      </template>
    </AdminDialog>

    <ConfirmDialog
      :open="revokeTarget !== null"
      :title="t('adm.gpg.confirmRevoke')"
      :description="revokeTarget ?? ''"
      :confirm-label="t('admin.gpg.revoke')"
      :busy="busy"
      @confirm="confirmRevoke"
      @close="revokeTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../components/AppButton.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import PageHeader from '../../components/PageHeader.vue'
import { ref, onMounted } from 'vue'
import { listGpgKeys, importGpgKey, generateGpgKey, revokeGpgKey, type GpgKey } from '../../api/gpg'
import { ApiError } from '../../api/client'
import DataTable from '../../components/DataTable.vue'
import type { Column } from '../../components/DataTable.vue'
import EmptyState from '../../components/EmptyState.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const keys = ref<GpgKey[]>([])
const loading = ref(false)
const loadError = ref('')
const busy = ref(false)
// Set when the server answers 501: key management is not implemented there.
const unavailable = ref(false)
const toasts = useToastStore()

const importOpen = ref(false)
const generateOpen = ref(false)
const importForm = ref({ armored: '', passphrase: '' })
const genForm = ref({ name: '', email: '', comment: '' })

const columns: Column[] = [
  { key: 'fingerprint', label: t('admin.gpg.column.fingerprint') },
  { key: 'uid', label: t('admin.gpg.column.uid') },
  { key: 'created_at', label: t('admin.gpg.column.created') },
  { key: 'actions', label: t('common.actions') },
]

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listGpgKeys()
    keys.value = res.items
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.gpg.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function reportFailure(err: unknown, fallback: string): void {
  if (err instanceof ApiError && err.status === 501) unavailable.value = true
  toasts.error(err instanceof Error ? err.message : fallback)
}

async function submitImport(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await importGpgKey({ armored: importForm.value.armored, passphrase: importForm.value.passphrase || undefined })
    toasts.success(t('adm.gpg.imported'))
    importOpen.value = false
    importForm.value = { armored: '', passphrase: '' }
    await load()
  } catch (err) {
    reportFailure(err, t('adm.gpg.importFailed'))
  } finally {
    busy.value = false
  }
}

async function submitGenerate(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await generateGpgKey({ ...genForm.value })
    toasts.success(t('adm.gpg.generated'))
    generateOpen.value = false
    genForm.value = { name: '', email: '', comment: '' }
    await load()
  } catch (err) {
    reportFailure(err, t('adm.gpg.generateFailed'))
  } finally {
    busy.value = false
  }
}

const revokeTarget = ref<string | null>(null)

function revoke(fingerprint: string): void {
  revokeTarget.value = fingerprint
}

async function confirmRevoke(): Promise<void> {
  const fingerprint = revokeTarget.value
  if (!fingerprint) return
  busy.value = true
  try {
    await revokeGpgKey(fingerprint)
    toasts.success(t('adm.gpg.revoked'))
    revokeTarget.value = null
    await load()
  } catch (err) {
    reportFailure(err, t('adm.gpg.revokeFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <div class="flex items-center gap-2 text-[11px] uppercase tracking-[0.14em] text-muted font-mono">
          <span>{{ t('nav.admin') }}</span>
          <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
          <span class="text-foreground/70">{{ t('nav.gpg') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('admin.gpg.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('admin.gpg.description') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors font-medium"
          @click="importOpen = true"
        >
          {{ t('admin.gpg.import') }}
        </button>
        <button
          type="button"
          class="px-3 py-1.5 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium"
          @click="generateOpen = true"
        >
          {{ t('admin.gpg.generate') }}
        </button>
      </div>
    </div>

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
        <button
          type="button"
          class="text-xs text-danger hover:opacity-80"
          :aria-label="`${t('admin.gpg.revoke')}: ${row.fingerprint}`"
          @click="revoke(String(row.fingerprint))"
        >
          {{ t('admin.gpg.revoke') }}
        </button>
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
            class="w-full px-3 py-2 border border-border rounded-lg text-xs font-mono bg-surface-hover/40 text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
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
            class="w-full px-3 py-2 border border-border rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
        </div>
      </form>
      <template #footer>
        <button type="button" @click="importOpen = false" class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors">
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="gpg-import-form"
          :disabled="busy"
          class="px-3 py-1.5 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium disabled:opacity-50"
        >
          {{ t('admin.gpg.import') }}
        </button>
      </template>
    </AdminDialog>

    <!-- Generate Drawer -->
    <AdminDialog drawer max-width="max-w-xl" :open="generateOpen" :eyebrow="t('adm.gpg.eyebrow')" :title="t('admin.gpg.generate')" @close="generateOpen = false">
      <form id="gpg-generate-form" class="space-y-5" @submit.prevent="submitGenerate">
        <div>
          <label for="gpg-name" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.name') }}</label>
          <input id="gpg-name" v-model="genForm.name" required type="text" class="w-full px-3 py-2 border border-border rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50" />
        </div>
        <div>
          <label for="gpg-email" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.email') }}</label>
          <input id="gpg-email" v-model="genForm.email" required type="email" class="w-full px-3 py-2 border border-border rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50" />
        </div>
        <div>
          <label for="gpg-comment" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.gpg.comment') }}</label>
          <input id="gpg-comment" v-model="genForm.comment" type="text" class="w-full px-3 py-2 border border-border rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50" />
        </div>
      </form>
      <template #footer>
        <button type="button" @click="generateOpen = false" class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors">
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="gpg-generate-form"
          :disabled="busy"
          class="px-3 py-1.5 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium disabled:opacity-50"
        >
          {{ t('admin.gpg.generate') }}
        </button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ChevronRightIcon } from '@heroicons/vue/20/solid'
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

async function revoke(fingerprint: string): Promise<void> {
  if (!window.confirm(`${t('admin.gpg.revoke')}?`)) return
  try {
    await revokeGpgKey(fingerprint)
    toasts.success(t('adm.gpg.revoked'))
    await load()
  } catch (err) {
    reportFailure(err, t('adm.gpg.revokeFailed'))
  }
}

onMounted(load)
</script>

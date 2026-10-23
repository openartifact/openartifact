<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Drill-down of the units a peer deferred (waiting for a dependency or a retry). Loads when opened. -->
<template>
  <AdminDialog
    :open="peer !== null"
    :title="fill(t('federation.deferred.title'), { name: peer?.name ?? '' })"
    :description="t('federation.deferred.hint')"
    max-width="max-w-4xl"
    @close="emit('close')"
  >
    <div v-if="loading" class="space-y-2" aria-busy="true">
      <div class="h-4 w-full rounded skel" />
      <div class="h-4 w-2/3 rounded skel" />
    </div>
    <p v-else-if="error" role="alert" class="text-sm text-danger">{{ error }}</p>
    <p v-else-if="items.length === 0" class="text-sm text-muted">{{ t('federation.deferred.empty') }}</p>
    <div v-else class="overflow-x-auto">
      <table class="w-full" data-testid="federation-deferred">
        <thead>
          <tr class="border-b border-border text-xs font-semibold text-muted">
            <th class="text-left px-3 py-2">{{ t('federation.deferred.column.unit') }}</th>
            <th class="text-right px-3 py-2">{{ t('federation.deferred.column.seq') }}</th>
            <th class="text-right px-3 py-2">{{ t('federation.deferred.column.attempts') }}</th>
            <th class="text-left px-3 py-2">{{ t('federation.deferred.column.reason') }}</th>
            <th class="text-left px-3 py-2">{{ t('federation.deferred.column.next') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in items" :key="`${u.seq}:${u.unit_key}`" class="border-t border-border align-top">
            <td class="px-3 py-2 text-xs font-mono text-foreground break-all">{{ u.unit_key }}</td>
            <td class="px-3 py-2 text-xs font-mono tabular-nums text-muted text-right">{{ u.seq }}</td>
            <td class="px-3 py-2 text-xs font-mono tabular-nums text-muted text-right">{{ u.attempts }}</td>
            <td class="px-3 py-2 text-xs text-foreground">
              {{ u.reason }}
              <p v-if="u.last_error" class="mt-0.5 text-danger break-words">{{ u.last_error }}</p>
            </td>
            <td class="px-3 py-2 text-xs text-muted whitespace-nowrap">{{ formatDateTime(u.next_attempt_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <template #footer>
      <AppButton variant="primary" @click="emit('close')">{{ t('common.close') }}</AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import { listFederationDeferred, type FederationDeferredUnit, type FederationPeer } from '../api/federation'
import { formatDateTime } from '../lib/datetime'
import { fill } from '../lib/federation'
import { t } from '../i18n/en'

const props = defineProps<{
  repoName: string
  /** The peer whose deferred units are shown; null keeps the dialog closed. */
  peer: FederationPeer | null
}>()
const emit = defineEmits<{ close: [] }>()

const items = ref<FederationDeferredUnit[]>([])
const loading = ref(false)
const error = ref('')
let latest = 0

watch(
  () => props.peer?.id,
  async (id) => {
    const request = ++latest
    items.value = []
    error.value = ''
    if (!id) return
    loading.value = true
    try {
      const res = await listFederationDeferred(props.repoName, id)
      if (request === latest) items.value = res
    } catch (err) {
      if (request === latest) error.value = err instanceof Error ? err.message : t('federation.deferred.loadFailed')
    } finally {
      if (request === latest) loading.value = false
    }
  },
  { immediate: true },
)
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <SectionActions>
        <div class="flex items-center gap-2">
          <AppButton size="sm" @click="openCreate">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.webhooks.add') }}
          </AppButton>
        </div>
      </SectionActions>

    <!-- Stat tiles (derived from the loaded list only) -->
    <div class="grid grid-cols-2 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-primary bg-primary-soft">
          <BellAlertIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-micro font-semibold text-muted">{{ t('adm.webhooks.stat.active') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ activeCount }}</div>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-muted bg-surface-hover">
          <PaperAirplaneIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-micro font-semibold text-muted">{{ t('adm.webhooks.stat.disabled') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ webhooks.length - activeCount }}</div>
        </div>
      </div>
    </div>

    <!-- Table card -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h2 class="text-sm font-semibold tracking-tight">{{ t('adm.webhooks.listTitle') }}</h2>
        <span class="text-caption font-mono text-muted">{{ webhooks.length }} {{ t('adm.common.total') }}</span>
      </div>

      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>

      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>

      <div v-else-if="webhooks.length === 0" class="bg-surface rounded-xl border-0">
        <EmptyState
          :title="t('adm.webhooks.empty')"
          :description="t('adm.webhooks.emptyHint')"
          :docs-url="docsUrl"
          :docs-label="t('docs.webhooks')"
        />
      </div>

      <TableScroller v-else>
        <table class="w-full text-sm">
          <thead>
            <tr class="text-caption font-semibold text-muted bg-surface-hover/30">
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.name') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.url') }}</th>
              <th scope="col" data-drop-order="3" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.events') }}</th>
              <th scope="col" data-drop-order="2" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.format') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.status') }}</th>
              <th scope="col" data-drop-order="1" class="text-left px-4 py-2.5 font-medium">{{ t('adm.webhooks.column.created') }}</th>
              <th scope="col" class="text-right px-4 py-2.5 font-medium" :class="ACTIONS_HEAD">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="hook in webhooks" :key="hook.id" class="group/row hover:bg-surface-hover/40 transition-colors">
              <td class="px-4 py-3" :class="GROW_CELL">
                <span class="block truncate font-mono text-ui font-medium" :title="hook.name">{{ hook.name || '—' }}</span>
              </td>
              <td class="px-4 py-3 max-w-52">
                <span class="text-xs text-muted font-mono truncate block" :title="hook.url">{{ hook.url || '—' }}</span>
              </td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-1.5 flex-wrap">
                  <span
                    v-for="event in hook.events || []"
                    :key="event"
                    class="inline-flex items-center px-2 py-0.5 rounded-full text-micro font-medium bg-surface-hover text-muted border border-border"
                  >
                    {{ event }}
                  </span>
                  <span v-if="!hook.events?.length" class="text-xs text-muted">—</span>
                </div>
              </td>
              <td class="px-4 py-3 text-xs text-muted">{{ formatLabel(hook.payload_format) }}</td>
              <td class="px-4 py-3">
                <span
                  v-if="hook.enabled"
                  class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium bg-success-soft/40 text-success"
                >
                  <span class="w-1.5 h-1.5 rounded-full bg-success" />
                  {{ t('adm.webhooks.statusActive') }}
                </span>
                <span v-else class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium bg-danger-soft/40 text-danger">
                  <span class="w-1.5 h-1.5 rounded-full bg-danger" />
                  {{ t('adm.webhooks.statusInactive') }}
                </span>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDate(hook.created_at) }}</td>
              <td class="px-4 py-3" :class="ACTIONS_CELL">
                <RowActions :actions="hookActions" :subject="hook.name || hook.url || ''" @action="deleteTarget = hook" />
              </td>
            </tr>
          </tbody>
        </table>
      </TableScroller>
    </div>

    <WebhookCreateDialog :open="drawerOpen" @close="drawerOpen = false" @created="onCreated" />

    <ConfirmDialog
      :open="!!deleteTarget"
      :title="t('adm.webhooks.confirmDelete')"
      :description="deleteTarget ? deleteTarget.name || deleteTarget.url : ''"
      :confirm-label="t('common.delete')"
      :cancel-label="t('common.cancel')"
      :busy="deleting"
      @confirm="deleteOne"
      @close="deleteTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import SectionActions from '../../../components/SectionActions.vue'
import { formatDate } from '../../../lib/datetime'
import { ref, computed, onMounted } from 'vue'
import { deleteWebhook, listWebhooks, type Webhook } from '../../../api/webhooks'
import { useDocsLink } from '../../../composables/useDocsLink'
import { useToastStore } from '../../../stores/toast'
import { t, tDynamic } from '../../../i18n'
import {
  PlusIcon,
  BellAlertIcon,
  PaperAirplaneIcon,
} from '@heroicons/vue/24/outline'
import { TrashIcon } from '@heroicons/vue/20/solid'
import EmptyState from '../../../components/EmptyState.vue'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import WebhookCreateDialog from './WebhookCreateDialog.vue'
import RowActions, { type RowAction } from '../../../components/ui/RowActions.vue'
import TableScroller from '../../../components/ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from '../../../components/ui/tableStyles'

const toasts = useToastStore()
const docsUrl = useDocsLink('settings-webhooks')

const webhooks = ref<Webhook[]>([])
const loading = ref(false)
const loadError = ref('')
const hookActions: RowAction[] = [{ key: 'delete', label: t('common.delete'), icon: TrashIcon, tone: 'danger' }]

const drawerOpen = ref(false)
const deleteTarget = ref<Webhook | null>(null)
const deleting = ref(false)

const activeCount = computed(() => webhooks.value.filter((h) => h.enabled).length)

function formatLabel(format?: string): string {
  return tDynamic(`adm.webhooks.format.${format || 'openartifact'}`)
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    webhooks.value = await listWebhooks()
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.webhooks.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  drawerOpen.value = true
}

async function onCreated(): Promise<void> {
  drawerOpen.value = false
  await load()
}

async function deleteOne(): Promise<void> {
  const hook = deleteTarget.value
  if (!hook?.id) return
  deleting.value = true
  try {
    await deleteWebhook(hook.id)
    toasts.success(t('adm.webhooks.deleted'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.webhooks.deleteFailed'))
  } finally {
    deleting.value = false
    deleteTarget.value = null
  }
}

onMounted(load)
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Security > VEX feeds: vendor CSAF providers whose not-affected and fixed statements become VEX decisions. -->
<template>
  <div class="space-y-5">
    <SectionActions>
      <AppButton size="sm" data-testid="vex-feed-add" @click="openDialog(null)">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('sx.vexFeeds.add') }}
      </AppButton>
    </SectionActions>

    <p class="text-sm text-muted max-w-3xl">{{ t('sx.vexFeeds.intro') }}</p>

    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h2 class="text-sm font-semibold tracking-tight">{{ t('sx.vexFeeds.listTitle') }}</h2>
        <span class="text-caption font-mono text-muted">{{ feeds.length }} {{ t('adm.common.total') }}</span>
      </div>

      <div v-if="loading" class="px-4 py-8 text-center text-muted" role="status">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center" role="alert">
        <p class="text-sm text-danger">{{ loadError }}</p>
        <AppButton variant="secondary" size="sm" @click="load">{{ t('security.common.retry') }}</AppButton>
      </div>
      <EmptyState
        v-else-if="feeds.length === 0"
        :title="t('sx.vexFeeds.empty')"
        :description="t('sx.vexFeeds.emptyHint')"
        :docs-url="docsUrl"
        :docs-label="t('sx.vexFeeds.docs')"
      />

      <TableScroller v-else>
        <table class="w-full text-sm" data-testid="vex-feeds">
          <thead>
            <tr class="text-caption font-semibold text-muted bg-surface-hover/30">
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('sx.vexFeeds.col.feed') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('sx.vexFeeds.col.status') }}</th>
              <th scope="col" data-drop-order="3" class="text-left px-4 py-2.5 font-medium">{{ t('sx.vexFeeds.col.lastSync') }}</th>
              <th scope="col" data-drop-order="1" class="text-right px-4 py-2.5 font-medium">{{ t('sx.vexFeeds.col.documents') }}</th>
              <th scope="col" data-drop-order="2" class="text-right px-4 py-2.5 font-medium">{{ t('sx.vexFeeds.col.statements') }}</th>
              <th scope="col" class="text-right px-4 py-2.5 font-medium" :class="ACTIONS_HEAD">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="feed in feeds"
              :key="feed.id"
              class="group/row hover:bg-surface-hover/40 transition-colors align-top border-t border-border"
              data-testid="vex-feed-row"
            >
              <td class="px-4 py-3" :class="GROW_CELL">
                <span class="block truncate font-medium text-foreground" :title="feed.name">{{ feed.name }}</span>
                <span class="block truncate text-xs font-mono text-muted" :title="feed.url">{{ feed.url }}</span>
              </td>
              <td class="px-4 py-3 text-xs max-w-xs">
                <StatusBadge :label="statusLabel(feed)" :variant="statusVariant(feed)" />
                <p v-if="feed.last_error" class="mt-1 text-danger break-words line-clamp-3" :title="feed.last_error" data-testid="vex-feed-error">
                  {{ feed.last_error }}
                </p>
              </td>
              <td class="px-4 py-3 text-xs text-muted">
                <p>{{ feed.last_success_at ? tp('sx.vexFeeds.lastSuccess', { when: formatDateTime(feed.last_success_at) }) : t('sx.vexFeeds.neverSynced') }}</p>
                <p v-if="feed.last_attempt_at && feed.last_attempt_at !== feed.last_success_at">
                  {{ tp('sx.vexFeeds.lastAttempt', { when: formatDateTime(feed.last_attempt_at) }) }}
                </p>
                <p>{{ tp('sx.vexFeeds.interval', { n: String(feed.interval_hours) }) }}</p>
              </td>
              <td class="px-4 py-3 text-xs text-right tabular-nums">
                <p class="text-foreground">{{ formatNumber(feed.documents_total) }}</p>
                <p class="text-muted">{{ tp('sx.vexFeeds.rejected', { n: formatNumber(feed.rejected_total) }) }}</p>
              </td>
              <td class="px-4 py-3 text-xs text-right tabular-nums">
                <p class="text-foreground">{{ formatNumber(feed.statements) }}</p>
                <p class="text-muted">{{ tp('sx.vexFeeds.applied', { n: formatNumber(feed.applied) }) }}</p>
              </td>
              <td class="px-4 py-3" :class="ACTIONS_CELL">
                <RowActions :actions="actionsOf(feed)" :subject="feed.name" @action="(key) => onAction(key, feed)" />
              </td>
            </tr>
          </tbody>
        </table>
      </TableScroller>
    </div>

    <VexFeedDialog :open="dialogOpen" :feed="editing" @close="dialogOpen = false" @saved="onSaved" />

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('sx.vexFeeds.deleteTitle')"
      :description="deleteTarget?.name ?? ''"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="remove"
      @close="deleteTarget = null"
    >
      {{ t('sx.vexFeeds.deleteBody') }}
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { PlusIcon } from '@heroicons/vue/24/outline'
import { ArrowPathIcon, PencilSquareIcon, TrashIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import EmptyState from '../../../components/EmptyState.vue'
import SectionActions from '../../../components/SectionActions.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import RowActions, { type RowAction } from '../../../components/ui/RowActions.vue'
import TableScroller from '../../../components/ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from '../../../components/ui/tableStyles'
import VexFeedDialog from './VexFeedDialog.vue'
import { ApiError } from '../../../api/client'
import { deleteVexFeed, listVexFeeds, syncVexFeed, type VexFeed } from '../../../api/vexFeeds'
import { useDocsLink } from '../../../composables/useDocsLink'
import { formatDateTime } from '../../../lib/datetime'
import { formatNumber } from '../../../lib/intl'
import { useToastStore } from '../../../stores/toast'
import { t, tp } from '../../../i18n'

const toasts = useToastStore()
const docsUrl = useDocsLink('settings-vex-feeds')

const feeds = ref<VexFeed[]>([])
const loading = ref(false)
const loadError = ref('')
const dialogOpen = ref(false)
const editing = ref<VexFeed | null>(null)
const deleteTarget = ref<VexFeed | null>(null)
const deleting = ref(false)
const syncing = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    feeds.value = await listVexFeeds()
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('sx.vexFeeds.loadFailed')
  } finally {
    loading.value = false
  }
}

function statusLabel(feed: VexFeed): string {
  if (!feed.enabled) return t('sx.vexFeeds.status.disabled')
  return feed.last_error ? t('sx.vexFeeds.status.error') : t('sx.vexFeeds.status.enabled')
}

function statusVariant(feed: VexFeed): 'neutral' | 'danger' | 'success' {
  if (!feed.enabled) return 'neutral'
  return feed.last_error ? 'danger' : 'success'
}

function actionsOf(feed: VexFeed): RowAction[] {
  const busy = syncing.value === feed.id
  return [
    { key: 'sync', label: t('sx.vexFeeds.syncNow'), icon: ArrowPathIcon, loading: busy, disabled: busy },
    { key: 'edit', label: t('common.edit'), icon: PencilSquareIcon },
    { key: 'delete', label: t('common.delete'), icon: TrashIcon, tone: 'danger' },
  ]
}

function openDialog(feed: VexFeed | null): void {
  editing.value = feed
  dialogOpen.value = true
}

function onAction(key: string, feed: VexFeed): void {
  if (key === 'sync') void sync(feed)
  else if (key === 'edit') openDialog(feed)
  else if (key === 'delete') deleteTarget.value = feed
}

async function sync(feed: VexFeed): Promise<void> {
  syncing.value = feed.id
  try {
    await syncVexFeed(feed.id)
    toasts.success(tp('sx.vexFeeds.syncStarted', { name: feed.name }))
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) toasts.info(tp('sx.vexFeeds.syncRunning', { name: feed.name }))
    else toasts.error(err instanceof Error ? err.message : t('sx.vexFeeds.syncFailed'))
  } finally {
    syncing.value = null
  }
}

async function onSaved(): Promise<void> {
  dialogOpen.value = false
  await load()
}

async function remove(): Promise<void> {
  const feed = deleteTarget.value
  if (!feed) return
  deleting.value = true
  try {
    await deleteVexFeed(feed.id)
    toasts.success(tp('sx.vexFeeds.deleted', { name: feed.name }))
    deleteTarget.value = null
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sx.vexFeeds.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

onMounted(load)
</script>

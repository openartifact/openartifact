<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Health table of the federation peers of one repository. Presentational: every action is an event. -->
<template>
  <TableScroller>
    <table class="w-full" :aria-label="t('federation.peers.table')" data-testid="federation-peers">
      <thead>
        <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
          <th scope="col" class="text-left px-4 py-2.5">{{ t('federation.peers.column.name') }}</th>
          <th class="text-left px-4 py-2.5">{{ t('federation.peers.column.state') }}</th>
          <th class="text-left px-4 py-2.5">{{ t('federation.peers.column.lag') }}</th>
          <th data-drop-order="1" class="text-left px-4 py-2.5">{{ t('federation.peers.column.lastSuccess') }}</th>
          <th data-drop-order="3" class="text-left px-4 py-2.5">{{ t('federation.peers.column.lastError') }}</th>
          <th data-drop-order="2" class="text-right px-4 py-2.5">{{ t('federation.peers.column.counts') }}</th>
          <th class="text-right px-4 py-2.5">{{ t('federation.peers.column.deferred') }}</th>
          <th class="text-right px-4 py-2.5" :class="ACTIONS_HEAD"><span class="sr-only">{{ t('common.actions') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="peer in peers"
          :key="peer.id"
          class="group/row border-t border-border align-top hover:bg-surface-hover transition-colors"
          :data-testid="`federation-peer-${peer.name}`"
        >
          <td class="px-4 py-3" :class="GROW_CELL">
            <p class="truncate text-sm font-medium text-foreground" :title="peer.name">{{ peer.name }}</p>
            <p class="truncate text-xs font-mono text-muted" :title="peer.base_url">{{ peer.base_url }}</p>
          </td>
          <td class="px-4 py-3">
            <StatusBadge :label="peerStateLabel(peer.state)" :variant="peerStateVariant(peer.state)" />
            <p v-if="peer.resync_requested" class="mt-1 text-xs text-muted">{{ t('federation.peers.resyncPending') }}</p>
          </td>
          <td class="px-4 py-3 text-xs whitespace-nowrap">
            <p class="text-foreground">{{ formatLag(peer.lag_seconds) }}</p>
            <p class="text-muted tabular-nums">{{ fill(t('federation.peers.backlog'), { n: peer.backlog_units }) }}</p>
          </td>
          <td class="px-4 py-3 text-xs text-muted whitespace-nowrap">
            {{ peer.last_success_at ? formatDateTime(peer.last_success_at) : t('federation.peers.never') }}
          </td>
          <td class="px-4 py-3 text-xs max-w-xs">
            <template v-if="peer.last_error">
              <p class="text-danger break-words whitespace-pre-wrap">
                {{ expanded.has(peer.id) ? peer.last_error : previewError(peer.last_error).text }}
              </p>
              <button
                v-if="previewError(peer.last_error).truncated"
                type="button"
                class="mt-0.5 text-primary hover:underline"
                :aria-expanded="expanded.has(peer.id)"
                @click="toggleExpanded(peer.id)"
              >
                {{ expanded.has(peer.id) ? t('federation.peers.showLess') : t('federation.peers.showMore') }}
              </button>
              <p class="mt-0.5 text-muted">{{ fill(t('federation.peers.errorCount'), { n: peer.error_count }) }}</p>
            </template>
            <span v-else class="text-muted">{{ t('federation.peers.noError') }}</span>
          </td>
          <td class="px-4 py-3 text-xs font-mono tabular-nums text-muted text-right">
            {{ fill(t('federation.peers.appliedConflicts'), { applied: peer.applied_total, conflicts: peer.conflicts_total }) }}
          </td>
          <td class="px-4 py-3 text-xs text-right">
            <button
              v-if="peer.deferred_units > 0"
              type="button"
              class="text-primary hover:underline tabular-nums"
              @click="emit('deferred', peer)"
            >
              {{ fill(t('federation.peers.deferredView'), { n: peer.deferred_units }) }}
            </button>
            <span v-else class="text-muted tabular-nums">{{ t('federation.peers.deferredNone') }}</span>
          </td>
          <td class="px-4 py-3" :class="ACTIONS_CELL">
            <RowActions :actions="actionsOf(peer)" @action="(key) => onAction(key, peer)">
              <AppToggle
                :model-value="peer.enabled"
                :label="fill(t('federation.peers.toggle'), { name: peer.name })"
                :disabled="busy.has(peer.id)"
                @update:model-value="(v: boolean) => emit('toggle', peer, v)"
              />
            </RowActions>
          </td>
        </tr>
      </tbody>
    </table>
  </TableScroller>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ArrowPathIcon, PencilSquareIcon, TrashIcon } from '@heroicons/vue/20/solid'
import AppToggle from './AppToggle.vue'
import StatusBadge from './StatusBadge.vue'
import RowActions, { type RowAction } from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import type { FederationPeer } from '../api/federation'
import { formatDateTime } from '../lib/datetime'
import { fill, formatLag, peerStateLabel, peerStateVariant, previewError, resyncPending } from '../lib/federation'
import { t } from '../i18n'

const props = defineProps<{
  peers: readonly FederationPeer[]
  /** Peer ids with a request in flight. */
  busy: ReadonlySet<string>
}>()
const emit = defineEmits<{
  edit: [peer: FederationPeer]
  delete: [peer: FederationPeer]
  resync: [peer: FederationPeer]
  deferred: [peer: FederationPeer]
  toggle: [peer: FederationPeer, enabled: boolean]
}>()

// Peers whose full last error is open.
const expanded = ref<Set<string>>(new Set())

function toggleExpanded(id: string): void {
  const next = new Set(expanded.value)
  if (!next.delete(id)) next.add(id)
  expanded.value = next
}

function actionsOf(peer: FederationPeer): RowAction[] {
  const pending = resyncPending(peer, props.busy.has(peer.id))
  return [
    { key: 'resync', label: fill(t('federation.peers.resyncAria'), { name: peer.name }), icon: ArrowPathIcon, loading: pending, disabled: pending },
    { key: 'edit', label: fill(t('federation.peers.edit'), { name: peer.name }), icon: PencilSquareIcon },
    { key: 'delete', label: fill(t('federation.peers.delete'), { name: peer.name }), icon: TrashIcon, tone: 'danger' },
  ]
}

function onAction(key: string, peer: FederationPeer): void {
  if (key === 'resync') emit('resync', peer)
  else if (key === 'edit') emit('edit', peer)
  else if (key === 'delete') emit('delete', peer)
}
</script>

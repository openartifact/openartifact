<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-6">
    <!-- Enable / disable -->
    <section class="bg-surface rounded-xl border border-border p-5 space-y-4">
      <div class="flex items-start justify-between gap-4">
        <div class="min-w-0">
          <h2 id="federation-heading" class="text-sm font-semibold text-foreground">{{ t('federation.title') }}</h2>
          <p class="text-xs text-muted mt-1 max-w-2xl">{{ t('federation.description') }}</p>
        </div>
        <AppToggle
          v-if="status"
          size="md"
          :model-value="status.enabled"
          :label="t('federation.enable')"
          :disabled="!status.supported || switching"
          data-testid="federation-toggle"
          @update:model-value="onToggle"
        />
      </div>

      <div v-if="loading && !status" class="space-y-2" aria-busy="true">
        <div class="h-4 w-48 rounded skel" />
        <div class="h-4 w-full rounded skel" />
      </div>
      <p v-else-if="error && !status" role="alert" class="text-sm text-danger">{{ error }}</p>
      <template v-else-if="status">
        <p v-if="!status.supported" class="rounded-lg border border-border bg-surface-hover/40 p-3 text-xs text-muted" data-testid="federation-unsupported">
          <span class="block text-sm font-medium text-foreground">{{ t('federation.unsupported.title') }}</span>
          {{ t('federation.unsupported.hint') }}
        </p>
        <p v-else-if="!status.enabled" class="text-xs text-muted">{{ t('federation.enableHint') }}</p>

        <dl v-if="status.enabled" class="grid grid-cols-2 sm:grid-cols-5 gap-4 text-xs" data-testid="federation-stats">
          <div class="min-w-0">
            <dt class="text-muted">{{ t('federation.stats.instance') }}</dt>
            <dd class="font-mono text-foreground truncate" :title="status.instance_id">{{ status.instance_id }}</dd>
          </div>
          <div class="min-w-0">
            <dt class="text-muted">{{ t('federation.stats.log') }}</dt>
            <dd class="font-mono text-foreground truncate" :title="status.log_id">{{ status.log_id }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('federation.stats.head') }}</dt>
            <dd class="font-mono tabular-nums text-foreground">{{ status.head_seq }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('federation.stats.units') }}</dt>
            <dd class="font-mono tabular-nums text-foreground">{{ status.units }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('federation.stats.tombstones') }}</dt>
            <dd class="font-mono tabular-nums text-foreground">{{ status.tombstones }}</dd>
          </div>
        </dl>
        <p v-if="error" role="alert" class="text-xs text-danger">{{ error }}</p>
      </template>
    </section>

    <template v-if="status?.enabled">
      <!-- Peers -->
      <section class="bg-surface rounded-xl border border-border overflow-hidden" aria-labelledby="federation-peers-heading">
        <div class="px-5 py-4 border-b border-border flex items-start justify-between gap-3">
          <div>
            <h3 id="federation-peers-heading" class="text-sm font-semibold text-foreground">{{ t('federation.peers.title') }}</h3>
            <p class="text-xs text-muted mt-0.5">{{ t('federation.peers.hint') }}</p>
          </div>
          <AppButton size="sm" @click="openAdd">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('federation.peers.add') }}
          </AppButton>
        </div>
        <EmptyState v-if="status.peers.length === 0" :title="t('federation.peers.empty')" :description="t('federation.peers.emptyHint')" />
        <FederationPeersTable
          v-else
          :peers="status.peers"
          :busy="busy"
          @edit="openEdit"
          @delete="deleting = $event"
          @resync="resyncing = $event"
          @deferred="deferredPeer = $event"
          @toggle="onTogglePeer"
        />
      </section>

      <!-- Inbound tokens -->
      <section class="bg-surface rounded-xl border border-border overflow-hidden" aria-labelledby="federation-tokens-heading">
        <div class="px-5 py-4 border-b border-border">
          <h3 id="federation-tokens-heading" class="text-sm font-semibold text-foreground">{{ t('federation.tokens.title') }}</h3>
          <p class="text-xs text-muted mt-0.5">{{ t('federation.tokens.hint') }}</p>
        </div>
        <form class="px-5 py-4 flex flex-wrap items-end gap-3 border-b border-border" @submit.prevent="createToken">
          <div class="flex-1 min-w-48">
            <label for="fed-token-name" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('federation.tokens.name') }}</label>
            <input
              id="fed-token-name"
              v-model="tokenName"
              type="text"
              autocomplete="off"
              :placeholder="t('federation.tokens.namePlaceholder')"
              :class="INPUT_CLASS"
            />
          </div>
          <AppButton type="submit" :disabled="tokenName.trim() === ''" :loading="creatingToken">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('federation.tokens.create') }}
          </AppButton>
        </form>
        <p v-if="status.tokens.length === 0" class="px-5 py-6 text-sm text-muted text-center">{{ t('federation.tokens.empty') }}</p>
        <div v-else class="overflow-x-auto">
          <table class="w-full" :aria-label="t('federation.tokens.table')" data-testid="federation-tokens">
            <thead>
              <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
                <th class="text-left px-4 py-2.5">{{ t('federation.tokens.column.name') }}</th>
                <th class="text-left px-4 py-2.5">{{ t('federation.tokens.column.created') }}</th>
                <th class="text-left px-4 py-2.5">{{ t('federation.tokens.column.lastUsed') }}</th>
                <th class="text-left px-4 py-2.5">{{ t('federation.tokens.column.status') }}</th>
                <th class="text-right px-4 py-2.5"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="tok in status.tokens" :key="tok.id" class="border-t border-border hover:bg-surface-hover transition-colors">
                <td class="px-4 py-2.5 text-sm font-medium text-foreground">{{ tok.name }}</td>
                <td class="px-4 py-2.5 text-xs text-muted whitespace-nowrap">{{ formatDateTime(tok.created_at) }}</td>
                <td class="px-4 py-2.5 text-xs text-muted whitespace-nowrap">
                  {{ tok.last_used_at ? formatDateTime(tok.last_used_at) : t('federation.tokens.neverUsed') }}
                </td>
                <td class="px-4 py-2.5">
                  <StatusBadge
                    :label="tok.revoked_at ? t('federation.tokens.revoked') : t('federation.tokens.active')"
                    :variant="tok.revoked_at ? 'neutral' : 'success'"
                  />
                </td>
                <td class="px-4 py-2.5 text-right">
                  <AppButton
                    v-if="!tok.revoked_at"
                    variant="danger-outline"
                    size="xs"
                    :aria-label="fill(t('federation.tokens.revokeAria'), { name: tok.name })"
                    @click="revoking = tok"
                  >
                    {{ t('federation.tokens.revoke') }}
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <FederationPeerDialog :open="peerDialogOpen" :peer="editing" :busy="savingPeer" @close="peerDialogOpen = false" @submit="savePeer" />
    <FederationDeferredDialog :repo-name="repo.name" :peer="deferredPeer" @close="deferredPeer = null" />
    <TokenSecretDialog :title="t('federation.tokens.secretTitle')" :token="secret" @close="secret = null" />

    <ConfirmDialog
      :open="confirmDisable"
      :title="t('federation.disableTitle')"
      :description="t('federation.disableBody')"
      :confirm-label="t('federation.disableConfirm')"
      :busy="switching"
      @confirm="disable"
      @close="confirmDisable = false"
    />
    <ConfirmDialog
      :open="resyncing !== null"
      :title="fill(t('federation.peers.resyncTitle'), { name: resyncing?.name ?? '' })"
      :description="t('federation.peers.resyncBody')"
      :confirm-label="t('federation.peers.resyncConfirm')"
      tone="primary"
      :busy="resyncBusy"
      @confirm="resync"
      @close="resyncing = null"
    />
    <ConfirmDialog
      :open="deleting !== null"
      :title="fill(t('federation.peers.deleteTitle'), { name: deleting?.name ?? '' })"
      :description="t('federation.peers.deleteBody')"
      :confirm-label="t('common.delete')"
      :busy="deleteBusy"
      @confirm="removePeer"
      @close="deleting = null"
    />
    <ConfirmDialog
      :open="revoking !== null"
      :title="fill(t('federation.tokens.revokeTitle'), { name: revoking?.name ?? '' })"
      :description="t('federation.tokens.revokeBody')"
      :confirm-label="t('federation.tokens.revokeConfirm')"
      :busy="revokeBusy"
      @confirm="revoke"
      @close="revoking = null"
    />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { PlusIcon } from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import AppToggle from '../components/AppToggle.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import FederationDeferredDialog from '../components/FederationDeferredDialog.vue'
import FederationPeerDialog from '../components/FederationPeerDialog.vue'
import FederationPeersTable from '../components/FederationPeersTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import TokenSecretDialog from '../components/TokenSecretDialog.vue'
import {
  createFederationPeer,
  createFederationToken,
  deleteFederationPeer,
  resyncFederationPeer,
  revokeFederationToken,
  setFederationEnabled,
  updateFederationPeer,
  type FederationPeer,
  type FederationToken,
} from '../api/federation'
import type { Repository } from '../api/repositories'
import { useFederationStatus } from '../composables/useFederationStatus'
import { formatDateTime } from '../lib/datetime'
import { fill, peerCreateBody, peerUpdateBody, upsertPeer, type PeerForm } from '../lib/federation'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ repo: Repository }>()
const emit = defineEmits<{ toggled: [enabled: boolean] }>()

const toasts = useToastStore()
const { status, loading, error, reload, apply } = useFederationStatus(() => props.repo.name)

function failure(title: string, err: unknown): void {
  toasts.error(title, err instanceof Error ? err.message : undefined)
}

// ─── Enable / disable ──────────────────────────────────────────────────────────

const switching = ref(false)
const confirmDisable = ref(false)

async function setEnabled(enabled: boolean): Promise<void> {
  switching.value = true
  try {
    apply(await setFederationEnabled(props.repo.name, enabled))
    confirmDisable.value = false
    emit('toggled', enabled)
    toasts.success(enabled ? t('federation.enabled') : t('federation.disabled'))
  } catch (err) {
    failure(t('federation.toggleFailed'), err)
  } finally {
    switching.value = false
  }
}

function onToggle(enabled: boolean): void {
  if (enabled) void setEnabled(true)
  else confirmDisable.value = true
}

function disable(): Promise<void> {
  return setEnabled(false)
}

// ─── Peers ─────────────────────────────────────────────────────────────────────

const busy = ref<Set<string>>(new Set())
const peerDialogOpen = ref(false)
const editing = ref<FederationPeer | null>(null)
const savingPeer = ref(false)
const deferredPeer = ref<FederationPeer | null>(null)
const resyncing = ref<FederationPeer | null>(null)
const resyncBusy = ref(false)
const deleting = ref<FederationPeer | null>(null)
const deleteBusy = ref(false)

function setBusy(id: string, on: boolean): void {
  const next = new Set(busy.value)
  if (on) next.add(id)
  else next.delete(id)
  busy.value = next
}

/** Shows `peer` in the table at once; the next status read confirms it. */
function showPeer(peer: FederationPeer): void {
  if (status.value) apply({ ...status.value, peers: upsertPeer(status.value.peers, peer) })
}

function openAdd(): void {
  editing.value = null
  peerDialogOpen.value = true
}

function openEdit(peer: FederationPeer): void {
  editing.value = peer
  peerDialogOpen.value = true
}

async function savePeer(form: PeerForm): Promise<void> {
  const peer = editing.value
  savingPeer.value = true
  try {
    if (peer) {
      showPeer(await updateFederationPeer(props.repo.name, peer.id, peerUpdateBody(form, peer)))
      toasts.success(fill(t('federation.peerDialog.updated'), { name: form.name.trim() }))
    } else {
      showPeer(await createFederationPeer(props.repo.name, peerCreateBody(form)))
      toasts.success(fill(t('federation.peerDialog.created'), { name: form.name.trim() }))
    }
    peerDialogOpen.value = false
    void reload()
  } catch (err) {
    failure(peer ? t('federation.peers.updateFailed') : t('federation.peerDialog.createFailed'), err)
  } finally {
    savingPeer.value = false
  }
}

async function onTogglePeer(peer: FederationPeer, enabled: boolean): Promise<void> {
  setBusy(peer.id, true)
  try {
    showPeer(await updateFederationPeer(props.repo.name, peer.id, { enabled }))
    void reload()
  } catch (err) {
    failure(t('federation.peers.updateFailed'), err)
  } finally {
    setBusy(peer.id, false)
  }
}

async function resync(): Promise<void> {
  const peer = resyncing.value
  if (!peer) return
  resyncBusy.value = true
  setBusy(peer.id, true)
  try {
    showPeer(await resyncFederationPeer(props.repo.name, peer.id))
    toasts.success(fill(t('federation.peers.resyncQueued'), { name: peer.name }))
    resyncing.value = null
    void reload()
  } catch (err) {
    failure(t('federation.peers.resyncFailed'), err)
  } finally {
    resyncBusy.value = false
    setBusy(peer.id, false)
  }
}

async function removePeer(): Promise<void> {
  const peer = deleting.value
  if (!peer) return
  deleteBusy.value = true
  try {
    await deleteFederationPeer(props.repo.name, peer.id)
    if (status.value) apply({ ...status.value, peers: status.value.peers.filter((p) => p.id !== peer.id) })
    toasts.success(fill(t('federation.peers.deleted'), { name: peer.name }))
    deleting.value = null
    void reload()
  } catch (err) {
    failure(t('federation.peers.deleteFailed'), err)
  } finally {
    deleteBusy.value = false
  }
}

// ─── Inbound tokens ────────────────────────────────────────────────────────────

const tokenName = ref('')
const creatingToken = ref(false)
// The clear-text token of the one just created; it is never fetched again.
const secret = ref<string | null>(null)
const revoking = ref<FederationToken | null>(null)
const revokeBusy = ref(false)

async function createToken(): Promise<void> {
  const name = tokenName.value.trim()
  if (name === '') return
  creatingToken.value = true
  try {
    const created = await createFederationToken(props.repo.name, name)
    secret.value = created.token
    tokenName.value = ''
    toasts.success(fill(t('federation.tokens.created'), { name }))
    void reload()
  } catch (err) {
    failure(t('federation.tokens.createFailed'), err)
  } finally {
    creatingToken.value = false
  }
}

async function revoke(): Promise<void> {
  const tok = revoking.value
  if (!tok) return
  revokeBusy.value = true
  try {
    await revokeFederationToken(props.repo.name, tok.id)
    toasts.success(fill(t('federation.tokens.revoked.toast'), { name: tok.name }))
    revoking.value = null
    void reload()
  } catch (err) {
    failure(t('federation.tokens.revokeFailed'), err)
  } finally {
    revokeBusy.value = false
  }
}
</script>

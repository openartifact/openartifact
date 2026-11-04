<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Inbound federation tokens of a repository: create form, table, secret and revoke dialogs. -->
<template>
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
    <p v-if="tokens.length === 0" class="px-5 py-6 text-sm text-muted text-center">{{ t('federation.tokens.empty') }}</p>
    <TableScroller v-else class="relative">
      <table class="w-full" :aria-label="t('federation.tokens.table')" data-testid="federation-tokens">
        <thead>
          <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
            <th scope="col" class="text-left px-4 py-2.5">{{ t('federation.tokens.column.name') }}</th>
            <th scope="col" data-drop-order="1" class="text-left px-4 py-2.5">{{ t('federation.tokens.column.created') }}</th>
            <th scope="col" data-drop-order="2" class="text-left px-4 py-2.5">{{ t('federation.tokens.column.lastUsed') }}</th>
            <th scope="col" class="text-left px-4 py-2.5">{{ t('federation.tokens.column.status') }}</th>
            <th scope="col" class="text-right px-4 py-2.5" :class="ACTIONS_HEAD"><span class="sr-only">{{ t('common.actions') }}</span></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="tok in tokens" :key="tok.id" class="group/row border-t border-border hover:bg-surface-hover transition-colors">
            <td class="truncate px-4 py-2.5 text-sm font-medium text-foreground" :class="GROW_CELL" :title="tok.name">{{ tok.name }}</td>
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
            <td class="px-4 py-2.5" :class="ACTIONS_CELL">
              <RowActions :actions="tokenActions(tok)" :subject="tok.name" @action="revoking = tok" />
            </td>
          </tr>
        </tbody>
      </table>
    </TableScroller>

    <TokenSecretDialog :title="t('federation.tokens.secretTitle')" :token="secret" @close="secret = null" />
    <ConfirmDialog
      :open="revoking !== null"
      :title="fill(t('federation.tokens.revokeTitle'), { name: revoking?.name ?? '' })"
      :description="t('federation.tokens.revokeBody')"
      :confirm-label="t('federation.tokens.revokeConfirm')"
      :busy="revokeBusy"
      @confirm="revoke"
      @close="revoking = null"
    />
  </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { NoSymbolIcon, PlusIcon } from '@heroicons/vue/20/solid'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import StatusBadge from './StatusBadge.vue'
import TokenSecretDialog from './TokenSecretDialog.vue'
import RowActions, { type RowAction } from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import { createFederationToken, revokeFederationToken, type FederationToken } from '../api/federation'
import { formatDateTime } from '../lib/datetime'
import { fill } from '../lib/federation'
import { INPUT_CLASS } from '../lib/formClasses'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

const props = defineProps<{ repoName: string; tokens: FederationToken[] }>()
/** A token was created or revoked; the owner re-reads the status. */
const emit = defineEmits<{ changed: [] }>()

const toasts = useToastStore()

function failure(title: string, err: unknown): void {
  toasts.error(title, err instanceof Error ? err.message : undefined)
}

const tokenName = ref('')
const creatingToken = ref(false)
// The clear-text token of the one just created; it is never fetched again.
const secret = ref<string | null>(null)
const revoking = ref<FederationToken | null>(null)
const revokeBusy = ref(false)

function tokenActions(tok: FederationToken): RowAction[] {
  return [
    {
      key: 'revoke',
      label: t('federation.tokens.revoke'),
      icon: NoSymbolIcon,
      tone: 'danger',
      hidden: !!tok.revoked_at,
    },
  ]
}

async function createToken(): Promise<void> {
  const name = tokenName.value.trim()
  if (name === '') return
  creatingToken.value = true
  try {
    const created = await createFederationToken(props.repoName, name)
    secret.value = created.token
    tokenName.value = ''
    toasts.success(fill(t('federation.tokens.created'), { name }))
    emit('changed')
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
    await revokeFederationToken(props.repoName, tok.id)
    toasts.success(fill(t('federation.tokens.revoked.toast'), { name: tok.name }))
    revoking.value = null
    emit('changed')
  } catch (err) {
    failure(t('federation.tokens.revokeFailed'), err)
  } finally {
    revokeBusy.value = false
  }
}
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- API tokens of a service account: list, create (the secret is shown once) and revoke. -->
<template>
  <CardSection :title="t('sa.token.title')" :description="t('sa.token.desc')">
    <template #action>
      <AppButton size="sm" class="max-md:h-11" data-testid="account-token-new" @click="creating = true">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('tok.create.open') }}
      </AppButton>
    </template>

    <ListTable
      :columns="columns"
      :caption="t('sa.token.title')"
      :rows="tokens.data.value"
      :loading="tokens.loading.value"
      :error="tokens.error.value"
      :limit="100"
      :empty-title="t('tok.empty.title')"
      :empty-hint="t('sa.token.emptyHint')"
      @retry="tokens.reload"
    >
      <template #cell-name="{ row }"><span class="block truncate font-mono text-sm" :title="row.name">{{ row.name }}</span></template>
      <template #cell-kind="{ row }"><StatusBadge :label="row.kind === 'access' ? t('sa.token.access') : t('sa.token.deploy')" /></template>
      <template #cell-lastUsed="{ row }">
        <RelativeTime v-if="row.last_used_at" :value="row.last_used_at" />
        <span v-else class="text-muted">{{ t('tok.lastUsed.never') }}</span>
      </template>
      <template #cell-expires="{ row }">
        <time v-if="row.expires_at" :datetime="row.expires_at" :title="formatDateTime(row.expires_at)" :class="expiry(row).kind === 'ok' ? 'text-foreground' : expiry(row).kind === 'soon' ? 'font-medium text-warning' : 'font-medium text-danger'">
          {{ expiryText(expiry(row)) }}
        </time>
        <span v-else class="text-muted">{{ t('tok.expiry.never') }}</span>
      </template>
      <template #cell-actions="{ row }">
        <RowActions :actions="[{ key: 'revoke', label: t('tok.revoke.action'), icon: NoSymbolIcon, tone: 'danger' }]" :subject="row.name" @action="revoking = row" />
      </template>
    </ListTable>

    <AccountTokenDrawer :open="creating" :account-id="accountId" @close="creating = false" @created="onCreated" />
    <TokenSecretDialog :title="revealTitle" :token="secret" :hint="t('sa.token.revealHint')" @close="secret = null" />
    <ConfirmDialog
      :open="revoking !== null"
      :title="t('tok.revoke.title')"
      :description="revoking ? tp('sa.token.revokeDesc', { name: revoking.name }) : ''"
      :confirm-label="t('tok.revoke.action')"
      :busy="revokeBusy"
      @confirm="confirmRevoke"
      @close="revoking = null"
    />
  </CardSection>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { NoSymbolIcon, PlusIcon } from '@heroicons/vue/20/solid'
import { listUserTokens, revokeUserToken, type OwnedToken } from '../../api/tokens'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import StatusBadge from '../StatusBadge.vue'
import TokenSecretDialog from '../TokenSecretDialog.vue'
import CardSection from '../ui/CardSection.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import RowActions from '../ui/RowActions.vue'
import AccountTokenDrawer, { type AccountTokenSecret } from './AccountTokenDrawer.vue'
import { useLoadable } from '../../composables/useLoadable'
import { formatDateTime } from '../../lib/datetime'
import { expiryOf, expiryText } from '../../lib/tokens'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const props = defineProps<{ accountId: string }>()

const toasts = useToastStore()
const tokens = useLoadable(() => listUserTokens(props.accountId), [] as OwnedToken[])

const columns: ListColumn[] = [
  { key: 'name', label: t('tok.col.name'), grow: true },
  { key: 'expires', label: t('tok.col.expires'), dropOrder: 2 },
  { key: 'lastUsed', label: t('tok.col.lastUsed'), dropOrder: 1 },
  { key: 'kind', label: t('tok.create.kind'), dropOrder: 3 },
  { key: 'actions', label: t('tok.col.actions'), actions: true },
]

const expiry = (token: OwnedToken) => expiryOf({ expires_at: token.expires_at ?? undefined, expired: false })

const creating = ref(false)
const secret = ref<string | null>(null)
const revealTitle = ref('')

function onCreated(created: AccountTokenSecret): void {
  creating.value = false
  revealTitle.value = tp('tok.reveal.title', { name: created.name })
  secret.value = created.secret
  void tokens.reload()
}

const revoking = ref<OwnedToken | null>(null)
const revokeBusy = ref(false)

async function confirmRevoke(): Promise<void> {
  const token = revoking.value
  if (!token) return
  revokeBusy.value = true
  try {
    await revokeUserToken(props.accountId, token.id)
    toasts.success(tp('tok.revoke.done', { name: token.name }))
    revoking.value = null
    await tokens.reload()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tok.revoke.failed'))
  } finally {
    revokeBusy.value = false
  }
}
</script>

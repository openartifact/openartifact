<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  CI OIDC trust policies: every policy, or those of one service account (`accountId`), with the
  create / edit drawer and the delete confirmation.
-->
<template>
  <div class="space-y-4">
    <SectionActions>
      <AppButton size="sm" class="max-md:h-11" data-testid="policy-new" @click="openCreate">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('sa.policy.create') }}
      </AppButton>
    </SectionActions>

    <FilterBar
      v-if="!accountId"
      :search="list.text.q"
      :search-label="t('sa.policy.search')"
      :placeholder="t('sa.policy.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear()"
    />

    <ListTable :columns="columns" :caption="t('sa.policy.listTitle')" :query="list" :empty-title="t('sa.policy.empty')" :empty-hint="t('sa.policy.emptyHint')">
      <template #empty-action>
        <AppButton data-testid="policy-empty-create" @click="openCreate">{{ t('sa.policy.create') }}</AppButton>
      </template>
      <template #cell-name="{ row }"><span class="block truncate font-mono text-ui font-medium" :title="row.name">{{ row.name }}</span></template>
      <template #cell-issuer="{ row }"><span class="block max-w-64 truncate font-mono text-xs text-muted" :title="row.issuer">{{ row.issuer }}</span></template>
      <template #cell-audience="{ row }"><span class="block max-w-48 truncate font-mono text-xs text-muted" :title="row.audience">{{ row.audience }}</span></template>
      <template #cell-account="{ row }">
        <router-link :to="accountPath(row.service_account_id)" class="font-mono text-ui text-primary hover:underline">{{ row.service_account }}</router-link>
      </template>
      <template #cell-ttl="{ row }"><span class="tabular-nums">{{ tp('sa.policy.ttlValue', { minutes: String(Math.round(row.token_ttl_seconds / 60)) }) }}</span></template>
      <template #cell-enabled="{ row }">
        <StatusBadge :label="row.enabled ? t('sa.policy.statusEnabled') : t('sa.policy.statusDisabled')" :variant="row.enabled ? 'success' : 'neutral'" />
      </template>
      <template #cell-actions="{ row }">
        <RowActions :actions="rowActions(row)" :subject="row.name" @action="(key) => onAction(key, row)" />
      </template>
    </ListTable>

    <OidcPolicyDrawer :open="drawerOpen" :policy="editing" :accounts="accounts" :account-id="accountId" @close="closeDrawer" @saved="onSaved" />

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('sa.policy.confirmDelete')"
      :description="deleteTarget ? tp('sa.policy.confirmDeleteBody', { name: deleteTarget.name }) : ''"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { CheckIcon, NoSymbolIcon, PencilSquareIcon, PlusIcon, TrashIcon } from '@heroicons/vue/20/solid'
import { deleteOidcPolicy, listOidcPolicies, updateOidcPolicy, type OidcPolicy, type ServiceAccountItem } from '../../api/serviceAccounts'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import SectionActions from '../SectionActions.vue'
import StatusBadge from '../StatusBadge.vue'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RowActions, { type RowAction } from '../ui/RowActions.vue'
import OidcPolicyDrawer from './OidcPolicyDrawer.vue'
import { useLocalList } from '../../composables/useLocalList'
import { strParam } from '../../composables/useUrlState'
import { compareText } from '../../lib/intl'
import { accountPath } from '../../lib/serviceAccounts'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const props = defineProps<{
  /** Accounts a policy can map to. */
  accounts: ServiceAccountItem[]
  /** Only the policies of this account, and new ones start on it. */
  accountId?: string
}>()

const toasts = useToastStore()

const columns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('sa.policy.col.name'), sortable: true, grow: true },
  { key: 'issuer', label: t('sa.policy.col.issuer'), dropOrder: 2 },
  { key: 'audience', label: t('sa.policy.col.audience'), dropOrder: 3 },
  ...(props.accountId ? [] : [{ key: 'account', label: t('sa.policy.col.account'), dropOrder: 4 }]),
  { key: 'ttl', label: t('sa.policy.col.ttl'), dropOrder: 1 },
  { key: 'enabled', label: t('sa.policy.col.status') },
  { key: 'actions', label: t('common.actions'), actions: true },
])

const { list, refresh } = useLocalList({
  filters: { q: strParam() },
  debounce: ['q'],
  sorts: ['name'],
  limit: 25,
  load: async () => (await listOidcPolicies()).filter((p) => !props.accountId || p.service_account_id === props.accountId),
  match: (p, f) => {
    const q = f.q.trim().toLowerCase()
    return !q || [p.name, p.issuer, p.audience, p.service_account].some((v) => v?.toLowerCase().includes(q))
  },
  compare: (a, b) => compareText(a.name, b.name),
})

function rowActions(row: OidcPolicy): RowAction[] {
  return [
    { key: 'edit', label: t('common.edit'), icon: PencilSquareIcon },
    row.enabled
      ? { key: 'toggle', label: t('sa.policy.disable'), icon: NoSymbolIcon }
      : { key: 'toggle', label: t('sa.policy.enable'), icon: CheckIcon },
    { key: 'delete', label: t('common.delete'), icon: TrashIcon, tone: 'danger' },
  ]
}

function onAction(key: string, row: OidcPolicy): void {
  if (key === 'edit') openEdit(row)
  else if (key === 'toggle') void toggle(row)
  else if (key === 'delete') deleteTarget.value = row
}

const drawerOpen = ref(false)
const editing = ref<OidcPolicy | null>(null)

function openCreate(): void {
  editing.value = null
  drawerOpen.value = true
}
function openEdit(row: OidcPolicy): void {
  editing.value = row
  drawerOpen.value = true
}
function closeDrawer(): void {
  drawerOpen.value = false
}

async function onSaved(saved: OidcPolicy): Promise<void> {
  toasts.success(tp(editing.value ? 'sa.policy.updated' : 'sa.policy.created', { name: saved.name }))
  closeDrawer()
  await refresh()
}

async function toggle(row: OidcPolicy): Promise<void> {
  try {
    await updateOidcPolicy(row.id, { enabled: !row.enabled })
    toasts.success(tp(row.enabled ? 'sa.policy.disabledToast' : 'sa.policy.enabledToast', { name: row.name }))
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
  }
}

const deleteTarget = ref<OidcPolicy | null>(null)
const deleting = ref(false)

async function confirmDelete(): Promise<void> {
  const policy = deleteTarget.value
  if (!policy) return
  deleting.value = true
  try {
    await deleteOidcPolicy(policy.id)
    toasts.success(tp('sa.policy.deleted', { name: policy.name }))
    deleteTarget.value = null
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
  } finally {
    deleting.value = false
  }
}
</script>

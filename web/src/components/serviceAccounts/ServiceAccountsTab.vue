<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Service accounts: the list with search and status filter, create, disable / enable and delete. -->
<template>
  <div class="space-y-4">
    <SectionActions>
      <AppButton size="sm" class="max-md:h-11" data-testid="account-new" @click="newFlag.set(true)">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('sa.create.open') }}
      </AppButton>
    </SectionActions>

    <FilterBar
      :search="list.text.q"
      :search-label="t('sa.search')"
      :placeholder="t('sa.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear()"
    >
      <select
        :value="list.filters.value.status"
        :aria-label="t('sa.filter')"
        class="h-9 rounded-lg border border-border-strong bg-surface px-2 text-sm text-foreground max-md:h-11"
        @change="list.setFilter('status', ($event.target as HTMLSelectElement).value as StatusFilter)"
      >
        <option value="all">{{ t('sa.filter.all') }}</option>
        <option value="active">{{ t('sa.filter.active') }}</option>
        <option value="disabled">{{ t('sa.filter.disabled') }}</option>
      </select>
    </FilterBar>

    <ListTable :columns="columns" :caption="t('sa.listTitle')" :query="list" :empty-title="t('sa.empty')" :empty-hint="t('sa.emptyHint')">
      <template #empty-action>
        <AppButton data-testid="account-empty-create" @click="newFlag.set(true)">{{ t('sa.create.open') }}</AppButton>
      </template>
      <template #cell-name="{ row }">
        <router-link :to="accountPath(row.id)" class="block truncate font-mono text-ui font-medium text-primary hover:underline" :title="row.name">{{ row.name }}</router-link>
      </template>
      <template #cell-description="{ row }"><span class="block max-w-72 truncate text-muted" :title="row.description">{{ row.description || '—' }}</span></template>
      <template #cell-status="{ row }">
        <StatusBadge :label="row.disabled ? t('sa.statusDisabled') : t('sa.statusActive')" :variant="row.disabled ? 'danger' : 'success'" />
      </template>
      <template #cell-created_at="{ row }"><RelativeTime :value="row.created_at" /></template>
      <template #cell-actions="{ row }">
        <RowActions :actions="rowActions(row)" :subject="row.name" @action="(key) => onAction(key, row)" />
      </template>
    </ListTable>

    <ServiceAccountCreateDrawer :open="newFlag.on.value" @close="newFlag.set(false)" @created="onCreated" />

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('sa.confirmDelete')"
      :description="deleteTarget ? tp('sa.confirmDeleteBody', { name: deleteTarget.name }) : ''"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { CheckIcon, NoSymbolIcon, PencilSquareIcon, PlusIcon, TrashIcon } from '@heroicons/vue/20/solid'
import { deleteServiceAccount, listAllServiceAccounts, updateServiceAccount, type ServiceAccountItem } from '../../api/serviceAccounts'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import SectionActions from '../SectionActions.vue'
import StatusBadge from '../StatusBadge.vue'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import RowActions, { type RowAction } from '../ui/RowActions.vue'
import ServiceAccountCreateDrawer from './ServiceAccountCreateDrawer.vue'
import { useLocalList } from '../../composables/useLocalList'
import { useUrlFlag } from '../../composables/useUrlFlag'
import { enumParam, strParam } from '../../composables/useUrlState'
import { compareText } from '../../lib/intl'
import { accountPath, matchesAccount, type StatusFilter } from '../../lib/serviceAccounts'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const router = useRouter()
const toasts = useToastStore()
const newFlag = useUrlFlag('new')

const columns: ListColumn[] = [
  { key: 'name', label: t('sa.col.name'), sortable: true, grow: true },
  { key: 'description', label: t('sa.col.description'), dropOrder: 2 },
  { key: 'status', label: t('sa.col.status') },
  { key: 'created_at', label: t('sa.col.created'), sortable: true, dropOrder: 1 },
  { key: 'actions', label: t('common.actions'), actions: true },
]

// The list is short and the server pages without filtering, so it is loaded whole and filtered in memory.
const { list, refresh } = useLocalList({
  filters: { q: strParam(), status: enumParam(['all', 'active', 'disabled'] as const, 'all') },
  debounce: ['q'],
  sorts: ['name', 'created_at'],
  limit: 25,
  load: async () => (await listAllServiceAccounts()).items,
  match: (a, f) => matchesAccount(a, f.q, f.status),
  compare: (a, b, sort) => (sort === 'created_at' ? String(a.created_at ?? '').localeCompare(String(b.created_at ?? '')) : compareText(a.name, b.name)),
})

function rowActions(row: ServiceAccountItem): RowAction[] {
  return [
    { key: 'open', label: t('common.edit'), icon: PencilSquareIcon, to: accountPath(row.id) },
    row.disabled
      ? { key: 'toggle', label: t('sa.enable'), icon: CheckIcon }
      : { key: 'toggle', label: t('sa.disable'), icon: NoSymbolIcon },
    { key: 'delete', label: t('common.delete'), icon: TrashIcon, tone: 'danger' },
  ]
}

function onAction(key: string, row: ServiceAccountItem): void {
  if (key === 'toggle') void toggle(row)
  else if (key === 'delete') deleteTarget.value = row
}

async function onCreated(account: ServiceAccountItem): Promise<void> {
  toasts.success(tp('sa.created', { name: account.name }))
  await newFlag.set(false)
  await router.push(accountPath(account.id))
}

// Reversible: a toast, no confirmation.
async function toggle(row: ServiceAccountItem): Promise<void> {
  try {
    await updateServiceAccount(row.id, { disabled: !row.disabled })
    toasts.success(tp(row.disabled ? 'sa.enabledToast' : 'sa.disabledToast', { name: row.name }))
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
  }
}

const deleteTarget = ref<ServiceAccountItem | null>(null)
const deleting = ref(false)

async function confirmDelete(): Promise<void> {
  const account = deleteTarget.value
  if (!account) return
  deleting.value = true
  try {
    await deleteServiceAccount(account.id)
    toasts.success(tp('sa.deleted', { name: account.name }))
    deleteTarget.value = null
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
  } finally {
    deleting.value = false
  }
}
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-4">
    <SectionActions>
      <AppButton size="sm" class="max-md:h-11" @click="openCreate">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('admin.users.create') }}
      </AppButton>
    </SectionActions>

    <FilterBar
      :search="list.text.q"
      :search-label="t('adm.users.search')"
      :placeholder="t('adm.users.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear()"
    >
      <select
        :value="list.filters.value.status"
        :aria-label="t('adm.users.filter')"
        class="h-9 rounded-lg border border-border-strong bg-surface px-2 text-sm text-foreground max-md:h-11"
        @change="list.setFilter('status', ($event.target as HTMLSelectElement).value as StatusFilter)"
      >
        <option value="all">{{ t('adm.users.filter.all') }}</option>
        <option value="active">{{ t('adm.users.filter.active') }}</option>
        <option value="disabled">{{ t('adm.users.filter.disabled') }}</option>
      </select>
      <template v-if="counts" #after>
        <span class="text-xs text-muted" data-testid="user-counts">{{ tp('settings.users.counts', { total: String(counts.total), active: String(counts.active), disabled: String(counts.disabled) }) }}</span>
      </template>
    </FilterBar>

    <ListTable :columns="columns" :caption="t('adm.users.listTitle')" :query="list" :empty-title="t('adm.users.empty')" :empty-hint="t('settings.users.emptyHint')">
      <template #cell-username="{ row }">
        <div class="flex items-center gap-2">
          <span class="flex h-7 w-7 items-center justify-center rounded-full bg-primary/20 text-xs font-bold text-primary" aria-hidden="true">{{ (row.username || '?')[0]!.toUpperCase() }}</span>
          <span class="font-mono text-ui font-medium">{{ row.username }}</span>
        </div>
      </template>
      <template #cell-email="{ row }"><span class="text-muted">{{ row.email || '—' }}</span></template>
      <template #cell-status="{ row }">
        <span
          class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-caption font-medium"
          :class="row.disabled ? 'bg-danger-soft/40 text-danger' : 'bg-success-soft/40 text-success'"
        >
          <span class="h-1.5 w-1.5 rounded-full" :class="row.disabled ? 'bg-danger' : 'bg-success'" aria-hidden="true" />
          {{ row.disabled ? t('adm.users.statusDisabled') : t('adm.users.statusActive') }}
        </span>
      </template>
      <template #cell-created_at="{ row }"><RelativeTime :value="row.created_at" /></template>
      <template #cell-actions="{ row }">
        <div class="flex items-center gap-1">
          <button type="button" :class="iconButton" :title="t('common.edit')" :aria-label="`${t('common.edit')} ${row.username}`" @click="openEdit(row)">
            <PencilIcon class="h-4 w-4" aria-hidden="true" />
          </button>
          <button
            type="button"
            :class="iconButton"
            :title="row.disabled ? t('admin.users.enable') : t('admin.users.disable')"
            :aria-label="`${row.disabled ? t('admin.users.enable') : t('admin.users.disable')} ${row.username}`"
            @click="toggleDisable(row)"
          >
            <component :is="row.disabled ? CheckIcon : NoSymbolIcon" class="h-4 w-4" aria-hidden="true" />
          </button>
          <button type="button" :class="iconButton" :title="t('admin.users.forceReset')" :aria-label="`${t('admin.users.forceReset')} ${row.username}`" @click="forceReset(row)">
            <KeyIcon class="h-4 w-4" aria-hidden="true" />
          </button>
          <button
            type="button"
            :class="[iconButton, 'hover:!text-danger']"
            :title="t('adm.users.deleteTitle')"
            :aria-label="`${t('adm.users.deleteTitle')}: ${row.username}`"
            @click="deleteTarget = row"
          >
            <TrashIcon class="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </template>
    </ListTable>

    <Drawer :open="drawerOpen" :busy="saving" :eyebrow="t('adm.users.eyebrow')" :title="editingUser ? t('adm.users.editTitle') : t('adm.users.addTitle')" @close="closeDrawer">
      <form id="user-form" class="space-y-5" @submit.prevent="submitForm">
        <div>
          <label for="user-full-name" class="mb-1.5 block text-sm font-medium text-foreground">{{ t('adm.users.field.name') }}</label>
          <input id="user-full-name" v-model="form.full_name" type="text" :placeholder="t('adm.users.field.namePlaceholder')" :class="inputClass" />
        </div>
        <div>
          <label for="user-username" class="mb-1.5 block text-sm font-medium text-foreground">{{ t('admin.users.column.username') }}</label>
          <input id="user-username" v-model="form.username" type="text" placeholder="username" :disabled="!!editingUser" :class="[inputClass, 'disabled:cursor-not-allowed disabled:opacity-50']" />
        </div>
        <div>
          <label for="user-email" class="mb-1.5 block text-sm font-medium text-foreground">{{ t('admin.users.column.email') }}</label>
          <input id="user-email" v-model="form.email" type="email" placeholder="user@example.com" :class="inputClass" />
        </div>
        <div v-if="!editingUser">
          <label for="user-password" class="mb-1.5 block text-sm font-medium text-foreground">{{ t('auth.password') }}</label>
          <input id="user-password" v-model="form.password" type="password" autocomplete="new-password" :placeholder="t('auth.passwordMin')" :class="inputClass" />
        </div>
        <label class="flex min-h-11 cursor-pointer items-center gap-2 text-sm text-foreground">
          <input v-model="form.must_change_password" type="checkbox" class="rounded" />
          <span>{{ t('adm.users.field.forceChange') }}</span>
        </label>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" :disabled="saving" @click="closeDrawer">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="user-form" :loading="saving">
          {{ editingUser ? t('adm.users.update') : t('common.create') }}
        </AppButton>
      </template>
    </Drawer>

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('adm.users.confirmDelete')"
      :description="deleteTarget ? `${deleteTarget.username}: ${t('adm.users.confirmDeleteBody')}` : ''"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CheckIcon, KeyIcon, NoSymbolIcon, PencilIcon, PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { createUser, deleteUser, forceUserPasswordReset, listAllUsers, setUserDisabled, updateUser } from '../../../api/admin'
import type { User } from '../../../api/generated/types.gen'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import SectionActions from '../../../components/SectionActions.vue'
import Drawer from '../../../components/ui/Drawer.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import RelativeTime from '../../../components/ui/RelativeTime.vue'
import { useLocalList } from '../../../composables/useLocalList'
import { useUrlFlag } from '../../../composables/useUrlFlag'
import { enumParam, strParam } from '../../../composables/useUrlState'
import { matchesUser, userCounts, type StatusFilter } from '../../../lib/users'
import { useToastStore } from '../../../stores/toast'
import { t, tp } from '../../../i18n'
import { compareText } from '../../../lib/intl'

type UserItem = User & { id: string }

const toasts = useToastStore()
const inputClass = 'w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-foreground placeholder:text-muted focus:ring-2 focus:ring-primary/50 max-md:min-h-11'
const iconButton = 'inline-flex h-8 w-8 items-center justify-center rounded-md text-muted hover:bg-surface-hover hover:text-foreground max-md:h-11 max-md:w-11'

const columns: ListColumn[] = [
  { key: 'username', label: t('admin.users.column.username'), sortable: true },
  { key: 'email', label: t('admin.users.column.email') },
  { key: 'status', label: t('adm.users.column.status') },
  { key: 'created_at', label: t('admin.users.column.created'), sortable: true },
  { key: 'actions', label: t('common.actions'), actions: true },
]

// The server pages /users but does not filter by status or sort, so the page walks every user once and
// filters, sorts and pages in memory: user lists are short and the counts stay exact.
const { list, all, refresh } = useLocalList({
  filters: { q: strParam(), status: enumParam(['all', 'active', 'disabled'] as const, 'all') },
  debounce: ['q'],
  sorts: ['username', 'created_at'],
  limit: 25,
  load: async () => (await listAllUsers()).items as UserItem[],
  match: (u, f) => matchesUser(u, f.q, f.status),
  compare: (a, b, sort) => {
    const key = sort === 'created_at' ? 'created_at' : 'username'
    return key === 'username' ? compareText(a.username, b.username) : String(a.created_at ?? '').localeCompare(String(b.created_at ?? ''))
  },
})
const counts = computed(() => (all.value.length ? userCounts(all.value) : null))

// Create / edit drawer. `?new=1` opens the create form (palette action, reload, Back closes it).
const newFlag = useUrlFlag('new')
const editingUser = ref<UserItem | null>(null)
const editOpen = ref(false)
const drawerOpen = computed(() => newFlag.on.value || editOpen.value)
const saving = ref(false)
const form = ref({ username: '', full_name: '', email: '', password: '', must_change_password: false })

watch(
  newFlag.on,
  (on) => {
    if (on) resetForm()
  },
  { immediate: true },
)

function resetForm(): void {
  editingUser.value = null
  form.value = { username: '', full_name: '', email: '', password: '', must_change_password: false }
}
function openCreate(): void {
  void newFlag.set(true)
}
function openEdit(user: UserItem): void {
  editingUser.value = user
  form.value = {
    username: user.username || '',
    full_name: user.full_name || '',
    email: user.email || '',
    password: '',
    must_change_password: !!user.must_change_password,
  }
  editOpen.value = true
}
function closeDrawer(): void {
  editOpen.value = false
  if (newFlag.on.value) void newFlag.set(false)
}

async function submitForm(): Promise<void> {
  if (saving.value) return
  saving.value = true
  const f = form.value
  try {
    if (editingUser.value) {
      await updateUser(editingUser.value.id, {
        full_name: f.full_name || undefined,
        email: f.email || undefined,
        must_change_password: f.must_change_password,
      })
      toasts.success(t('adm.users.updated'))
    } else {
      if (!f.username || !f.password) {
        toasts.error(t('adm.users.credentialsRequired'))
        return
      }
      await createUser({
        username: f.username,
        full_name: f.full_name || undefined,
        email: f.email || undefined,
        password: f.password,
        must_change_password: f.must_change_password,
      })
      toasts.success(t('adm.users.created'))
    }
    closeDrawer()
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  } finally {
    saving.value = false
  }
}

// Reversible: a toast, no confirmation.
async function toggleDisable(user: UserItem): Promise<void> {
  try {
    await setUserDisabled(user.id, !user.disabled)
    toasts.success(user.disabled ? t('adm.users.enabled') : t('adm.users.disabledToast'))
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  }
}

async function forceReset(user: UserItem): Promise<void> {
  try {
    await forceUserPasswordReset(user.id)
    toasts.success(t('adm.users.resetRequired'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  }
}

// DELETE /users/{id} removes the account for good; Disable keeps it. Only this one asks.
const deleteTarget = ref<UserItem | null>(null)
const deleting = ref(false)

async function confirmDelete(): Promise<void> {
  const user = deleteTarget.value
  if (!user) return
  deleting.value = true
  try {
    await deleteUser(user.id)
    toasts.success(t('adm.users.deletedToast'))
    deleteTarget.value = null
    await refresh()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  } finally {
    deleting.value = false
  }
}
</script>

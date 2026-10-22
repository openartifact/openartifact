<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('admin.users.title')" :description="t('adm.users.description')">
      <template #actions>
        <div class="flex items-center gap-2">
          <button
            type="button"
            @click="openCreate"
            class="px-3 py-1.5 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium"
          >
            {{ t('admin.users.create') }}
          </button>
        </div>
      </template>
    </PageHeader>

    <!-- StatRow -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-primary bg-primary-soft">
          <UsersIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-[10px] font-semibold text-muted">{{ t('adm.users.stat.total') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ total }}</div>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-success bg-success-soft/60">
          <CheckIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-[10px] font-semibold text-muted">{{ t('adm.users.stat.active') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ activeCount }}</div>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-danger bg-danger-soft/60">
          <NoSymbolIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-[10px] font-semibold text-muted">{{ t('adm.users.stat.disabled') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ disabledCount }}</div>
        </div>
      </div>
    </div>

    <!-- Search -->
    <div class="flex items-center gap-2">
      <div class="relative flex-1 max-w-sm">
        <MagnifyingGlassIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" aria-hidden="true" />
        <input
          v-model="q"
          type="search"
          :aria-label="t('adm.users.search')"
          :placeholder="t('adm.users.search')"
          class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
      </div>
    </div>

    <!-- TableCard -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.users.listTitle') }}</h3>
        <span class="text-[11px] font-mono text-muted">{{ filteredUsers.length }} {{ t('adm.common.total') }}</span>
      </div>
      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="filteredUsers.length === 0" class="px-4 py-8 text-center text-muted">{{ t('adm.users.empty') }}</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-[11px] font-semibold text-muted bg-surface-hover/30">
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('admin.users.column.username') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('admin.users.column.email') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.users.column.status') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('admin.users.column.created') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="user in filteredUsers" :key="user.id" class="hover:bg-surface-hover/40 transition-colors">
              <td class="px-4 py-3">
                <div class="flex items-center gap-2">
                  <div class="w-7 h-7 rounded-full bg-primary/20 text-primary text-xs font-bold flex items-center justify-center" aria-hidden="true">
                    {{ (user.username || '?')[0].toUpperCase() }}
                  </div>
                  <span class="font-mono text-[13px] font-medium">{{ user.username }}</span>
                </div>
              </td>
              <td class="px-4 py-3 text-muted text-sm">{{ user.email || '—' }}</td>
              <td class="px-4 py-3">
                <span
                  v-if="user.disabled"
                  class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] font-medium bg-danger-soft/40 text-danger"
                >
                  <span class="w-1.5 h-1.5 rounded-full bg-danger" />
                  {{ t('adm.users.statusDisabled') }}
                </span>
                <span v-else class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] font-medium bg-success-soft/40 text-success">
                  <span class="w-1.5 h-1.5 rounded-full bg-success" />
                  {{ t('adm.users.statusActive') }}
                </span>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDate(user.created_at) }}</td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-1">
                  <button
                    type="button"
                    @click="openEdit(user)"
                    :title="t('common.edit')"
                    :aria-label="`${t('common.edit')} ${user.username}`"
                    class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  >
                    <PencilIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                  <button
                    type="button"
                    @click="toggleDisable(user)"
                    :title="user.disabled ? t('admin.users.enable') : t('admin.users.disable')"
                    :aria-label="`${user.disabled ? t('admin.users.enable') : t('admin.users.disable')} ${user.username}`"
                    class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  >
                    <component :is="user.disabled ? CheckIcon : NoSymbolIcon" class="w-4 h-4" aria-hidden="true" />
                  </button>
                  <button
                    type="button"
                    @click="forceReset(user.id)"
                    :title="t('admin.users.forceReset')"
                    :aria-label="`${t('admin.users.forceReset')} ${user.username}`"
                    class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  >
                    <KeyIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                  <button
                    type="button"
                    @click="deleteOne(user)"
                    :disabled="user.disabled"
                    :title="t('adm.users.deleteTitle')"
                    :aria-label="`${t('adm.users.deleteTitle')}: ${user.username}`"
                    class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    <TrashIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Create/Edit Drawer -->
    <AdminDialog
      :open="drawerOpen"
      drawer
      :eyebrow="t('adm.users.eyebrow')"
      :title="editingUser ? t('adm.users.editTitle') : t('adm.users.addTitle')"
      @close="closeDrawer"
    >
      <form id="user-form" class="space-y-5" @submit.prevent="submitForm">
        <div>
          <label for="user-full-name" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.users.field.name') }}</label>
          <input
            id="user-full-name"
            v-model="formData.full_name"
            type="text"
            :placeholder="t('adm.users.field.namePlaceholder')"
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
        </div>
        <div>
          <label for="user-username" class="block text-sm font-medium text-foreground mb-1.5">{{ t('admin.users.column.username') }}</label>
          <input
            id="user-username"
            v-model="formData.username"
            type="text"
            placeholder="username"
            :disabled="!!editingUser"
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50 disabled:opacity-50 disabled:cursor-not-allowed"
          />
        </div>
        <div>
          <label for="user-email" class="block text-sm font-medium text-foreground mb-1.5">{{ t('admin.users.column.email') }}</label>
          <input
            id="user-email"
            v-model="formData.email"
            type="email"
            placeholder="user@example.com"
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
        </div>
        <div v-if="!editingUser">
          <label for="user-password" class="block text-sm font-medium text-foreground mb-1.5">{{ t('auth.password') }}</label>
          <input
            id="user-password"
            v-model="formData.password"
            type="password"
            autocomplete="new-password"
            :placeholder="t('auth.passwordMin')"
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
        </div>
        <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
          <input v-model="formData.must_change_password" type="checkbox" class="rounded" />
          <span>{{ t('adm.users.field.forceChange') }}</span>
        </label>
      </form>
      <template #footer>
        <button
          type="button"
          @click="closeDrawer"
          class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="user-form"
          :disabled="saving"
          class="px-3 py-1.5 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ editingUser ? t('adm.users.update') : t('common.create') }}
        </button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../../components/PageHeader.vue'
import { formatDate } from '../../lib/datetime'
import { ref, computed, onMounted } from 'vue'
import {
  listAllUsers,
  createUser,
  updateUser,
  deleteUser,
  setUserDisabled,
  forceUserPasswordReset,
} from '../../api/admin'
import type { User } from '../../api/generated/types.gen'
import AdminDialog from '../../components/AdminDialog.vue'
type UserItem = User & { id: string }
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import {
  UsersIcon,
  CheckIcon,
  NoSymbolIcon,
  MagnifyingGlassIcon,
  PencilIcon,
  KeyIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'

const toasts = useToastStore()

const users = ref<UserItem[]>([])
const loading = ref(false)
const loadError = ref('')
const saving = ref(false)
const q = ref('')
const drawerOpen = ref(false)
const editingUser = ref<UserItem | null>(null)

const formData = ref({
  username: '',
  full_name: '',
  email: '',
  password: '',
  must_change_password: false,
})

const filteredUsers = computed(() => {
  if (!q.value) return users.value
  const query = q.value.toLowerCase()
  return users.value.filter(
    (u) =>
      u.username?.toLowerCase().includes(query) ||
      u.email?.toLowerCase().includes(query) ||
      u.full_name?.toLowerCase().includes(query),
  )
})

const total = computed(() => users.value.length)
const activeCount = computed(() => users.value.filter((u) => !u.disabled).length)
const disabledCount = computed(() => users.value.filter((u) => u.disabled).length)


async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listAllUsers()
    users.value = res.items as UserItem[]
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.users.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  editingUser.value = null
  formData.value = {
    username: '',
    full_name: '',
    email: '',
    password: '',
    must_change_password: false,
  }
  drawerOpen.value = true
}

function openEdit(user: UserItem): void {
  editingUser.value = user
  formData.value = {
    username: user.username || '',
    full_name: user.full_name || '',
    email: user.email || '',
    password: '',
    must_change_password: !!user.must_change_password,
  }
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
  editingUser.value = null
}

async function submitForm(): Promise<void> {
  if (saving.value) return
  saving.value = true
  try {
    if (editingUser.value) {
      await updateUser(editingUser.value.id, {
        full_name: formData.value.full_name || undefined,
        email: formData.value.email || undefined,
        must_change_password: formData.value.must_change_password,
      })
      toasts.success(t('adm.users.updated'))
    } else {
      if (!formData.value.username || !formData.value.password) {
        toasts.error(t('adm.users.credentialsRequired'))
        return
      }
      await createUser({
        username: formData.value.username,
        full_name: formData.value.full_name || undefined,
        email: formData.value.email || undefined,
        password: formData.value.password,
        must_change_password: formData.value.must_change_password,
      })
      toasts.success(t('adm.users.created'))
    }
    closeDrawer()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  } finally {
    saving.value = false
  }
}

async function toggleDisable(user: UserItem): Promise<void> {
  try {
    await setUserDisabled(user.id, !user.disabled)
    toasts.success(user.disabled ? t('adm.users.enabled') : t('adm.users.disabledToast'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  }
}

async function forceReset(id: string | number): Promise<void> {
  try {
    await forceUserPasswordReset(id)
    toasts.success(t('adm.users.resetRequired'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  }
}

// The backend soft-deletes: DELETE /users/{id} disables the account and keeps
// the row, so the confirmation and toast say so instead of claiming removal.
async function deleteOne(user: UserItem): Promise<void> {
  if (!window.confirm(`${t('adm.users.confirmDelete')} "${user.username}"`)) return
  try {
    await deleteUser(user.id)
    toasts.success(t('adm.users.disabledToast'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.users.operationFailed'))
  }
}

onMounted(load)
</script>

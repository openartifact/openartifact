<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <div class="flex items-center gap-2 text-[11px] uppercase tracking-[0.14em] text-muted font-mono">
          <span>{{ t('nav.admin') }}</span>
          <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
          <span class="text-foreground/70">{{ t('nav.permissions') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('admin.permissions.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('adm.permissions.description') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <button
          type="button"
          @click="openGrant"
          class="px-3 py-1.5 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium"
        >
          {{ t('adm.permissions.create') }}
        </button>
      </div>
    </div>

    <!-- TableCard -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.permissions.listTitle') }}</h3>
        <span class="text-[11px] font-mono text-muted">{{ permissions.length }} {{ t('adm.common.total') }}</span>
      </div>
      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="permissions.length === 0" class="px-4 py-8 text-center text-muted">{{ t('adm.permissions.empty') }}</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-[11px] uppercase tracking-[0.12em] font-semibold text-muted bg-surface-hover/30">
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.permissions.column.name') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.permissions.column.repositories') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.permissions.column.paths') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('adm.permissions.column.actions') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="perm in permissions" :key="perm.id" class="hover:bg-surface-hover/40 transition-colors">
              <td class="px-4 py-3">
                <span class="font-mono text-[13px] font-medium">{{ perm.name || '—' }}</span>
              </td>
              <td class="px-4 py-3">
                <span class="font-mono text-[12px] px-2 py-0.5 rounded bg-surface-hover text-foreground/80">
                  {{ perm.repository_filter || '—' }}
                </span>
              </td>
              <td class="px-4 py-3">
                <span class="font-mono text-[12px] px-2 py-0.5 rounded bg-surface-hover text-foreground/80">
                  {{ perm.path_pattern || '—' }}
                </span>
              </td>
              <td class="px-4 py-3">
                <div class="flex flex-wrap gap-1.5">
                  <span v-for="action in perm.actions ?? []" :key="action" :class="getActionPillClasses(action)">
                    {{ action }}
                  </span>
                </div>
              </td>
              <td class="px-4 py-3">
                <button
                  type="button"
                  @click="revoke(perm)"
                  :title="t('admin.permissions.revoke')"
                  :aria-label="`${t('admin.permissions.revoke')}: ${perm.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Create target drawer -->
    <AdminDialog
      :open="grantDrawerOpen"
      drawer
      :eyebrow="t('adm.permissions.eyebrow')"
      :title="t('adm.permissions.createTitle')"
      @close="closeGrant"
    >
      <form id="permission-form" class="space-y-5" @submit.prevent="submitGrant">
        <div>
          <label for="perm-name" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.permissions.field.name') }}</label>
          <input
            id="perm-name"
            v-model="formData.name"
            type="text"
            required
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
        </div>
        <div>
          <label for="perm-repos" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.permissions.field.repositories') }}</label>
          <input
            id="perm-repos"
            v-model="formData.repository_filter"
            type="text"
            placeholder="^docker-.*$"
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
          <p class="mt-1 text-xs text-muted">{{ t('adm.permissions.field.regexHint') }}</p>
        </div>
        <div>
          <label for="perm-paths" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.permissions.field.paths') }}</label>
          <input
            id="perm-paths"
            v-model="formData.path_pattern"
            type="text"
            placeholder=".*"
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
          <p class="mt-1 text-xs text-muted">{{ t('adm.permissions.field.regexHint') }}</p>
        </div>
        <fieldset>
          <legend class="block text-sm font-medium text-foreground mb-2">{{ t('adm.permissions.field.actions') }}</legend>
          <div class="flex flex-wrap gap-4">
            <label v-for="action in PERMISSION_ACTIONS" :key="action" class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
              <input v-model="formData.actions" :value="action" type="checkbox" class="rounded border-border" />
              <span>{{ action }}</span>
            </label>
          </div>
        </fieldset>
        <fieldset class="space-y-3">
          <legend class="block text-sm font-medium text-foreground mb-1">
            {{ t('adm.permissions.field.assignTo') }} <span class="text-muted font-normal">({{ t('common.optional') }})</span>
          </legend>
          <div class="flex gap-4">
            <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
              <input v-model="formData.principal_type" value="user" type="radio" class="border-border" />
              <span>{{ t('adm.permissions.principalUser') }}</span>
            </label>
            <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
              <input v-model="formData.principal_type" value="group" type="radio" class="border-border" />
              <span>{{ t('adm.permissions.principalGroup') }}</span>
            </label>
          </div>
          <select
            v-model="formData.principal_id"
            :aria-label="t('adm.permissions.field.principal')"
            class="w-full px-3 py-2 border border-border rounded-lg bg-surface-hover/40 text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
          >
            <option value="">{{ t('adm.permissions.noPrincipal') }}</option>
            <option v-for="p in principalOptions" :key="p.id" :value="p.id">{{ p.label }}</option>
          </select>
        </fieldset>
      </form>
      <template #footer>
        <button
          type="button"
          @click="closeGrant"
          class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="permission-form"
          :disabled="saving || formData.actions.length === 0"
          class="px-3 py-1.5 text-sm rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ t('common.create') }}
        </button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  listPermissions,
  createPermissionTarget,
  assignPermissionTarget,
  revokePermission,
  listAllUsers,
  listGroups,
  PERMISSION_ACTIONS,
  type PermissionAction,
  type PermissionItem,
  type UserItem,
  type GroupItem,
} from '../../api/admin'
import AdminDialog from '../../components/AdminDialog.vue'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import { ChevronRightIcon, TrashIcon } from '@heroicons/vue/24/outline'

const toasts = useToastStore()

const permissions = ref<PermissionItem[]>([])
const users = ref<UserItem[]>([])
const groups = ref<GroupItem[]>([])
const loading = ref(false)
const loadError = ref('')
const saving = ref(false)
const grantDrawerOpen = ref(false)

const formData = ref({
  name: '',
  repository_filter: '.*',
  path_pattern: '.*',
  actions: ['read'] as PermissionAction[],
  principal_type: 'user' as 'user' | 'group',
  principal_id: '',
})

const principalOptions = computed(() =>
  formData.value.principal_type === 'user'
    ? users.value.map((u) => ({ id: String(u.id), label: u.username ?? String(u.id) }))
    : groups.value.map((g) => ({ id: String(g.id), label: g.name ?? String(g.id) })),
)

function getActionPillClasses(action: string): string {
  const base = 'inline-flex items-center px-2 py-0.5 rounded-full text-[11px] font-medium'
  if (action === 'manage' || action === 'delete') return `${base} bg-danger-soft/40 text-danger`
  if (action === 'write') return `${base} bg-warning-soft/40 text-warning`
  if (action === 'read') return `${base} bg-success-soft/40 text-success`
  return `${base} bg-surface-hover text-foreground`
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [res, usersRes, groupsRes] = await Promise.all([listPermissions(), listAllUsers(), listGroups()])
    permissions.value = res.items
    users.value = usersRes.items
    groups.value = groupsRes.items
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.permissions.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openGrant(): void {
  formData.value = {
    name: '',
    repository_filter: '.*',
    path_pattern: '.*',
    actions: ['read'],
    principal_type: 'user',
    principal_id: '',
  }
  grantDrawerOpen.value = true
}

function closeGrant(): void {
  grantDrawerOpen.value = false
}

// Creating a permission target and binding it to a principal are separate
// backend calls; the assignment is only made when a principal was chosen.
async function submitGrant(): Promise<void> {
  const form = formData.value
  if (!form.name.trim() || form.actions.length === 0) {
    toasts.error(t('adm.permissions.required'))
    return
  }
  saving.value = true
  try {
    const target = await createPermissionTarget({
      name: form.name.trim(),
      repository_filter: form.repository_filter,
      path_pattern: form.path_pattern,
      actions: form.actions,
    })
    if (form.principal_id && target?.id) {
      await assignPermissionTarget(target.id, {
        principal_type: form.principal_type,
        principal_id: form.principal_id,
      })
    }
    toasts.success(t('adm.permissions.created'))
    closeGrant()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.permissions.createFailed'))
    await load()
  } finally {
    saving.value = false
  }
}

async function revoke(perm: PermissionItem): Promise<void> {
  if (!perm.id) return
  if (!window.confirm(`${t('adm.permissions.confirmRevoke')} "${perm.name}"?`)) return
  try {
    await revokePermission(perm.id)
    toasts.success(t('adm.permissions.revoked'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.permissions.revokeFailed'))
  }
}

onMounted(load)
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-4">
    <SectionActions>
      <AppButton size="sm" class="max-md:h-11" @click="openGrant">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('adm.permissions.create') }}
      </AppButton>
    </SectionActions>

    <FilterBar
      :search="list.text.q"
      :search-label="t('settings.permissions.search')"
      :placeholder="t('settings.permissions.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear()"
    />

    <ListTable :columns="columns" :caption="t('adm.permissions.listTitle')" :query="list" :empty-title="t('adm.permissions.empty')" :empty-hint="t('adm.permissions.emptyHint')">
      <template #empty-action><DocsLink :url="docsUrl">{{ t('docs.rbac') }}</DocsLink></template>
      <template #cell-name="{ row }"><span class="font-mono text-ui font-medium">{{ row.name || '—' }}</span></template>
      <template #cell-repository_filter="{ row }">
        <span class="rounded bg-surface-hover px-2 py-0.5 font-mono text-xs text-foreground/80">{{ row.repository_filter || '—' }}</span>
      </template>
      <template #cell-path_pattern="{ row }">
        <span class="rounded bg-surface-hover px-2 py-0.5 font-mono text-xs text-foreground/80">{{ row.path_pattern || '—' }}</span>
      </template>
      <template #cell-actions_list="{ row }">
        <div class="flex flex-wrap gap-1.5">
          <span v-for="action in row.actions ?? []" :key="action" :class="getActionPillClasses(action)">{{ action }}</span>
        </div>
      </template>
      <template #cell-actions="{ row }">
        <button
          type="button"
          :title="t('admin.permissions.revoke')"
          :aria-label="`${t('admin.permissions.revoke')}: ${row.name}`"
          class="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted hover:bg-danger/10 hover:text-danger max-md:h-11 max-md:w-11"
          @click="revokeTarget = row"
        >
          <TrashIcon class="h-4 w-4" aria-hidden="true" />
        </button>
      </template>
    </ListTable>

    <!-- Create target drawer -->
    <Drawer
      :open="grantDrawerOpen"
      :busy="saving"
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
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
          />
        </div>
        <div>
          <label for="perm-repos" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.permissions.field.repositories') }}</label>
          <input
            id="perm-repos"
            v-model="formData.repository_filter"
            type="text"
            placeholder="^docker-.*$"
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
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
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
          />
          <p class="mt-1 text-xs text-muted">{{ t('adm.permissions.field.regexHint') }}</p>
        </div>
        <fieldset>
          <legend class="block text-sm font-medium text-foreground mb-2">{{ t('adm.permissions.field.actions') }}</legend>
          <div class="flex flex-wrap gap-4">
            <label v-for="action in PERMISSION_ACTIONS" :key="action" class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
              <input v-model="formData.actions" :value="action" type="checkbox" class="rounded border-border-strong" />
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
              <input v-model="formData.principal_type" value="user" type="radio" class="border-border-strong" />
              <span>{{ t('adm.permissions.principalUser') }}</span>
            </label>
            <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
              <input v-model="formData.principal_type" value="group" type="radio" class="border-border-strong" />
              <span>{{ t('adm.permissions.principalGroup') }}</span>
            </label>
          </div>
          <select
            v-model="formData.principal_id"
            :aria-label="t('adm.permissions.field.principal')"
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 text-foreground focus:ring-2 focus:ring-primary/50"
          >
            <option value="">{{ t('adm.permissions.noPrincipal') }}</option>
            <option v-for="p in principalOptions" :key="p.id" :value="p.id">{{ p.label }}</option>
          </select>
        </fieldset>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="closeGrant">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="permission-form" :loading="saving" :disabled="formData.actions.length === 0">
          {{ t('common.create') }}
        </AppButton>
      </template>
    </Drawer>

    <ConfirmDialog
      :open="revokeTarget !== null"
      :title="t('adm.permissions.confirmRevoke')"
      :description="revokeTarget?.name ?? ''"
      :confirm-label="t('admin.permissions.revoke')"
      :busy="revoking"
      @confirm="confirmRevoke"
      @close="revokeTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import SectionActions from '../../../components/SectionActions.vue'
import { ref, computed, watch } from 'vue'
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
} from '../../../api/admin'
import DocsLink from '../../../components/DocsLink.vue'
import Drawer from '../../../components/ui/Drawer.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import { useLocalList } from '../../../composables/useLocalList'
import { useUrlFlag } from '../../../composables/useUrlFlag'
import { strParam } from '../../../composables/useUrlState'
import { useDocsLink } from '../../../composables/useDocsLink'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n'
import { PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { compareText } from '../../../lib/intl'

const toasts = useToastStore()
const docsUrl = useDocsLink('settings-permissions')

const users = ref<UserItem[]>([])
const groups = ref<GroupItem[]>([])
const saving = ref(false)

const columns: ListColumn[] = [
  { key: 'name', label: t('adm.permissions.column.name'), sortable: true },
  { key: 'repository_filter', label: t('adm.permissions.column.repositories') },
  { key: 'path_pattern', label: t('adm.permissions.column.paths') },
  { key: 'actions_list', label: t('adm.permissions.column.actions') },
  { key: 'actions', label: t('common.actions'), actions: true },
]

// /permission-targets returns every target at once; users and groups feed the assign-to select.
const { list, refresh: load } = useLocalList({
  filters: { q: strParam() },
  debounce: ['q'],
  sorts: ['name'],
  limit: 25,
  async load() {
    const [res, usersRes, groupsRes] = await Promise.all([listPermissions(), listAllUsers(), listGroups()])
    users.value = usersRes.items
    groups.value = groupsRes.items
    return res.items
  },
  match: (p, f) => {
    const q = f.q.trim().toLowerCase()
    return !q || [p.name, p.repository_filter, p.path_pattern].some((v) => v?.toLowerCase().includes(q))
  },
  compare: (a, b) => compareText(a.name, b.name),
})

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
  const base = 'inline-flex items-center px-2 py-0.5 rounded-full text-caption font-medium'
  if (action === 'manage' || action === 'delete') return `${base} bg-danger-soft/40 text-danger`
  if (action === 'write') return `${base} bg-warning-soft/40 text-warning`
  if (action === 'read') return `${base} bg-success-soft/40 text-success`
  return `${base} bg-surface-hover text-foreground`
}

// `?new=1` opens the create drawer (palette link, reload; Back closes it).
const newFlag = useUrlFlag('new')
const grantDrawerOpen = computed(() => newFlag.on.value)
watch(newFlag.on, (on) => { if (on) resetForm() }, { immediate: true })

function openGrant(): void {
  void newFlag.set(true)
}

function resetForm(): void {
  formData.value = {
    name: '',
    repository_filter: '.*',
    path_pattern: '.*',
    actions: ['read'],
    principal_type: 'user',
    principal_id: '',
  }
}

function closeGrant(): void {
  void newFlag.set(false)
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

const revokeTarget = ref<PermissionItem | null>(null)
const revoking = ref(false)

async function confirmRevoke(): Promise<void> {
  const perm = revokeTarget.value
  if (!perm?.id) return
  revoking.value = true
  try {
    await revokePermission(perm.id)
    toasts.success(t('adm.permissions.revoked'))
    revokeTarget.value = null
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.permissions.revokeFailed'))
  } finally {
    revoking.value = false
  }
}
</script>

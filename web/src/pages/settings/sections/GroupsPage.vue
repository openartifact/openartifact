<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-4">
    <SectionActions>
      <AppButton size="sm" class="max-md:h-11" @click="openCreate">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('admin.groups.create') }}
      </AppButton>
    </SectionActions>

    <FilterBar
      :search="list.text.q"
      :search-label="t('settings.groups.search')"
      :placeholder="t('settings.groups.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear()"
    >
      <template v-if="all.length" #after>
        <span class="text-xs text-muted" data-testid="group-counts">{{ tp('settings.groups.counts', { groups: String(all.length), memberships: String(totalMembers) }) }}</span>
      </template>
    </FilterBar>

    <ListTable :columns="columns" :caption="t('adm.groups.listTitle')" :query="list" :empty-title="t('adm.groups.empty')" :empty-hint="t('settings.groups.emptyHint')">
      <template #cell-name="{ row }"><span class="font-semibold text-foreground">{{ row.name }}</span></template>
      <template #cell-members="{ row }">
        <button
          type="button"
          :aria-label="`${t('admin.groups.manageMembers')}: ${row.name}`"
          class="inline-flex min-h-8 items-center gap-1.5 rounded-full bg-surface-hover px-2 py-0.5 font-mono text-xs text-foreground hover:bg-surface-hover/80 max-md:min-h-11"
          @click="openMembers(row)"
        >
          {{ memberCounts[String(row.id)] ?? '—' }}
        </button>
      </template>
      <template #cell-created_at="{ row }"><RelativeTime :value="row.created_at" /></template>
      <template #cell-actions="{ row }">
        <div class="flex items-center gap-1">
          <button type="button" :class="iconButton" :title="t('admin.groups.manageMembers')" :aria-label="`${t('admin.groups.manageMembers')}: ${row.name}`" @click="openMembers(row)">
            <UsersIcon class="h-4 w-4" aria-hidden="true" />
          </button>
          <button type="button" :class="[iconButton, 'hover:!text-danger']" :title="t('common.delete')" :aria-label="`${t('common.delete')}: ${row.name}`" @click="deleteTarget = row">
            <TrashIcon class="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </template>
    </ListTable>

    <!-- Create Drawer -->
    <Drawer
      :open="createDrawerOpen"
      :busy="saving"
      :eyebrow="t('adm.groups.eyebrow')"
      :title="t('adm.groups.createTitle')"
      @close="closeCreateDrawer"
    >
      <form id="group-form" class="space-y-5" @submit.prevent="submitCreate">
        <div>
          <label for="group-name" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.groups.field.name') }}</label>
          <input
            id="group-name"
            v-model="newGroupName"
            type="text"
            required
            :placeholder="t('adm.groups.field.namePlaceholder')"
            class="w-full px-3 py-2 border border-border-strong rounded-lg bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
          />
          <p class="text-xs text-muted mt-1">{{ t('adm.groups.field.nameHint') }}</p>
        </div>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="closeCreateDrawer">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="group-form" :loading="saving">
          {{ t('common.create') }}
        </AppButton>
      </template>
    </Drawer>

    <!-- Members Drawer -->
    <Drawer
      :open="membersDrawerOpen"
      :eyebrow="t('admin.groups.column.members')"
      :title="selectedGroup?.name ?? ''"
      @close="closeMembersDrawer"
    >
      <div class="space-y-5">
        <div>
          <label for="group-member-search" class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.groups.addMember') }}</label>
          <div class="relative">
            <MagnifyingGlassIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" aria-hidden="true" />
            <input
              id="group-member-search"
              v-model="memberSearch"
              type="search"
              :placeholder="t('adm.groups.searchUsers')"
              class="w-full pl-9 pr-3 h-9 text-sm rounded-lg bg-surface-hover/40 border border-border-strong text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50"
            />
          </div>
          <div v-if="memberSearch" class="mt-2 space-y-1 max-h-40 overflow-y-auto">
            <button
              v-for="user in filteredAvailableUsers"
              :key="user.id"
              type="button"
              :disabled="busyMember !== null"
              @click="addMember(user)"
              class="w-full text-left px-3 py-2 rounded-lg text-sm hover:bg-surface-hover transition-colors text-foreground disabled:opacity-50"
            >
              {{ user.username }}
            </button>
            <p v-if="filteredAvailableUsers.length === 0" class="px-3 py-2 text-sm text-muted">{{ t('adm.groups.noMatches') }}</p>
          </div>
        </div>

        <div>
          <h3 class="block text-sm font-medium text-foreground mb-1.5">{{ t('adm.groups.currentMembers') }}</h3>
          <div v-if="membersLoading" class="text-sm text-muted">{{ t('common.loading') }}</div>
          <div v-else-if="currentMembers.length === 0" class="text-sm text-muted">{{ t('adm.groups.noMembers') }}</div>
          <ul v-else class="space-y-1">
            <li
              v-for="member in currentMembers"
              :key="member.id"
              class="flex items-center justify-between px-3 py-2 rounded-lg bg-surface-hover/40 border border-border"
            >
              <span class="text-sm text-foreground">{{ member.full_name || member.username }}</span>
              <button
                type="button"
                :disabled="busyMember !== null"
                @click="removeMember(member)"
                :aria-label="`${t('adm.groups.removeMember')}: ${member.username}`"
                class="p-1 rounded text-muted hover:text-danger hover:bg-danger/10 transition-colors disabled:opacity-50"
              >
                <XMarkIcon class="w-4 h-4" aria-hidden="true" />
              </button>
            </li>
          </ul>
        </div>
      </div>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="closeMembersDrawer">
          {{ t('common.close') }}
        </AppButton>
      </template>
    </Drawer>

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('adm.groups.confirmDelete')"
      :description="deleteTarget?.name ?? ''"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import SectionActions from '../../../components/SectionActions.vue'
import { ref, computed, watch } from 'vue'
import {
  listGroups,
  createGroup,
  deleteGroup,
  listGroupMembers,
  addGroupMember,
  removeGroupMember,
  listAllUsers,
  type GroupItem,
  type UserItem,
} from '../../../api/admin'
import Drawer from '../../../components/ui/Drawer.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import RelativeTime from '../../../components/ui/RelativeTime.vue'
import { useLocalList } from '../../../composables/useLocalList'
import { useUrlFlag } from '../../../composables/useUrlFlag'
import { strParam } from '../../../composables/useUrlState'
import { useToastStore } from '../../../stores/toast'
import { t, tp } from '../../../i18n'
import {
  PlusIcon,
  UsersIcon,
  TrashIcon,
  XMarkIcon,
  MagnifyingGlassIcon,
} from '@heroicons/vue/24/outline'
import { compareText } from '../../../lib/intl'

const toasts = useToastStore()

const memberCounts = ref<Record<string, number>>({})
const allUsers = ref<UserItem[]>([])
const saving = ref(false)
const membersDrawerOpen = ref(false)
const membersLoading = ref(false)
const busyMember = ref<string | null>(null)
const selectedGroup = ref<GroupItem | null>(null)
const newGroupName = ref('')
const memberSearch = ref('')
const currentMembers = ref<UserItem[]>([])

const iconButton = 'inline-flex h-8 w-8 items-center justify-center rounded-md text-muted hover:bg-surface-hover hover:text-foreground max-md:h-11 max-md:w-11'
const columns: ListColumn[] = [
  { key: 'name', label: t('admin.groups.column.name'), sortable: true },
  { key: 'members', label: t('admin.groups.column.members') },
  { key: 'created_at', label: t('admin.groups.column.created'), sortable: true },
  { key: 'actions', label: t('common.actions'), actions: true },
]

// /groups returns every group at once and carries no member count, so the page loads the groups, the users
// (for the member picker) and one membership list per group, then filters and sorts in memory.
const { list, all, refresh: load } = useLocalList({
  filters: { q: strParam() },
  debounce: ['q'],
  sorts: ['name', 'created_at'],
  limit: 25,
  async load() {
    const [groupsRes, usersRes] = await Promise.all([listGroups(), listAllUsers()])
    allUsers.value = usersRes.items
    const lists = await Promise.all(groupsRes.items.map((g) => listGroupMembers(String(g.id))))
    memberCounts.value = Object.fromEntries(groupsRes.items.map((g, i) => [String(g.id), lists[i]!.items.length]))
    return groupsRes.items
  },
  match: (g, f) => !f.q.trim() || (g.name ?? '').toLowerCase().includes(f.q.trim().toLowerCase()),
  compare: (a, b, sort) => {
    const key = sort === 'created_at' ? 'created_at' : 'name'
    return key === 'name' ? compareText(a.name, b.name) : String(a.created_at ?? '').localeCompare(String(b.created_at ?? ''))
  },
})

const totalMembers = computed(() => Object.values(memberCounts.value).reduce((sum, n) => sum + n, 0))

const filteredAvailableUsers = computed(() => {
  if (!memberSearch.value) return []
  const query = memberSearch.value.toLowerCase()
  const memberIds = new Set(currentMembers.value.map((m) => m.id))
  return allUsers.value.filter(
    (u) =>
      !memberIds.has(u.id) &&
      !u.disabled &&
      (u.username?.toLowerCase().includes(query) || u.full_name?.toLowerCase().includes(query)),
  )
})

// `?new=1` opens the create drawer (palette link, reload; Back closes it).
const newFlag = useUrlFlag('new')
const createDrawerOpen = computed(() => newFlag.on.value)
watch(newFlag.on, (on) => {
  if (on) newGroupName.value = ''
})

function openCreate(): void {
  void newFlag.set(true)
}

function closeCreateDrawer(): void {
  void newFlag.set(false)
  newGroupName.value = ''
}

async function submitCreate(): Promise<void> {
  const name = newGroupName.value.trim()
  if (!name) {
    toasts.error(t('adm.groups.nameRequired'))
    return
  }
  saving.value = true
  try {
    await createGroup({ name })
    toasts.success(t('adm.groups.created'))
    closeCreateDrawer()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.groups.createFailed'))
  } finally {
    saving.value = false
  }
}

async function refreshMembers(group: GroupItem): Promise<void> {
  const res = await listGroupMembers(String(group.id))
  currentMembers.value = res.items
  memberCounts.value = { ...memberCounts.value, [String(group.id)]: res.items.length }
}

async function openMembers(group: GroupItem): Promise<void> {
  selectedGroup.value = group
  currentMembers.value = []
  memberSearch.value = ''
  membersDrawerOpen.value = true
  membersLoading.value = true
  try {
    await refreshMembers(group)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.groups.loadMembersFailed'))
  } finally {
    membersLoading.value = false
  }
}

function closeMembersDrawer(): void {
  membersDrawerOpen.value = false
  selectedGroup.value = null
  currentMembers.value = []
  memberSearch.value = ''
}

// Membership changes use the single-member endpoints so concurrent edits and
// members this page has not loaded are never overwritten.
async function addMember(user: UserItem): Promise<void> {
  const group = selectedGroup.value
  if (!group || !user.id) return
  busyMember.value = user.id
  try {
    await addGroupMember(String(group.id), user.id)
    memberSearch.value = ''
    await refreshMembers(group)
    toasts.success(t('adm.groups.memberAdded'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.groups.memberAddFailed'))
  } finally {
    busyMember.value = null
  }
}

async function removeMember(member: UserItem): Promise<void> {
  const group = selectedGroup.value
  if (!group || !member.id) return
  busyMember.value = member.id
  try {
    await removeGroupMember(String(group.id), member.id)
    await refreshMembers(group)
    toasts.success(t('adm.groups.memberRemoved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.groups.memberRemoveFailed'))
  } finally {
    busyMember.value = null
  }
}

const deleteTarget = ref<GroupItem | null>(null)
const deleting = ref(false)

async function confirmDelete(): Promise<void> {
  const group = deleteTarget.value
  if (!group) return
  deleting.value = true
  try {
    await deleteGroup(String(group.id))
    toasts.success(t('adm.groups.deleted'))
    deleteTarget.value = null
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.groups.deleteFailed'))
  } finally {
    deleting.value = false
  }
}
</script>

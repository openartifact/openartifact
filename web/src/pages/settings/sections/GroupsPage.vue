<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <SectionActions>
        <div class="flex items-center gap-2">
          <AppButton variant="primary" size="sm" @click="openCreate">
            {{ t('admin.groups.create') }}
          </AppButton>
        </div>
      </SectionActions>

    <!-- StatRow -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-primary bg-primary-soft">
          <FolderIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-micro font-semibold text-muted">{{ t('adm.groups.stat.groups') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ groups.length }}</div>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-success bg-success-soft/60">
          <UsersIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-micro font-semibold text-muted">{{ t('adm.groups.stat.memberships') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ totalMembers }}</div>
        </div>
      </div>
    </div>

    <!-- TableCard -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.groups.listTitle') }}</h3>
        <span class="text-caption font-mono text-muted">{{ groups.length }} {{ t('adm.common.total') }}</span>
      </div>
      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="groups.length === 0" class="px-4 py-8 text-center text-muted">{{ t('adm.groups.empty') }}</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-caption font-semibold text-muted bg-surface-hover/30">
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('admin.groups.column.name') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('admin.groups.column.members') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('admin.groups.column.created') }}</th>
              <th scope="col" class="text-left px-4 py-2.5 font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="group in groups" :key="group.id" class="hover:bg-surface-hover/40 transition-colors">
              <td class="px-4 py-3">
                <span class="font-semibold text-foreground">{{ group.name }}</span>
              </td>
              <td class="px-4 py-3">
                <button
                  type="button"
                  @click="openMembers(group)"
                  :aria-label="`${t('admin.groups.manageMembers')}: ${group.name}`"
                  class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-mono bg-surface-hover text-foreground hover:bg-surface-hover/80 transition-colors"
                >
                  {{ memberCounts[String(group.id)] ?? '—' }}
                </button>
              </td>
              <td class="px-4 py-3 text-xs text-muted font-mono">{{ formatDate(group.created_at) }}</td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-1">
                  <button
                    type="button"
                    @click="openMembers(group)"
                    :title="t('admin.groups.manageMembers')"
                    :aria-label="`${t('admin.groups.manageMembers')}: ${group.name}`"
                    class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  >
                    <UsersIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                  <button
                    type="button"
                    @click="deleteOne(group)"
                    :title="t('common.delete')"
                    :aria-label="`${t('common.delete')}: ${group.name}`"
                    class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
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

    <!-- Create Drawer -->
    <AdminDialog
      :open="createDrawerOpen"
      drawer
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
    </AdminDialog>

    <!-- Members Drawer -->
    <AdminDialog
      :open="membersDrawerOpen"
      drawer
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
    </AdminDialog>

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
import { formatDate } from '../../../lib/datetime'
import { ref, computed, onMounted } from 'vue'
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
import AdminDialog from '../../../components/AdminDialog.vue'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n/en'
import {
  FolderIcon,
  UsersIcon,
  TrashIcon,
  XMarkIcon,
  MagnifyingGlassIcon,
} from '@heroicons/vue/24/outline'

const toasts = useToastStore()

const groups = ref<GroupItem[]>([])
const memberCounts = ref<Record<string, number>>({})
const allUsers = ref<UserItem[]>([])
const loading = ref(false)
const loadError = ref('')
const saving = ref(false)
const createDrawerOpen = ref(false)
const membersDrawerOpen = ref(false)
const membersLoading = ref(false)
const busyMember = ref<string | null>(null)
const selectedGroup = ref<GroupItem | null>(null)
const newGroupName = ref('')
const memberSearch = ref('')
const currentMembers = ref<UserItem[]>([])

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


async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [groupsRes, usersRes] = await Promise.all([listGroups(), listAllUsers()])
    groups.value = groupsRes.items
    allUsers.value = usersRes.items
    // The group payload carries no member count, so derive it from the
    // membership lists (one small request per group).
    const lists = await Promise.all(groupsRes.items.map((g) => listGroupMembers(String(g.id))))
    memberCounts.value = Object.fromEntries(groupsRes.items.map((g, i) => [String(g.id), lists[i].items.length]))
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.groups.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  newGroupName.value = ''
  createDrawerOpen.value = true
}

function closeCreateDrawer(): void {
  createDrawerOpen.value = false
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

function deleteOne(group: GroupItem): void {
  deleteTarget.value = group
}

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

onMounted(load)
</script>

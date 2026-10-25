<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="open"
    drawer
    max-width="max-w-lg"
    :eyebrow="t('adm.projects.accessEyebrow')"
    :title="project?.name ?? ''"
    @close="emit('close')"
  >
    <div class="-mx-6 -my-5">
      <!-- Members -->
      <form class="px-6 py-4 border-b border-border bg-surface-hover/20 space-y-3" @submit.prevent="addMember">
        <p class="text-xs font-semibold text-foreground">{{ t('adm.projects.addMember') }}</p>
        <div class="flex items-center gap-2">
          <select
            v-model="newMemberUserId"
            :aria-label="t('adm.projects.field.user')"
            class="flex-1 min-w-0 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
          >
            <option value="">{{ t('adm.projects.selectUser') }}</option>
            <option v-for="u in availableUsers" :key="u.id" :value="u.id">{{ u.username }}</option>
          </select>
          <select
            v-model="newMemberRole"
            :aria-label="t('adm.projects.field.role')"
            class="px-2.5 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary shrink-0"
          >
            <option v-for="role in PROJECT_MEMBER_ROLES" :key="role" :value="role">{{ role }}</option>
          </select>
          <AppButton variant="primary" size="sm" type="submit" class="shrink-0" :loading="addingMember" :disabled="!newMemberUserId">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.projects.add') }}
          </AppButton>
        </div>
      </form>

      <div>
        <div v-if="membersLoading" class="px-6 py-8 text-center text-sm text-muted">{{ t('adm.projects.loadingMembers') }}</div>
        <div v-else-if="members.length === 0" class="px-6 py-12 text-center">
          <UsersIcon class="w-10 h-10 text-muted mx-auto mb-3" aria-hidden="true" />
          <p class="text-sm font-medium text-foreground">{{ t('adm.projects.noMembers') }}</p>
          <p class="text-xs text-muted mt-1">{{ t('adm.projects.noMembersHint') }}</p>
        </div>
        <table v-else class="w-full">
          <thead>
            <tr class="bg-surface-hover/30">
              <th scope="col" class="text-left text-caption font-semibold text-muted px-6 py-2.5">{{ t('adm.projects.column.user') }}</th>
              <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.role') }}</th>
              <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.joined') }}</th>
              <th scope="col" class="w-10"><span class="sr-only">{{ t('common.actions') }}</span></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="member in members" :key="member.user_id" class="hover:bg-surface-hover/30 transition-colors group">
              <td class="px-6 py-3">
                <div class="flex items-center gap-2">
                  <div class="w-7 h-7 rounded-full bg-primary/20 flex items-center justify-center shrink-0" aria-hidden="true">
                    <span class="text-primary text-xs font-medium">{{ memberName(member)[0]?.toUpperCase() ?? '?' }}</span>
                  </div>
                  <span class="font-mono text-xs text-foreground">{{ memberName(member) }}</span>
                </div>
              </td>
              <td class="px-3 py-3">
                <select
                  :value="member.role"
                  :aria-label="tp('adm.projects.memberRole', { name: memberName(member) })"
                  class="px-2 py-1 rounded-md border border-border-strong bg-bg text-foreground text-xs focus:ring-2 focus:ring-primary"
                  @change="changeMemberRole(member, ($event.target as HTMLSelectElement).value as ProjectMemberRole)"
                >
                  <option v-for="role in PROJECT_MEMBER_ROLES" :key="role" :value="role">{{ role }}</option>
                </select>
              </td>
              <td class="px-3 py-3">
                <span class="font-mono text-xs text-muted">{{ formatDate(member.created_at) }}</span>
              </td>
              <td class="px-3 py-3">
                <button
                  type="button"
                  :title="t('adm.projects.removeMember')"
                  :aria-label="tp('adm.projects.removeMemberAria', { name: memberName(member) })"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors opacity-60 group-hover:opacity-100 focus:opacity-100"
                  @click="removeMemberTarget = member"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Groups -->
      <form class="px-6 py-4 border-y border-border bg-surface-hover/20 space-y-3" data-testid="project-groups" @submit.prevent="attachGroup">
        <p class="text-xs font-semibold text-foreground">{{ t('adm.projects.groups.attach') }}</p>
        <div class="flex items-center gap-2">
          <select
            v-model="newGroupId"
            :aria-label="t('adm.projects.field.group')"
            class="flex-1 min-w-0 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
          >
            <option value="">{{ t('adm.projects.groups.select') }}</option>
            <option v-for="g in availableGroups" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
          <select
            v-model="newGroupRole"
            :aria-label="t('adm.projects.field.role')"
            class="px-2.5 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary shrink-0"
          >
            <option v-for="role in PROJECT_GROUP_ROLES" :key="role" :value="role">{{ role }}</option>
          </select>
          <AppButton variant="primary" size="sm" type="submit" class="shrink-0" :loading="attachingGroup" :disabled="!newGroupId">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.projects.add') }}
          </AppButton>
        </div>
      </form>

      <div>
        <div v-if="groupsLoading" class="px-6 py-8 text-center text-sm text-muted">{{ t('adm.projects.groups.loading') }}</div>
        <div v-else-if="grants.length === 0" class="px-6 py-12 text-center">
          <UserGroupIcon class="w-10 h-10 text-muted mx-auto mb-3" aria-hidden="true" />
          <p class="text-sm font-medium text-foreground">{{ t('adm.projects.groups.empty') }}</p>
          <p class="text-xs text-muted mt-1">{{ t('adm.projects.groups.emptyHint') }}</p>
        </div>
        <table v-else class="w-full">
          <thead>
            <tr class="bg-surface-hover/30">
              <th scope="col" class="text-left text-caption font-semibold text-muted px-6 py-2.5">{{ t('adm.projects.column.group') }}</th>
              <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.role') }}</th>
              <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.attached') }}</th>
              <th scope="col" class="w-10"><span class="sr-only">{{ t('common.actions') }}</span></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="grant in grants" :key="grant.group_id" class="hover:bg-surface-hover/30 transition-colors group">
              <td class="px-6 py-3">
                <div class="flex items-center gap-2">
                  <UserGroupIcon class="w-4 h-4 text-muted shrink-0" aria-hidden="true" />
                  <span class="font-mono text-xs text-foreground">{{ grant.group_name || grant.group_id }}</span>
                </div>
              </td>
              <td class="px-3 py-3">
                <select
                  :value="grant.role"
                  :aria-label="tp('adm.projects.groups.groupRole', { name: grant.group_name || grant.group_id || '' })"
                  class="px-2 py-1 rounded-md border border-border-strong bg-bg text-foreground text-xs focus:ring-2 focus:ring-primary"
                  @change="changeGroupRole(grant, ($event.target as HTMLSelectElement).value as ProjectGroupRole)"
                >
                  <option v-for="role in PROJECT_GROUP_ROLES" :key="role" :value="role">{{ role }}</option>
                </select>
              </td>
              <td class="px-3 py-3">
                <span class="font-mono text-xs text-muted">{{ formatDate(grant.created_at) }}</span>
              </td>
              <td class="px-3 py-3">
                <button
                  type="button"
                  :title="t('adm.projects.groups.remove')"
                  :aria-label="tp('adm.projects.groups.removeAria', { name: grant.group_name || grant.group_id || '' })"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors opacity-60 group-hover:opacity-100 focus:opacity-100"
                  @click="removeGroupTarget = grant"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <template #footer>
      <button
        type="button"
        class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
        @click="emit('close')"
      >
        {{ t('common.close') }}
      </button>
    </template>
  </AdminDialog>

  <ConfirmDialog
    :open="!!removeMemberTarget"
    :title="t('adm.projects.confirmRemoveMember')"
    :description="removeMemberTarget ? memberName(removeMemberTarget) : ''"
    :confirm-label="t('adm.projects.removeMember')"
    :busy="removingMember"
    @confirm="confirmRemoveMember"
    @close="removeMemberTarget = null"
  />

  <ConfirmDialog
    :open="!!removeGroupTarget"
    :title="t('adm.projects.groups.confirmRemove')"
    :description="removeGroupTarget ? (removeGroupTarget.group_name || removeGroupTarget.group_id || '') : ''"
    :confirm-label="t('adm.projects.groups.remove')"
    :busy="removingGroup"
    @confirm="confirmRemoveGroup"
    @close="removeGroupTarget = null"
  />
</template>

<script setup lang="ts">
import { toRef, watch } from 'vue'
import { PlusIcon, TrashIcon, UserGroupIcon, UsersIcon } from '@heroicons/vue/24/outline'
import type { GroupItem, UserItem } from '../api/admin'
import {
  PROJECT_GROUP_ROLES,
  PROJECT_MEMBER_ROLES,
  type Project,
  type ProjectGroupRole,
  type ProjectMemberRole,
} from '../api/projects'
import { useProjectGroups } from '../composables/useProjectGroups'
import { useProjectMembers } from '../composables/useProjectMembers'
import { t, tp } from '../i18n/en'
import { formatDate } from '../lib/datetime'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'

const props = defineProps<{
  open: boolean
  project: Project | null
  users: UserItem[]
  allGroups: GroupItem[]
}>()

const emit = defineEmits<{
  close: []
  /** The member count of a project changed after a (re)load. */
  'members-count': [projectId: string, count: number]
  /** The group grant count of a project changed after a (re)load. */
  'groups-count': [projectId: string, count: number]
}>()

const project = toRef(props, 'project')

const {
  members,
  loading: membersLoading,
  newUserId: newMemberUserId,
  newRole: newMemberRole,
  adding: addingMember,
  removeTarget: removeMemberTarget,
  removing: removingMember,
  availableUsers,
  memberName,
  open: loadMembers,
  add: addMember,
  changeRole: changeMemberRole,
  confirmRemove: confirmRemoveMember,
} = useProjectMembers(
  project,
  toRef(props, 'users'),
  (id, count) => emit('members-count', id, count),
)

const {
  grants,
  loading: groupsLoading,
  newGroupId,
  newRole: newGroupRole,
  attaching: attachingGroup,
  removeTarget: removeGroupTarget,
  removing: removingGroup,
  availableGroups,
  open: loadGroups,
  attach: attachGroup,
  changeRole: changeGroupRole,
  confirmRemove: confirmRemoveGroup,
} = useProjectGroups(
  project,
  toRef(props, 'allGroups'),
  (id, count) => emit('groups-count', id, count),
)

// Every opening resets both forms and reloads members and groups.
watch(
  () => props.open,
  (open) => {
    if (open && props.project) void Promise.all([loadMembers(props.project), loadGroups(props.project)])
  },
)
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Members and attached groups of a project. Every role-bearing control follows
  the server's escalation rules: maintainers grant and change roles below
  maintainer only, owner/maintainer entries need an owner, and the recorded
  owner and the last owner cannot be demoted or removed. The data is owned by
  the page; this component mutates through the API and asks it to reload.
-->
<template>
  <div class="space-y-6">
    <p v-if="project.is_system" class="text-sm text-muted" data-testid="members-system">{{ t('projects.members.system') }}</p>
    <p v-else-if="!canManage" class="text-sm text-muted" data-testid="members-readonly">{{ t('projects.members.readOnly') }}</p>

    <!-- Members -->
    <section class="rounded-xl border border-border bg-surface overflow-hidden" :aria-label="t('projects.members.title')">
      <div class="px-4 py-3 border-b border-border">
        <h2 class="text-sm font-semibold text-foreground">{{ t('projects.members.title') }}</h2>
        <p class="text-xs text-muted mt-0.5">{{ t('projects.members.hint') }}</p>
      </div>

      <form
        v-if="canManage"
        class="px-4 py-3 border-b border-border bg-surface-hover/20 flex flex-wrap items-end gap-2"
        data-testid="member-add"
        @submit.prevent="addMember"
      >
        <div class="flex-1 min-w-40">
          <label for="member-username" class="block text-xs font-semibold text-foreground mb-1">{{ t('projects.members.username') }}</label>
          <input
            id="member-username"
            v-model="newUsername"
            type="text"
            autocomplete="off"
            :placeholder="t('projects.members.usernamePlaceholder')"
            class="w-full px-3 py-1.5 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary"
          />
        </div>
        <div>
          <label class="block text-xs font-semibold text-foreground mb-1" for="member-new-role">{{ t('projects.role.label') }}</label>
          <RoleSelect
            id="member-new-role"
            v-model="newRole"
            :roles="MEMBER_ROLES_ASC"
            :grantable="memberGrantable"
            :label="t('projects.role.label')"
          />
        </div>
        <AppButton variant="primary" size="sm" type="submit" :loading="adding" :disabled="!newUsername.trim()">
          {{ t('projects.members.addAction') }}
        </AppButton>
        <p v-if="myRole !== 'owner'" class="basis-full text-caption text-muted">{{ t('projects.members.escalation') }}</p>
      </form>

      <EmptyState v-if="members.length === 0" :title="t('projects.members.empty')" />
      <div v-else class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.members.column.user') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.members.column.role') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.members.column.joined') }}</th>
              <th v-if="canManage" scope="col" class="w-12 px-4 py-2.5"><span class="sr-only">{{ t('common.actions') }}</span></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="member in members" :key="member.user_id" data-testid="member-row">
              <td class="px-4 py-3 font-mono text-sm text-foreground">{{ memberName(member) }}</td>
              <td class="px-4 py-3">
                <RoleSelect
                  v-if="canManage"
                  :model-value="member.role ?? 'viewer'"
                  :roles="MEMBER_ROLES_ASC"
                  :grantable="memberGrantable"
                  :disabled="!memberEditable(member)"
                  :title="memberProtected(member) ? t('projects.members.protected') : undefined"
                  :label="`${t('projects.members.roleOf')} ${memberName(member)}`"
                  @update:model-value="(role) => changeMemberRole(member, role as ProjectMemberRole)"
                />
                <ProjectRoleBadge v-else :role="member.role" />
              </td>
              <td class="px-4 py-3 text-xs text-muted">{{ formatDate(member.created_at) }}</td>
              <td v-if="canManage" class="px-4 py-3 text-right">
                <button
                  type="button"
                  :title="t('projects.members.remove')"
                  :aria-label="`${t('projects.members.remove')}: ${memberName(member)}`"
                  :disabled="!memberEditable(member)"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors disabled:opacity-40 disabled:pointer-events-none"
                  @click="removeMemberTarget = member"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- Groups -->
    <section class="rounded-xl border border-border bg-surface overflow-hidden" :aria-label="t('projects.groups.title')" data-testid="project-groups">
      <div class="px-4 py-3 border-b border-border">
        <h2 class="text-sm font-semibold text-foreground">{{ t('projects.groups.title') }}</h2>
        <p class="text-xs text-muted mt-0.5">{{ t('projects.groups.hint') }}</p>
      </div>

      <form
        v-if="canManage"
        class="px-4 py-3 border-b border-border bg-surface-hover/20 flex flex-wrap items-end gap-2"
        data-testid="group-attach"
        @submit.prevent="attachGroup"
      >
        <div class="flex-1 min-w-40">
          <label for="group-name" class="block text-xs font-semibold text-foreground mb-1">{{ t('projects.groups.name') }}</label>
          <input
            id="group-name"
            v-model="newGroupName"
            type="text"
            autocomplete="off"
            :placeholder="t('projects.groups.namePlaceholder')"
            class="w-full px-3 py-1.5 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary"
          />
        </div>
        <div>
          <label class="block text-xs font-semibold text-foreground mb-1" for="group-new-role">{{ t('projects.role.label') }}</label>
          <RoleSelect
            id="group-new-role"
            v-model="newGroupRole"
            :roles="GROUP_ROLES_ASC"
            :grantable="groupGrantable"
            :label="t('projects.role.label')"
          />
        </div>
        <AppButton variant="primary" size="sm" type="submit" :loading="attaching" :disabled="!newGroupName.trim()">
          {{ t('projects.groups.attachAction') }}
        </AppButton>
      </form>

      <EmptyState v-if="groups.length === 0" :title="t('projects.groups.empty')" />
      <div v-else class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.groups.column.group') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.groups.column.role') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.groups.column.attached') }}</th>
              <th v-if="canManage" scope="col" class="w-12 px-4 py-2.5"><span class="sr-only">{{ t('common.actions') }}</span></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="grant in groups" :key="grant.group_id" data-testid="group-row">
              <td class="px-4 py-3 font-mono text-sm text-foreground">{{ groupName(grant) }}</td>
              <td class="px-4 py-3">
                <RoleSelect
                  v-if="canManage"
                  :model-value="grant.role ?? 'viewer'"
                  :roles="GROUP_ROLES_ASC"
                  :grantable="groupGrantable"
                  :disabled="!canModifyEntry(myRole, grant.role)"
                  :label="`${t('projects.groups.roleOf')} ${groupName(grant)}`"
                  @update:model-value="(role) => changeGroupRole(grant, role as ProjectGroupRole)"
                />
                <ProjectRoleBadge v-else :role="grant.role" />
              </td>
              <td class="px-4 py-3 text-xs text-muted">{{ formatDate(grant.created_at) }}</td>
              <td v-if="canManage" class="px-4 py-3 text-right">
                <button
                  type="button"
                  :title="t('projects.groups.remove')"
                  :aria-label="`${t('projects.groups.remove')}: ${groupName(grant)}`"
                  :disabled="!canModifyEntry(myRole, grant.role)"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors disabled:opacity-40 disabled:pointer-events-none"
                  @click="removeGroupTarget = grant"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <ConfirmDialog
      :open="!!removeMemberTarget"
      :title="t('projects.members.confirmRemove')"
      :description="removeMemberTarget ? memberName(removeMemberTarget) : ''"
      :confirm-label="t('projects.members.remove')"
      :busy="removingMember"
      @confirm="confirmRemoveMember"
      @close="removeMemberTarget = null"
    />
    <ConfirmDialog
      :open="!!removeGroupTarget"
      :title="t('projects.groups.confirmRemove')"
      :description="removeGroupTarget ? groupName(removeGroupTarget) : ''"
      :confirm-label="t('projects.groups.remove')"
      :busy="removingGroup"
      @confirm="confirmRemoveGroup"
      @close="removeGroupTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { TrashIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import EmptyState from './EmptyState.vue'
import ProjectRoleBadge from './ProjectRoleBadge.vue'
import RoleSelect from './RoleSelect.vue'
import {
  addProjectMember,
  removeProjectGroup,
  removeProjectMember,
  setProjectGroup,
  updateProjectMember,
  type Project,
  type ProjectGroup,
  type ProjectGroupRole,
  type ProjectMember,
  type ProjectMemberRole,
} from '../api/projects'
import { formatDate } from '../lib/datetime'
import {
  GROUP_ROLES_ASC,
  MEMBER_ROLES_ASC,
  atLeast,
  canModifyEntry,
  errorStatus,
  grantableRoles,
} from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{
  project: Project
  members: ProjectMember[]
  groups: ProjectGroup[]
}>()

/** Something changed server-side; the page reloads members and groups. */
const emit = defineEmits<{ (e: 'changed'): void }>()

const toasts = useToastStore()

const name = computed(() => props.project.name ?? '')
const myRole = computed(() => props.project.my_role)
// The system project's membership grants nothing, so its editing affordances stay hidden.
const canManage = computed(() => !props.project.is_system && atLeast(myRole.value, 'maintainer'))
const memberGrantable = computed(() => grantableRoles(myRole.value, 'member'))
const groupGrantable = computed(() => grantableRoles(myRole.value, 'group'))

const ownerCount = computed(() => props.members.filter((m) => m.role === 'owner').length)

function memberName(member: ProjectMember): string {
  return member.username || member.user_id || ''
}

function groupName(grant: ProjectGroup): string {
  return grant.group_name || grant.group_id || ''
}

/** The recorded owner and the last owner cannot be demoted or removed. */
function memberProtected(member: ProjectMember): boolean {
  if (member.user_id && member.user_id === props.project.owner_id) return true
  return member.role === 'owner' && ownerCount.value <= 1
}

function memberEditable(member: ProjectMember): boolean {
  return canModifyEntry(myRole.value, member.role) && !memberProtected(member)
}

// Add member
const newUsername = ref('')
const newRole = ref<ProjectMemberRole>('viewer')
const adding = ref(false)

async function addMember(): Promise<void> {
  const username = newUsername.value.trim()
  if (!username || adding.value) return
  adding.value = true
  try {
    await addProjectMember(name.value, { username, role: newRole.value })
    newUsername.value = ''
    toasts.success(t('projects.members.added'))
    emit('changed')
  } catch (err) {
    const status = errorStatus(err)
    toasts.error(
      status === 404 ? t('projects.members.userNotFound')
      : status === 409 ? t('projects.members.alreadyMember')
      : status === 403 ? t('projects.members.forbidden')
      : t('projects.members.addFailed'),
    )
  } finally {
    adding.value = false
  }
}

async function changeMemberRole(member: ProjectMember, role: ProjectMemberRole): Promise<void> {
  if (!member.user_id || member.role === role) return
  try {
    await updateProjectMember(name.value, member.user_id, { role })
    toasts.success(t('projects.members.roleUpdated'))
  } catch (err) {
    const status = errorStatus(err)
    toasts.error(
      status === 409 ? t('projects.members.protected')
      : status === 403 ? t('projects.members.forbidden')
      : t('projects.members.roleUpdateFailed'),
    )
  }
  // Reload either way so the select reflects what the server holds.
  emit('changed')
}

const removeMemberTarget = ref<ProjectMember | null>(null)
const removingMember = ref(false)

async function confirmRemoveMember(): Promise<void> {
  const member = removeMemberTarget.value
  if (!member?.user_id) return
  removingMember.value = true
  try {
    await removeProjectMember(name.value, member.user_id)
    toasts.success(t('projects.members.removed'))
    removeMemberTarget.value = null
    emit('changed')
  } catch (err) {
    const status = errorStatus(err)
    toasts.error(
      status === 409 ? t('projects.members.protected')
      : status === 403 ? t('projects.members.forbidden')
      : t('projects.members.removeFailed'),
    )
  } finally {
    removingMember.value = false
  }
}

// Groups
const newGroupName = ref('')
const newGroupRole = ref<ProjectGroupRole>('viewer')
const attaching = ref(false)

async function attachGroup(): Promise<void> {
  const group = newGroupName.value.trim()
  if (!group || attaching.value) return
  attaching.value = true
  try {
    await setProjectGroup(name.value, group, { role: newGroupRole.value })
    newGroupName.value = ''
    toasts.success(t('projects.groups.attached'))
    emit('changed')
  } catch (err) {
    const status = errorStatus(err)
    toasts.error(
      status === 404 ? t('projects.groups.notFound')
      : status === 403 ? t('projects.members.forbidden')
      : t('projects.groups.attachFailed'),
    )
  } finally {
    attaching.value = false
  }
}

async function changeGroupRole(grant: ProjectGroup, role: ProjectGroupRole): Promise<void> {
  if (!grant.group_id || grant.role === role) return
  try {
    await setProjectGroup(name.value, grant.group_id, { role })
    toasts.success(t('projects.groups.roleUpdated'))
  } catch (err) {
    toasts.error(errorStatus(err) === 403 ? t('projects.members.forbidden') : t('projects.groups.roleUpdateFailed'))
  }
  emit('changed')
}

const removeGroupTarget = ref<ProjectGroup | null>(null)
const removingGroup = ref(false)

async function confirmRemoveGroup(): Promise<void> {
  const grant = removeGroupTarget.value
  if (!grant?.group_id) return
  removingGroup.value = true
  try {
    await removeProjectGroup(name.value, grant.group_id)
    toasts.success(t('projects.groups.removed'))
    removeGroupTarget.value = null
    emit('changed')
  } catch (err) {
    toasts.error(errorStatus(err) === 403 ? t('projects.members.forbidden') : t('projects.groups.removeFailed'))
  } finally {
    removingGroup.value = false
  }
}
</script>

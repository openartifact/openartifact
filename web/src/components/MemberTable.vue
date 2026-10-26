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
  <div class="space-y-8">
    <p v-if="project.is_system" class="text-sm text-muted" data-testid="members-system">{{ t('projects.members.system') }}</p>
    <p v-else-if="!canManage" class="text-sm text-muted" data-testid="members-readonly">{{ t('projects.members.readOnly') }}</p>

    <!-- Members -->
    <section :aria-label="t('projects.members.title')">
      <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 class="text-sm font-semibold text-foreground">{{ t('projects.members.title') }}</h2>
          <p class="mt-0.5 text-xs text-muted">{{ t('projects.members.hint') }}</p>
        </div>
        <AppButton v-if="canManage" variant="primary" size="sm" data-testid="member-add" @click="memberDrawer = true">
          {{ t('projects.members.add') }}
        </AppButton>
      </div>
      <ListTable
        :columns="memberColumns"
        :caption="t('projects.members.title')"
        :rows="members"
        row-key="user_id"
        :total="members.length"
        :limit="Math.max(members.length, 1)"
        :empty-title="t('projects.members.empty')"
      >
        <template #cell-user="{ row }">
          <span class="font-mono text-sm text-foreground" data-testid="member-row">{{ memberName(row) }}</span>
        </template>
        <template #cell-role="{ row }">
          <RoleSelect
            v-if="canManage"
            :model-value="row.role ?? 'viewer'"
            :roles="MEMBER_ROLES_ASC"
            :grantable="memberGrantable"
            :disabled="!memberEditable(row)"
            :title="memberProtected(row) ? t('projects.members.protected') : undefined"
            :label="`${t('projects.members.roleOf')} ${memberName(row)}`"
            @update:model-value="(role) => changeMemberRole(row, role as ProjectMemberRole)"
          />
          <ProjectRoleBadge v-else :role="row.role" />
        </template>
        <template #cell-joined="{ row }">
          <time :datetime="row.created_at" :title="formatDateTime(row.created_at)" class="text-xs text-muted">{{ timeAgo(row.created_at) }}</time>
        </template>
        <template #cell-actions="{ row }">
          <button
            type="button"
            :title="t('projects.members.remove')"
            :aria-label="`${t('projects.members.remove')}: ${memberName(row)}`"
            :disabled="!memberEditable(row)"
            class="rounded-md p-1.5 text-muted transition-colors hover:bg-danger/10 hover:text-danger disabled:pointer-events-none disabled:opacity-40 max-md:h-11 max-md:w-11"
            @click="removeMemberTarget = row"
          >
            <TrashIcon class="h-4 w-4" aria-hidden="true" />
          </button>
        </template>
      </ListTable>
    </section>

    <!-- Groups -->
    <section :aria-label="t('projects.groups.title')" data-testid="project-groups">
      <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 class="text-sm font-semibold text-foreground">{{ t('projects.groups.title') }}</h2>
          <p class="mt-0.5 text-xs text-muted">{{ t('projects.groups.hint') }}</p>
        </div>
        <AppButton v-if="canManage" variant="secondary" size="sm" data-testid="group-attach" @click="groupDrawer = true">
          {{ t('projects.groups.attach') }}
        </AppButton>
      </div>
      <ListTable
        :columns="groupColumns"
        :caption="t('projects.groups.title')"
        :rows="groups"
        row-key="group_id"
        :total="groups.length"
        :limit="Math.max(groups.length, 1)"
        :empty-title="t('projects.groups.empty')"
      >
        <template #cell-group="{ row }">
          <span class="font-mono text-sm text-foreground" data-testid="group-row">{{ groupName(row) }}</span>
        </template>
        <template #cell-role="{ row }">
          <RoleSelect
            v-if="canManage"
            :model-value="row.role ?? 'viewer'"
            :roles="GROUP_ROLES_ASC"
            :grantable="groupGrantable"
            :disabled="!canModifyEntry(myRole, row.role)"
            :label="`${t('projects.groups.roleOf')} ${groupName(row)}`"
            @update:model-value="(role) => changeGroupRole(row, role as ProjectGroupRole)"
          />
          <ProjectRoleBadge v-else :role="row.role" />
        </template>
        <template #cell-attached="{ row }">
          <time :datetime="row.created_at" :title="formatDateTime(row.created_at)" class="text-xs text-muted">{{ timeAgo(row.created_at) }}</time>
        </template>
        <template #cell-actions="{ row }">
          <button
            type="button"
            :title="t('projects.groups.remove')"
            :aria-label="`${t('projects.groups.remove')}: ${groupName(row)}`"
            :disabled="!canModifyEntry(myRole, row.role)"
            class="rounded-md p-1.5 text-muted transition-colors hover:bg-danger/10 hover:text-danger disabled:pointer-events-none disabled:opacity-40 max-md:h-11 max-md:w-11"
            @click="removeGroupTarget = row"
          >
            <TrashIcon class="h-4 w-4" aria-hidden="true" />
          </button>
        </template>
      </ListTable>
    </section>

    <Drawer :open="memberDrawer" :title="t('projects.members.add')" :eyebrow="t('projects.members.title')" :busy="adding" @close="memberDrawer = false">
      <form id="member-add-form" class="space-y-4" @submit.prevent="addMember">
        <FormField :label="t('projects.members.username')" id="member-username">
          <input
            id="member-username"
            v-model="newUsername"
            type="text"
            autocomplete="off"
            :placeholder="t('projects.members.usernamePlaceholder')"
            :class="[INPUT_CLASS, 'font-mono']"
          />
        </FormField>
        <FormField :label="t('projects.role.label')" id="member-new-role">
          <RoleSelect
            id="member-new-role"
            v-model="newRole"
            :roles="MEMBER_ROLES_ASC"
            :grantable="memberGrantable"
            :label="t('projects.role.label')"
          />
        </FormField>
        <p v-if="myRole !== 'owner'" class="text-caption text-muted">{{ t('projects.members.escalation') }}</p>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" :disabled="adding" @click="memberDrawer = false">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="member-add-form" :loading="adding" :disabled="!newUsername.trim()">
          {{ t('projects.members.addAction') }}
        </AppButton>
      </template>
    </Drawer>

    <Drawer :open="groupDrawer" :title="t('projects.groups.attach')" :eyebrow="t('projects.groups.title')" :busy="attaching" @close="groupDrawer = false">
      <form id="group-attach-form" class="space-y-4" @submit.prevent="attachGroup">
        <FormField :label="t('projects.groups.name')" id="group-name">
          <input
            id="group-name"
            v-model="newGroupName"
            type="text"
            autocomplete="off"
            :placeholder="t('projects.groups.namePlaceholder')"
            :class="[INPUT_CLASS, 'font-mono']"
          />
        </FormField>
        <FormField :label="t('projects.role.label')" id="group-new-role">
          <RoleSelect
            id="group-new-role"
            v-model="newGroupRole"
            :roles="GROUP_ROLES_ASC"
            :grantable="groupGrantable"
            :label="t('projects.role.label')"
          />
        </FormField>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" :disabled="attaching" @click="groupDrawer = false">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="group-attach-form" :loading="attaching" :disabled="!newGroupName.trim()">
          {{ t('projects.groups.attachAction') }}
        </AppButton>
      </template>
    </Drawer>

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
import FormField from './FormField.vue'
import ProjectRoleBadge from './ProjectRoleBadge.vue'
import RoleSelect from './RoleSelect.vue'
import Drawer from './ui/Drawer.vue'
import ListTable, { type ListColumn } from './ui/ListTable.vue'
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
import { formatDateTime, timeAgo } from '../lib/datetime'
import { INPUT_CLASS } from '../lib/formClasses'
import {
  GROUP_ROLES_ASC,
  MEMBER_ROLES_ASC,
  atLeast,
  canModifyEntry,
  errorStatus,
  grantableRoles,
} from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

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

const memberColumns = computed<ListColumn[]>(() => [
  { key: 'user', label: t('projects.members.column.user') },
  { key: 'role', label: t('projects.members.column.role') },
  { key: 'joined', label: t('projects.members.column.joined') },
  ...(canManage.value ? [{ key: 'actions', label: t('common.actions'), actions: true }] : []),
])
const groupColumns = computed<ListColumn[]>(() => [
  { key: 'group', label: t('projects.groups.column.group') },
  { key: 'role', label: t('projects.groups.column.role') },
  { key: 'attached', label: t('projects.groups.column.attached') },
  ...(canManage.value ? [{ key: 'actions', label: t('common.actions'), actions: true }] : []),
])

const memberDrawer = ref(false)
const groupDrawer = ref(false)

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
    memberDrawer.value = false
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
    groupDrawer.value = false
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

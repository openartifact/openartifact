<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Members of a project: table, add drawer and remove confirmation. Role
  controls follow the server's escalation rules (see MemberTable).
-->
<template>
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
        <span class="block truncate font-mono text-sm text-foreground" data-testid="member-row" :title="memberName(row)">{{ memberName(row) }}</span>
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
        <RowActions
          :actions="[{ key: 'remove', label: t('projects.members.remove'), icon: TrashIcon, tone: 'danger', disabled: !memberEditable(row) }]"
          :subject="memberName(row)"
          @action="removeMemberTarget = row"
        />
      </template>
    </ListTable>

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

    <ConfirmDialog
      :open="!!removeMemberTarget"
      :title="t('projects.members.confirmRemove')"
      :description="removeMemberTarget ? memberName(removeMemberTarget) : ''"
      :confirm-label="t('projects.members.remove')"
      :busy="removingMember"
      @confirm="confirmRemoveMember"
      @close="removeMemberTarget = null"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { TrashIcon } from '@heroicons/vue/20/solid'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import FormField from './FormField.vue'
import ProjectRoleBadge from './ProjectRoleBadge.vue'
import RoleSelect from './RoleSelect.vue'
import Drawer from './ui/Drawer.vue'
import RowActions from './ui/RowActions.vue'
import ListTable, { type ListColumn } from './ui/ListTable.vue'
import { addProjectMember, removeProjectMember, updateProjectMember, type Project, type ProjectMember, type ProjectMemberRole } from '../api/projects'
import { useProjectAccess } from '../composables/useAccessErrorToast'
import { formatDateTime, timeAgo } from '../lib/datetime'
import { INPUT_CLASS } from '../lib/formClasses'
import { MEMBER_ROLES_ASC, canModifyEntry, grantableRoles } from '../lib/projectAccess'
import { t } from '../i18n'

const props = defineProps<{
  project: Project
  members: ProjectMember[]
  canManage: boolean
}>()

/** Something changed server-side; the page reloads members and groups. */
const emit = defineEmits<{ (e: 'changed'): void }>()

const { toasts, toastAccessError, name, myRole } = useProjectAccess(() => props.project)
const memberGrantable = computed(() => grantableRoles(myRole.value, 'member'))

const memberColumns = computed<ListColumn[]>(() => [
  { key: 'user', label: t('projects.members.column.user'), grow: true },
  { key: 'role', label: t('projects.members.column.role') },
  { key: 'joined', label: t('projects.members.column.joined'), dropOrder: 1 },
  ...(props.canManage ? [{ key: 'actions', label: t('common.actions'), actions: true }] : []),
])

const memberDrawer = ref(false)

const ownerCount = computed(() => props.members.filter((m) => m.role === 'owner').length)

function memberName(member: ProjectMember): string {
  return member.username || member.user_id || ''
}

/** The recorded owner and the last owner cannot be demoted or removed. */
function memberProtected(member: ProjectMember): boolean {
  if (member.user_id && member.user_id === props.project.owner_id) return true
  return member.role === 'owner' && ownerCount.value <= 1
}

function memberEditable(member: ProjectMember): boolean {
  return canModifyEntry(myRole.value, member.role) && !memberProtected(member)
}

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
    toastAccessError(err, t('projects.members.addFailed'), {
      404: t('projects.members.userNotFound'),
      409: t('projects.members.alreadyMember'),
    })
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
    toastAccessError(err, t('projects.members.roleUpdateFailed'), { 409: t('projects.members.protected') })
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
    toastAccessError(err, t('projects.members.removeFailed'), { 409: t('projects.members.protected') })
  } finally {
    removingMember.value = false
  }
}
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Groups attached to a project: table, attach drawer and remove confirmation.
  Role controls follow the server's escalation rules (see MemberTable).
-->
<template>
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
        <span class="block truncate font-mono text-sm text-foreground" data-testid="group-row" :title="groupName(row)">{{ groupName(row) }}</span>
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
        <RowActions
          :actions="[{ key: 'remove', label: t('projects.groups.remove'), icon: TrashIcon, tone: 'danger', disabled: !canModifyEntry(myRole, row.role) }]"
          :subject="groupName(row)"
          @action="removeGroupTarget = row"
        />
      </template>
    </ListTable>

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
      :open="!!removeGroupTarget"
      :title="t('projects.groups.confirmRemove')"
      :description="removeGroupTarget ? groupName(removeGroupTarget) : ''"
      :confirm-label="t('projects.groups.remove')"
      :busy="removingGroup"
      @confirm="confirmRemoveGroup"
      @close="removeGroupTarget = null"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { TrashIcon } from '@heroicons/vue/20/solid'
import ListTable, { type ListColumn } from './ui/ListTable.vue'
import RowActions from './ui/RowActions.vue'
import Drawer from './ui/Drawer.vue'
import RoleSelect from './RoleSelect.vue'
import ProjectRoleBadge from './ProjectRoleBadge.vue'
import FormField from './FormField.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import AppButton from './AppButton.vue'
import { removeProjectGroup, setProjectGroup, type Project, type ProjectGroup, type ProjectGroupRole } from '../api/projects'
import { useProjectAccess } from '../composables/useAccessErrorToast'
import { formatDateTime, timeAgo } from '../lib/datetime'
import { INPUT_CLASS } from '../lib/formClasses'
import { GROUP_ROLES_ASC, canModifyEntry, grantableRoles } from '../lib/projectAccess'
import { t } from '../i18n'

const props = defineProps<{
  project: Project
  groups: ProjectGroup[]
  canManage: boolean
}>()

/** Something changed server-side; the page reloads members and groups. */
const emit = defineEmits<{ (e: 'changed'): void }>()

const { toasts, toastAccessError, name, myRole } = useProjectAccess(() => props.project)
const groupGrantable = computed(() => grantableRoles(myRole.value, 'group'))

const groupColumns = computed<ListColumn[]>(() => [
  { key: 'group', label: t('projects.groups.column.group'), grow: true },
  { key: 'role', label: t('projects.groups.column.role') },
  { key: 'attached', label: t('projects.groups.column.attached'), dropOrder: 1 },
  ...(props.canManage ? [{ key: 'actions', label: t('common.actions'), actions: true }] : []),
])

const groupDrawer = ref(false)

function groupName(grant: ProjectGroup): string {
  return grant.group_name || grant.group_id || ''
}

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
    toastAccessError(err, t('projects.groups.attachFailed'), { 404: t('projects.groups.notFound') })
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
    toastAccessError(err, t('projects.groups.roleUpdateFailed'))
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
    toastAccessError(err, t('projects.groups.removeFailed'))
  } finally {
    removingGroup.value = false
  }
}
</script>

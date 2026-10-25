// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref, type Ref } from 'vue'
import type { UserItem } from '../api/admin'
import {
  addProjectMember,
  listProjectMembers,
  removeProjectMember,
  updateProjectMember,
  type Project,
  type ProjectMember,
  type ProjectMemberRole,
} from '../api/projects'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'

/**
 * Member management of one project in the admin drawer: the member list, the
 * add form and the role/remove actions. `onCount` reports the member count
 * after every refresh so the project table stays current.
 */
export function useProjectMembers(
  project: Ref<Project | null>,
  users: Ref<UserItem[]>,
  onCount: (projectId: string, count: number) => void,
) {
  const toasts = useToastStore()
  const members = ref<ProjectMember[]>([])
  const loading = ref(false)
  const newUserId = ref('')
  const newRole = ref<ProjectMemberRole>('viewer')
  const adding = ref(false)
  const removeTarget = ref<ProjectMember | null>(null)
  const removing = ref(false)

  const availableUsers = computed(() => {
    const memberIds = new Set(members.value.map((m) => m.user_id))
    return users.value.filter((u) => u.id && !memberIds.has(u.id) && !u.disabled)
  })

  function memberName(member: ProjectMember): string {
    return member.username || (users.value.find((u) => u.id === member.user_id)?.username ?? member.user_id ?? '')
  }

  async function refresh(target: Project): Promise<void> {
    members.value = await listProjectMembers(target.name ?? '')
    onCount(target.id ?? '', members.value.length)
  }

  /** Clears the add form and loads the members of the current project. */
  async function open(target: Project): Promise<void> {
    members.value = []
    newUserId.value = ''
    newRole.value = 'viewer'
    loading.value = true
    await refresh(target)
      .catch((err: unknown) => toasts.error(err instanceof Error ? err.message : t('adm.projects.loadMembersFailed')))
      .finally(() => {
        loading.value = false
      })
  }

  async function add(): Promise<void> {
    const target = project.value
    if (!newUserId.value || !target?.name || adding.value) return
    adding.value = true
    try {
      await addProjectMember(target.name, { user_id: newUserId.value, role: newRole.value })
      newUserId.value = ''
      toasts.success(t('adm.projects.memberAdded'))
      await refresh(target)
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.projects.memberAddFailed'))
    } finally {
      adding.value = false
    }
  }

  async function changeRole(member: ProjectMember, role: ProjectMemberRole): Promise<void> {
    const target = project.value
    if (!target?.name || !member.user_id || member.role === role) return
    try {
      await updateProjectMember(target.name, member.user_id, { role })
      toasts.success(t('adm.projects.memberRoleUpdated'))
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.projects.memberRoleUpdateFailed'))
    }
    // Reload either way so the select reflects what the server holds.
    await refresh(target).catch(() => undefined)
  }

  async function confirmRemove(): Promise<void> {
    const target = project.value
    const member = removeTarget.value
    if (!target?.name || !member?.user_id) return
    removing.value = true
    try {
      await removeProjectMember(target.name, member.user_id)
      toasts.success(t('adm.projects.memberRemoved'))
      removeTarget.value = null
      await refresh(target)
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.projects.memberRemoveFailed'))
    } finally {
      removing.value = false
    }
  }

  return {
    members,
    loading,
    newUserId,
    newRole,
    adding,
    removeTarget,
    removing,
    availableUsers,
    memberName,
    open,
    add,
    changeRole,
    confirmRemove,
  }
}

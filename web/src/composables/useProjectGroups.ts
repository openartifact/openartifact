// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref, type Ref } from 'vue'
import type { GroupItem } from '../api/admin'
import {
  listProjectGroups,
  removeProjectGroup,
  setProjectGroup,
  type Project,
  type ProjectGroup,
  type ProjectGroupRole,
} from '../api/projects'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'

/**
 * Group grants of one project in the admin drawer: the grant list, the attach
 * form and the role/detach actions. `onCount` reports the grant count after
 * every refresh so the project table stays current.
 */
export function useProjectGroups(
  project: Ref<Project | null>,
  allGroups: Ref<GroupItem[]>,
  onCount: (projectId: string, count: number) => void,
) {
  const toasts = useToastStore()
  const grants = ref<ProjectGroup[]>([])
  const loading = ref(false)
  const newGroupId = ref('')
  const newRole = ref<ProjectGroupRole>('viewer')
  const attaching = ref(false)
  const removeTarget = ref<ProjectGroup | null>(null)
  const removing = ref(false)

  const availableGroups = computed(() => {
    const attached = new Set(grants.value.map((g) => g.group_id))
    return allGroups.value.filter((g) => g.id && !attached.has(g.id))
  })

  async function refresh(target: Project): Promise<void> {
    grants.value = await listProjectGroups(target.name ?? '')
    onCount(target.id ?? '', grants.value.length)
  }

  /** Clears the attach form and loads the grants of the current project. */
  async function open(target: Project): Promise<void> {
    grants.value = []
    newGroupId.value = ''
    newRole.value = 'viewer'
    loading.value = true
    await refresh(target)
      .catch((err: unknown) => toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.loadFailed')))
      .finally(() => {
        loading.value = false
      })
  }

  async function attach(): Promise<void> {
    const target = project.value
    if (!newGroupId.value || !target?.name || attaching.value) return
    attaching.value = true
    try {
      await setProjectGroup(target.name, newGroupId.value, { role: newRole.value })
      newGroupId.value = ''
      toasts.success(t('adm.projects.groups.attached'))
      await refresh(target)
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.attachFailed'))
    } finally {
      attaching.value = false
    }
  }

  async function changeRole(grant: ProjectGroup, role: ProjectGroupRole): Promise<void> {
    const target = project.value
    if (!target?.name || !grant.group_id || grant.role === role) return
    try {
      await setProjectGroup(target.name, grant.group_id, { role })
      toasts.success(t('adm.projects.groups.roleUpdated'))
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.roleUpdateFailed'))
    }
    await refresh(target).catch(() => undefined)
  }

  async function confirmRemove(): Promise<void> {
    const target = project.value
    const grant = removeTarget.value
    if (!target?.name || !grant?.group_id) return
    removing.value = true
    try {
      await removeProjectGroup(target.name, grant.group_id)
      toasts.success(t('adm.projects.groups.removed'))
      removeTarget.value = null
      await refresh(target)
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.removeFailed'))
    } finally {
      removing.value = false
    }
  }

  return {
    grants,
    loading,
    newGroupId,
    newRole,
    attaching,
    removeTarget,
    removing,
    availableGroups,
    open,
    attach,
    changeRole,
    confirmRemove,
  }
}

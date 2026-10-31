<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Project roles of a service account: change a role, remove it, or add the account to another project. -->
<template>
  <AccessPanel
    :title="t('sa.projects.title')"
    :description="t('sa.projects.desc')"
    :rows="rows"
    :options="options"
    :add-label="t('sa.projects.add')"
    :remove-label="t('sa.projects.remove')"
    :empty-text="t('sa.projects.empty')"
    :loading="projects.loading.value"
    :error="projects.error.value"
    @add="addTo"
    @remove="removeFrom"
  >
    <template #row-extra="{ row }">
      <RoleSelect :model-value="roleOf(row.id)" :roles="PROJECT_MEMBER_ROLES" :label="tp('sa.projects.roleOf', { project: row.label })" @update:model-value="(role) => setRole(row.id, role as ProjectMemberRole)" />
    </template>
    <template #add-extra>
      <RoleSelect v-model="newRole" :roles="PROJECT_MEMBER_ROLES" :label="t('sa.projects.newRole')" />
    </template>
  </AccessPanel>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  addProjectMember,
  listProjectMembers,
  listProjects,
  PROJECT_MEMBER_ROLES,
  removeProjectMember,
  updateProjectMember,
  type ProjectMemberRole,
} from '../../api/projects'
import RoleSelect from '../RoleSelect.vue'
import AccessPanel from './AccessPanel.vue'
import { useAccountAccess } from '../../composables/useAccountAccess'
import { useLoadable } from '../../composables/useLoadable'
import { t, tp } from '../../i18n'

const props = defineProps<{ accountId: string }>()

interface Split {
  member: { name: string; role: ProjectMemberRole }[]
  available: string[]
}

// There is no per-user endpoint: the members of every project are read and the account's role picked out.
async function loadProjects(): Promise<Split> {
  const projects = (await listProjects({ all: true })).projects.filter((p): p is typeof p & { name: string } => !!p.name)
  const members = await Promise.all(projects.map((p) => listProjectMembers(p.name)))
  const split: Split = { member: [], available: [] }
  projects.forEach((p, i) => {
    const mine = members[i]!.find((m) => m.user_id === props.accountId)
    if (mine?.role) split.member.push({ name: p.name, role: mine.role })
    else split.available.push(p.name)
  })
  return split
}

const projects = useLoadable(loadProjects, { member: [], available: [] } as Split)
const change = useAccountAccess(projects.reload)

// Least privilege first: a new membership starts as viewer.
const newRole = ref<string>('viewer')

const rows = computed(() => projects.data.value.member.map((m) => ({ id: m.name, label: m.name, to: `/projects/${encodeURIComponent(m.name)}` })))
const options = computed(() => projects.data.value.available.map((name) => ({ value: name, label: name })))
const roleOf = (project: string): string => projects.data.value.member.find((m) => m.name === project)?.role ?? 'viewer'

const addTo = (project: string): Promise<void> =>
  change(() => addProjectMember(project, { user_id: props.accountId, role: newRole.value as ProjectMemberRole }), tp('sa.projects.added', { name: project }))
const setRole = (project: string, role: ProjectMemberRole): Promise<void> =>
  change(() => updateProjectMember(project, props.accountId, { role }), tp('sa.projects.updated', { name: project }))
const removeFrom = (project: string): Promise<void> =>
  change(() => removeProjectMember(project, props.accountId), tp('sa.projects.removed', { name: project }))
</script>

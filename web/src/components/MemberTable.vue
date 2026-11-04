<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Members and attached groups of a project. Every role-bearing control follows
  the server's escalation rules: maintainers grant and change roles below
  maintainer only, owner/maintainer entries need an owner, and the recorded
  owner and the last owner cannot be demoted or removed. The data is owned by
  the page; the two sections mutate through the API and ask it to reload.
-->
<template>
  <div class="space-y-8">
    <p v-if="project.is_system" class="text-sm text-muted" data-testid="members-system">{{ t('projects.members.system') }}</p>
    <p v-else-if="!canManage" class="text-sm text-muted" data-testid="members-readonly">{{ t('projects.members.readOnly') }}</p>

    <ProjectMembersSection :project="project" :members="members" :can-manage="canManage" @changed="emit('changed')" />
    <ProjectGroupsSection :project="project" :groups="groups" :can-manage="canManage" @changed="emit('changed')" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import ProjectGroupsSection from './ProjectGroupsSection.vue'
import ProjectMembersSection from './ProjectMembersSection.vue'
import type { Project, ProjectGroup, ProjectMember } from '../api/projects'
import { atLeast } from '../lib/projectAccess'
import { t } from '../i18n'

const props = defineProps<{
  project: Project
  members: ProjectMember[]
  groups: ProjectGroup[]
}>()

/** Something changed server-side; the page reloads members and groups. */
const emit = defineEmits<{ (e: 'changed'): void }>()

// The system project's membership grants nothing, so its editing affordances stay hidden.
const canManage = computed(() => !props.project.is_system && atLeast(props.project.my_role, 'maintainer'))
</script>

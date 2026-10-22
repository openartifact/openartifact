<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Header of a project page: the project name with the caller's role, page
  actions, and the shared section bar. Sections the caller's role does not
  allow are not listed.
-->
<template>
  <div>
    <PageHeader :title="project?.display_name || name" :crumbs="crumbs">
      <template v-if="project" #meta>
        <ProjectRoleBadge :role="project.my_role" />
        <span v-if="project.is_system" class="text-xs text-muted">{{ t('projects.builtIn') }}</span>
        <span v-if="project.display_name && project.display_name !== name" class="font-mono text-xs text-muted">{{ name }}</span>
      </template>
      <template v-if="$slots.actions" #actions>
        <slot name="actions" />
      </template>
    </PageHeader>

    <SectionTabs v-if="project" :items="sections" :active="active" :label="t('projects.tabs')" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import PageHeader from './PageHeader.vue'
import ProjectRoleBadge from './ProjectRoleBadge.vue'
import SectionTabs, { type SectionTab } from './SectionTabs.vue'
import type { Project } from '../api/projects'
import type { Crumb } from '../lib/crumbs'
import { visibleProjectTabs, type ProjectTab } from '../lib/projectAccess'
import { t } from '../i18n/en'

const props = defineProps<{
  name: string
  project: Project | null
  active: ProjectTab
  /** Item count shown on the repositories and members sections. */
  counts?: Partial<Record<ProjectTab, number>>
  crumbs?: Crumb[]
}>()

const sections = computed<SectionTab[]>(() => {
  const base = `/projects/${encodeURIComponent(props.name)}`
  return visibleProjectTabs(props.project?.my_role).map((id) => ({
    id,
    label: t(`projects.tab.${id}`),
    to: id === 'overview' ? base : { path: base, query: { tab: id } },
    count: props.counts?.[id],
  }))
})
</script>

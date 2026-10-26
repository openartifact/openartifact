<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <DashSection :title="t('dashboard.projects.title')" heading-id="dash-projects" state="ready" :empty="projects.length === 0">
    <template #actions>
      <router-link to="/projects" class="text-xs text-primary hover:underline">{{ t('dashboard.projects.all') }}</router-link>
    </template>
    <template #empty>
      <StateBlock variant="empty" :title="t('dashboard.projects.empty')" :hint="t('dashboard.projects.emptyHint')" />
    </template>
    <ul class="divide-y divide-border rounded-lg border border-border bg-surface" role="list" data-testid="my-projects">
      <li v-for="p in projects" :key="p.name" class="flex items-center justify-between gap-3 px-4 py-2.5 text-sm max-md:min-h-11">
        <router-link :to="`/projects/${encodeURIComponent(p.name)}`" class="min-w-0 truncate font-medium text-foreground hover:underline">{{ p.name }}</router-link>
        <ProjectRoleBadge :role="p.role" />
      </li>
    </ul>
  </DashSection>
</template>

<script setup lang="ts">
import type { ProjectMembership } from '../../api/projects'
import ProjectRoleBadge from '../ProjectRoleBadge.vue'
import StateBlock from '../ui/StateBlock.vue'
import DashSection from './DashSection.vue'
import { t } from '../../i18n/en'

defineProps<{ projects: ProjectMembership[] }>()
</script>

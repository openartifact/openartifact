<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <label :for="id" class="block text-sm font-medium text-foreground mb-2">{{ t('repos.project.label') }}</label>
    <select
      :id="id"
      v-model="model"
      class="w-full px-3 py-2 border border-border-strong rounded-md text-sm font-mono bg-bg text-foreground focus:ring-2 focus:ring-primary"
    >
      <option value="">{{ t('repos.project.default') }}</option>
      <option v-for="name in names" :key="name" :value="name">{{ name }}</option>
    </select>
    <p class="text-xs mt-1 text-muted">{{ t('repos.project.hint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { listProjects, SYSTEM_PROJECT_NAME } from '../api/projects'
import type { Project } from '../api/projects'
import { t } from '../i18n/en'

defineProps<{ id: string }>()

// '' selects the built-in system project (the default).
const model = defineModel<string>({ default: '' })
const projects = ref<Project[]>([])

// The repository's current project stays selectable even when the list could
// not be loaded or does not contain it.
const names = computed(() => {
  const list = projects.value.map((p) => p.name ?? '').filter(Boolean)
  return model.value && !list.includes(model.value) ? [model.value, ...list] : list
})

onMounted(async () => {
  try {
    // Creating or moving a repository is admin-only in this release, so the
    // picker offers every project.
    projects.value = (await listProjects({ all: true })).projects.filter((p) => p.name !== SYSTEM_PROJECT_NAME)
  } catch {
    projects.value = []
  }
})
</script>

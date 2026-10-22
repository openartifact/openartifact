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
      <option v-if="session.isAdmin" value="">{{ t('repos.project.default') }}</option>
      <option v-for="name in names" :key="name" :value="name">{{ name }}</option>
    </select>
    <p v-if="!session.isAdmin && loaded && names.length === 0" class="text-xs mt-1 text-danger" role="alert">{{ t('repos.project.none') }}</p>
    <p v-else class="text-xs mt-1 text-muted">{{ session.isAdmin ? t('repos.project.hint') : t('repos.project.memberHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { listProjects, SYSTEM_PROJECT_NAME } from '../api/projects'
import type { Project } from '../api/projects'
import { atLeast } from '../lib/projectAccess'
import { t } from '../i18n/en'
import { useSessionStore } from '../stores/session'

defineProps<{ id: string }>()

// Global admins pick any project, or '' for the built-in system project (the
// default). Everyone else picks among the projects they maintain or own.
const model = defineModel<string>({ default: '' })
// The selected project's record (policy included); null for the system project.
const emit = defineEmits<{ select: [project: Project | null] }>()
const session = useSessionStore()
const projects = ref<Project[]>([])
const loaded = ref(false)

// The repository's current project stays selectable even when the list could
// not be loaded or does not contain it.
const names = computed(() => {
  const list = projects.value.map((p) => p.name ?? '').filter(Boolean)
  return model.value && !list.includes(model.value) ? [model.value, ...list] : list
})

function announce(): void {
  emit('select', projects.value.find((p) => p.name === model.value) ?? null)
}

watch(model, announce)

onMounted(async () => {
  try {
    const all = session.isAdmin
    const list = (await listProjects({ all })).projects.filter((p) => p.name !== SYSTEM_PROJECT_NAME)
    projects.value = all ? list : list.filter((p) => atLeast(p.my_role, 'maintainer'))
  } catch {
    projects.value = []
  }
  loaded.value = true
  if (!session.isAdmin && !model.value && names.value.length > 0) model.value = names.value[0]
  announce()
})
</script>

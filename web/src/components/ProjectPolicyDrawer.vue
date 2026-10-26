<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <Drawer
    :open="open"
    :eyebrow="t('adm.projects.edit')"
    :title="tp('adm.projects.editTitle', { name: project?.name ?? '' })"
    :busy="saving"
    @close="emit('close')"
  >
    <form id="project-form" class="space-y-4" @submit.prevent="save">
      <div>
        <p class="text-sm font-semibold text-foreground">{{ t('adm.projects.policy.title') }}</p>
        <p class="text-caption text-muted mt-0.5">{{ t('adm.projects.policy.hint') }}</p>
      </div>

      <fieldset class="space-y-2">
        <legend class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.allowedFormats') }}</legend>
        <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
          <input v-model="form.all_formats" type="checkbox" class="rounded border-border-strong" data-testid="project-all-formats" />
          <span>{{ t('adm.projects.policy.allFormats') }}</span>
        </label>
        <div v-if="!form.all_formats" class="grid grid-cols-2 sm:grid-cols-3 gap-x-4 gap-y-2">
          <label v-for="format in ALL_FORMATS" :key="format" class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
            <input v-model="form.allowed_formats" :value="format" type="checkbox" class="rounded border-border-strong" />
            <span>{{ formatLabel(format) }}</span>
          </label>
        </div>
        <p v-if="!form.all_formats" class="text-caption text-muted">{{ t('adm.projects.policy.allowedFormatsHint') }}</p>
      </fieldset>

      <fieldset class="space-y-2">
        <legend class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.allowedRepoTypes') }}</legend>
        <div class="flex flex-wrap gap-4">
          <label v-for="type in PROJECT_REPO_TYPES" :key="type" class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
            <input v-model="form.allowed_repo_types" :value="type" type="checkbox" class="rounded border-border-strong" />
            <span>{{ t(`adm.projects.policy.repoType.${type}`) }}</span>
          </label>
        </div>
      </fieldset>

      <div>
        <label for="project-name-prefix" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.repoNamePrefix') }}</label>
        <input
          id="project-name-prefix"
          v-model="form.repo_name_prefix"
          type="text"
          class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
        />
        <p class="text-caption text-muted mt-1">{{ t('adm.projects.policy.repoNamePrefixHint') }}</p>
      </div>

      <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
        <input v-model="form.allow_public_repos" type="checkbox" class="rounded border-border-strong" />
        <span>{{ t('adm.projects.policy.allowPublicRepos') }}</span>
      </label>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="project-max-repos" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.maxRepositories') }}</label>
          <input
            id="project-max-repos"
            v-model.number="form.max_repositories"
            type="number"
            min="0"
            step="1"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
          />
          <p class="text-caption text-muted mt-1">{{ t('adm.projects.policy.maxRepositoriesHint') }}</p>
        </div>
        <div>
          <label for="project-token-ttl" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.tokenMaxTtlDays') }}</label>
          <input
            id="project-token-ttl"
            v-model.number="form.token_max_ttl_days"
            type="number"
            min="0"
            step="1"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
          />
          <p class="text-caption text-muted mt-1">{{ t('adm.projects.policy.tokenMaxTtlDaysHint') }}</p>
        </div>
      </div>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="project-form" :loading="saving">{{ t('adm.projects.saveAction') }}</AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import Drawer from './ui/Drawer.vue'
import { PROJECT_REPO_TYPES, updateProject, type Project } from '../api/projects'
import { t, tp } from '../i18n/en'
import { ALL_FORMATS, formatLabel } from '../lib/formats'
import { formPolicy, projectToForm } from '../lib/projectForm'
import { errorStatus } from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'

const props = defineProps<{
  open: boolean
  /** The project whose policy is edited. */
  project: Project | null
}>()

const emit = defineEmits<{
  close: []
  /** The policy was saved on the server. */
  saved: [project: Project]
}>()

const toasts = useToastStore()
const saving = ref(false)
const form = ref(projectToForm({}))

// Each opening starts from the project's current policy.
watch(
  () => props.open,
  (open) => {
    if (open && props.project) form.value = projectToForm(props.project)
  },
)

async function save(): Promise<void> {
  if (saving.value || !props.project?.name) return
  saving.value = true
  try {
    // Only the policy: name and description are edited by owners in General.
    const updated = await updateProject(props.project.name, formPolicy(form.value))
    toasts.success(t('adm.projects.updated'))
    emit('saved', updated)
    emit('close')
  } catch (err) {
    toasts.error(errorStatus(err) === 403 ? t('projects.settings.forbidden') : t('adm.projects.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>

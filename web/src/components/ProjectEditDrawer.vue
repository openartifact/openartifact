<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="open"
    drawer
    max-width="max-w-lg"
    :eyebrow="mode === 'create' ? t('adm.projects.new') : t('adm.projects.edit')"
    :title="mode === 'create' ? t('adm.projects.createTitle') : tp('adm.projects.editTitle', { name: project?.name ?? '' })"
    @close="emit('close')"
  >
    <form id="project-form" class="space-y-4" @submit.prevent="save">
      <div v-if="mode === 'create'">
        <label for="project-name" class="block text-xs font-semibold text-foreground mb-1.5">
          {{ t('adm.projects.field.name') }} <span class="text-danger ml-0.5">*</span>
        </label>
        <input
          id="project-name"
          v-model="form.name"
          type="text"
          required
          placeholder="my-project"
          class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
        />
        <p class="text-caption text-muted mt-1 font-mono">{{ t('adm.projects.field.nameHint') }}</p>
      </div>
      <div>
        <label for="project-display-name" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.field.displayName') }}</label>
        <input
          id="project-display-name"
          v-model="form.display_name"
          type="text"
          class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
        />
      </div>
      <div>
        <label for="project-description" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.field.description') }}</label>
        <textarea
          id="project-description"
          v-model="form.description"
          rows="3"
          class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm placeholder:text-muted focus:ring-2 focus:ring-primary resize-none transition-colors"
        />
      </div>

      <div class="pt-2 border-t border-border">
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
      <button
        type="button"
        class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
        @click="emit('close')"
      >
        {{ t('common.cancel') }}
      </button>
      <AppButton variant="primary" size="sm" type="submit" form="project-form" :loading="saving" :disabled="(mode === 'create' && !form.name.trim())">
        {{ mode === 'create' ? t('adm.projects.createAction') : t('adm.projects.saveAction') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import { PROJECT_REPO_TYPES, createProject, updateProject, type Project } from '../api/projects'
import { t, tp } from '../i18n/en'
import { ALL_FORMATS, formatLabel } from '../lib/formats'
import { emptyProjectForm, formPolicy, projectToForm } from '../lib/projectForm'
import { useToastStore } from '../stores/toast'

const props = defineProps<{
  open: boolean
  mode: 'create' | 'edit'
  /** The project being edited; unused in create mode. */
  project: Project | null
}>()

const emit = defineEmits<{
  close: []
  /** The project was created or updated on the server. */
  saved: []
}>()

const toasts = useToastStore()
const saving = ref(false)
const form = ref(emptyProjectForm())

// Each opening starts from a fresh form (create) or the project's current values (edit).
watch(
  () => props.open,
  (open) => {
    if (!open) return
    form.value = props.mode === 'create' || !props.project ? emptyProjectForm() : projectToForm(props.project)
  },
)

async function save(): Promise<void> {
  if (saving.value) return
  const current = form.value
  if (props.mode === 'create' && !current.name.trim()) return
  saving.value = true
  const policy = formPolicy(current)
  try {
    if (props.mode === 'create') {
      await createProject({
        name: current.name.trim(),
        display_name: current.display_name.trim() || undefined,
        description: current.description.trim() || undefined,
        ...policy,
      })
      toasts.success(t('adm.projects.created'))
    } else if (props.project?.name) {
      // Empty strings are sent on purpose so a field can be cleared.
      await updateProject(props.project.name, {
        display_name: current.display_name.trim(),
        description: current.description.trim(),
        ...policy,
      })
      toasts.success(t('adm.projects.updated'))
    }
    emit('close')
    emit('saved')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>

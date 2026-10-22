<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Settings of a project. Display name and description are editable by owners;
  the policy is always read-only here (global admins change it in the
  administration area); deleting needs an owner and never applies to the
  system project.
-->
<template>
  <div class="space-y-6">
    <!-- General -->
    <section class="rounded-xl border border-border bg-surface p-4 sm:p-5" :aria-label="t('projects.settings.general')">
      <h2 class="text-sm font-semibold text-foreground mb-3">{{ t('projects.settings.general') }}</h2>
      <form v-if="isOwner" class="space-y-4 max-w-xl" data-testid="project-edit" @submit.prevent="save">
        <div>
          <label for="project-display-name" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('projects.settings.displayName') }}</label>
          <input
            id="project-display-name"
            v-model="displayName"
            type="text"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary"
          />
        </div>
        <div>
          <label for="project-description" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('projects.settings.description') }}</label>
          <textarea
            id="project-description"
            v-model="description"
            rows="3"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary resize-none"
          />
        </div>
        <AppButton variant="primary" size="sm" type="submit" :loading="saving" :disabled="!dirty">
          {{ t('projects.settings.save') }}
        </AppButton>
      </form>
      <template v-else>
        <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm">
          <dt class="text-muted">{{ t('projects.settings.displayName') }}</dt>
          <dd class="text-foreground">{{ project.display_name || project.name }}</dd>
          <dt class="text-muted">{{ t('projects.settings.description') }}</dt>
          <dd class="text-foreground">{{ project.description || t('projects.overview.noDescription') }}</dd>
        </dl>
        <p class="mt-3 text-xs text-muted" data-testid="project-readonly">{{ t('projects.settings.readOnly') }}</p>
      </template>
    </section>

    <!-- Policy -->
    <section class="rounded-xl border border-border bg-surface p-4 sm:p-5" :aria-label="t('projects.settings.policy')" data-testid="project-policy">
      <h2 class="text-sm font-semibold text-foreground">{{ t('projects.settings.policy') }}</h2>
      <p class="text-xs text-muted mt-0.5 mb-3">
        {{ t('projects.settings.policyManaged') }}
        <router-link v-if="session.isAdmin" to="/admin/projects" class="text-primary hover:underline">{{ t('projects.settings.policyAdminLink') }}</router-link>
      </p>
      <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm">
        <dt class="text-muted">{{ t('projects.settings.allowedFormats') }}</dt>
        <dd class="text-foreground">{{ formats }}</dd>
        <dt class="text-muted">{{ t('projects.settings.allowedRepoTypes') }}</dt>
        <dd class="text-foreground">{{ (project.allowed_repo_types ?? PROJECT_REPO_TYPES).join(', ') }}</dd>
        <dt class="text-muted">{{ t('projects.settings.repoNamePrefix') }}</dt>
        <dd class="font-mono text-foreground">{{ project.repo_name_prefix || t('projects.settings.none') }}</dd>
        <dt class="text-muted">{{ t('projects.settings.allowPublicRepos') }}</dt>
        <dd class="text-foreground">{{ project.allow_public_repos === false ? t('projects.settings.notAllowed') : t('projects.settings.allowed') }}</dd>
        <dt class="text-muted">{{ t('projects.settings.maxRepositories') }}</dt>
        <dd class="text-foreground">{{ project.max_repositories || t('projects.settings.unlimited') }}</dd>
        <dt class="text-muted">{{ t('projects.settings.tokenMaxTtl') }}</dt>
        <dd class="text-foreground">
          {{ project.token_max_ttl_days ? `${project.token_max_ttl_days} ${t('projects.settings.days')}` : t('projects.settings.noCap') }}
        </dd>
      </dl>
    </section>

    <!-- Quota: edited by global admins only -->
    <ProjectQuotaEditor v-if="project.name" :project="project.name" :editable="session.isAdmin" />

    <!-- Cleanup defaults -->
    <ProjectCleanupDefaults v-if="project.name" :project="project.name" :can-edit="isOwner || session.isAdmin" />

    <!-- Delete -->
    <section v-if="isOwner" class="rounded-xl border border-danger/30 bg-surface p-4 sm:p-5" :aria-label="t('projects.settings.danger')" data-testid="project-danger">
      <h2 class="text-sm font-semibold text-foreground">{{ t('projects.settings.danger') }}</h2>
      <p v-if="project.is_system" class="text-xs text-muted mt-1">{{ t('projects.settings.deleteSystem') }}</p>
      <template v-else>
        <p class="text-xs text-muted mt-1 mb-3">{{ t('projects.settings.deleteHint') }}</p>
        <AppButton variant="danger-outline" size="sm" @click="openDelete">{{ t('projects.settings.deleteAction') }}</AppButton>
      </template>
    </section>

    <ConfirmDialog
      :open="deleteOpen"
      :title="t('projects.settings.deleteTitle')"
      :description="t('projects.settings.deleteHint')"
      :confirm-label="t('projects.settings.deleteAction')"
      :confirm-disabled="confirmName !== project.name"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteOpen = false"
    >
      <label for="project-delete-confirm" class="block text-xs font-semibold text-foreground mb-1.5">
        {{ t('projects.settings.deleteConfirm') }}
        <code class="font-mono text-danger bg-danger-soft px-1 py-0.5 rounded mx-1">{{ project.name }}</code>
      </label>
      <input
        id="project-delete-confirm"
        v-model="confirmName"
        type="text"
        :placeholder="project.name"
        class="w-full px-3 py-2 rounded-lg border border-border-strong bg-surface text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-danger"
      />
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import ProjectCleanupDefaults from './ProjectCleanupDefaults.vue'
import ProjectQuotaEditor from './ProjectQuotaEditor.vue'
import { deleteProject, updateProject, PROJECT_REPO_TYPES, type Project } from '../api/projects'
import { formatLabel } from '../lib/formats'
import { errorStatus } from '../lib/projectAccess'
import { useSessionStore } from '../stores/session'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ project: Project }>()

const emit = defineEmits<{
  (e: 'saved', project: Project): void
  (e: 'deleted'): void
}>()

const session = useSessionStore()
const toasts = useToastStore()

const isOwner = computed(() => props.project.my_role === 'owner')
const formats = computed(() => {
  const list = props.project.allowed_formats
  return list && list.length > 0 ? list.map(formatLabel).join(', ') : t('projects.settings.allFormats')
})

const displayName = ref('')
const description = ref('')
const saving = ref(false)

watch(
  () => props.project,
  (p) => {
    displayName.value = p.display_name ?? ''
    description.value = p.description ?? ''
  },
  { immediate: true },
)

const dirty = computed(
  () =>
    displayName.value.trim() !== (props.project.display_name ?? '') ||
    description.value.trim() !== (props.project.description ?? ''),
)

async function save(): Promise<void> {
  if (!props.project.name || saving.value || !dirty.value) return
  saving.value = true
  try {
    // Empty strings are sent on purpose so a field can be cleared.
    const updated = await updateProject(props.project.name, {
      display_name: displayName.value.trim(),
      description: description.value.trim(),
    })
    toasts.success(t('projects.settings.saved'))
    emit('saved', updated)
  } catch (err) {
    toasts.error(errorStatus(err) === 403 ? t('projects.settings.forbidden') : t('projects.settings.saveFailed'))
  } finally {
    saving.value = false
  }
}

const deleteOpen = ref(false)
const confirmName = ref('')
const deleting = ref(false)

function openDelete(): void {
  confirmName.value = ''
  deleteOpen.value = true
}

async function confirmDelete(): Promise<void> {
  if (!props.project.name || confirmName.value !== props.project.name) return
  deleting.value = true
  try {
    await deleteProject(props.project.name)
    toasts.success(t('projects.settings.deleted'))
    deleteOpen.value = false
    emit('deleted')
  } catch (err) {
    deleteOpen.value = false
    toasts.error(errorStatus(err) === 409 ? t('projects.settings.deleteConflict') : t('projects.settings.deleteFailed'))
  } finally {
    deleting.value = false
  }
}
</script>

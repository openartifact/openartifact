<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="max-w-2xl space-y-6">
    <h2 class="text-lg font-semibold text-foreground">{{ t('nav.settings') }}</h2>
    <form @submit.prevent="handleSave" class="space-y-4 bg-surface border border-border rounded-lg p-6">
      <FormField :label="t('repos.column.name')" id="repo-name">
        <input
          id="repo-name"
          :value="repo.name"
          type="text"
          disabled
          class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-muted"
        />
      </FormField>
      <FormField :label="t('repos.settings.description')" id="repo-description">
        <textarea
          id="repo-description"
          v-model="form.description"
          rows="2"
          class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-foreground focus:ring-2 focus:ring-primary"
        />
      </FormField>
      <div class="flex items-center gap-3">
        <input id="repo-public" v-model="form.public" type="checkbox" class="rounded accent-[rgb(var(--color-primary))]" />
        <label for="repo-public" class="text-sm font-medium text-foreground">{{ t('wizard.public') }}</label>
      </div>
      <FormField :label="t('repos.settings.presign')" id="repo-presign">
        <select
          id="repo-presign"
          v-model="form.presign"
          class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-foreground focus:ring-2 focus:ring-primary"
        >
          <option value="inherit">{{ t('repos.settings.presign.inherit') }}</option>
          <option value="always">{{ t('repos.settings.presign.always') }}</option>
          <option value="never">{{ t('repos.settings.presign.never') }}</option>
        </select>
        <p class="text-caption text-muted mt-1">{{ t('repos.settings.presign.hint') }}</p>
      </FormField>
      <FormField :label="t('repos.settings.trashRetention')" id="repo-trash-retention" :hint="t('repos.settings.trashRetentionHint')" :error="retentionError">
        <input
          id="repo-trash-retention"
          v-model="form.trashRetention"
          type="number"
          min="0"
          step="1"
          :placeholder="t('repos.settings.trashRetentionPlaceholder')"
          class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-foreground focus:ring-2 focus:ring-primary"
        />
      </FormField>
      <div class="space-y-1">
        <div class="flex items-center gap-3">
          <input id="repo-immutable" v-model="form.immutableReleases" type="checkbox" class="rounded accent-[rgb(var(--color-primary))]" />
          <label for="repo-immutable" class="text-sm font-medium text-foreground">{{ t('repos.settings.immutableReleases') }}</label>
        </div>
        <p class="text-xs text-muted">{{ t('repos.settings.immutableReleasesHint') }}</p>
      </div>
      <div v-if="repo.repo_type === 'group'" class="space-y-1">
        <div class="flex items-center gap-3">
          <input
            id="repo-merge-proxy"
            v-model="form.mergeProxyVersions"
            type="checkbox"
            :disabled="!session.isAdmin"
            class="rounded accent-[rgb(var(--color-primary))]"
          />
          <label for="repo-merge-proxy" class="text-sm font-medium text-foreground">{{ t('repos.settings.mergeProxyVersions') }}</label>
        </div>
        <p class="text-xs text-muted">{{ t('repos.settings.mergeProxyVersionsHint') }}</p>
        <p v-if="!session.isAdmin" class="text-xs text-muted">{{ t('repos.settings.mergeProxyVersionsAdminOnly') }}</p>
      </div>
      <AppButton type="submit" :disabled="!!retentionError" :loading="loading">
        {{ loading ? t('common.saving') : t('common.save') }}
      </AppButton>
    </form>

    <!-- Storage Quota -->
    <div class="bg-surface border border-border rounded-lg p-6 space-y-4">
      <h3 class="text-sm font-semibold text-foreground">{{ t('repos.settings.quota') }}</h3>
      <RepoQuotaTab :repo-name="repo.name" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../components/AppButton.vue'
import type { Repository } from '../api/repositories'
import { presignModeOf, presignValueOf, updateRepository } from '../api/repositories'
import type { PresignMode } from '../api/repositories'
import FormField from '../components/FormField.vue'
import RepoQuotaTab from './RepoQuotaTab.vue'
import { useToastStore } from '../stores/toast'
import { parseRetentionInput } from '../lib/trash'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n/en'

const session = useSessionStore()
const props = defineProps<{ repo: Repository }>()
const emit = defineEmits<{ saved: [repo: Repository] }>()

interface SettingsForm {
  description: string
  public: boolean
  presign: PresignMode
  trashRetention: string
  immutableReleases: boolean
  mergeProxyVersions: boolean
}

function formFrom(r: Repository): SettingsForm {
  return {
    description: r.description ?? '',
    public: r.public ?? false,
    presign: presignModeOf(r),
    trashRetention: r.trash_retention_days == null ? '' : String(r.trash_retention_days),
    immutableReleases: r.immutable_releases ?? false,
    mergeProxyVersions: r.merge_proxy_versions ?? false,
  }
}

const form = ref<SettingsForm>(formFrom(props.repo))
const retentionError = computed(() =>
  parseRetentionInput(form.value.trashRetention) === undefined ? t('repos.settings.trashRetentionInvalid') : '',
)
const loading = ref(false)
const toasts = useToastStore()

watch(
  () => props.repo,
  (r) => {
    form.value = formFrom(r)
  },
)

async function handleSave(): Promise<void> {
  loading.value = true
  try {
    const updated = await updateRepository(props.repo.name, {
      description: form.value.description,
      public: form.value.public,
      presign_downloads: presignValueOf(form.value.presign),
      // Explicit null resets to the server default.
      trash_retention_days: parseRetentionInput(form.value.trashRetention) ?? null,
      immutable_releases: form.value.immutableReleases,
      // The API answers 403 to non-admins that send the field, so only admins send it.
      ...(props.repo.repo_type === 'group' && session.isAdmin ? { merge_proxy_versions: form.value.mergeProxyVersions } : {}),
    })
    toasts.success(t('common.save'))
    emit('saved', updated)
  } catch (err: unknown) {
    toasts.error(err instanceof Error ? err.message : t('repos.settings.saveFailed'))
  } finally {
    loading.value = false
  }
}
</script>

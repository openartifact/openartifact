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
          class="w-full px-3 py-2 border border-border rounded-md text-sm bg-bg text-muted"
        />
      </FormField>
      <FormField :label="t('repos.settings.description')" id="repo-description">
        <textarea
          id="repo-description"
          v-model="form.description"
          rows="2"
          class="w-full px-3 py-2 border border-border rounded-md text-sm bg-bg text-foreground focus:outline-none focus:ring-2 focus:ring-primary"
        />
      </FormField>
      <div class="flex items-center gap-3">
        <input id="repo-public" v-model="form.public" type="checkbox" class="rounded" />
        <label for="repo-public" class="text-sm font-medium text-foreground">{{ t('wizard.public') }}</label>
      </div>
      <button
        type="submit"
        :disabled="loading"
        class="py-2 px-4 bg-primary text-primary-contrast text-sm font-medium rounded-md hover:opacity-90 disabled:opacity-50"
      >
        {{ loading ? t('common.saving') : t('common.save') }}
      </button>
    </form>

    <!-- Storage Quota -->
    <div class="bg-surface border border-border rounded-lg p-6 space-y-4">
      <h3 class="text-sm font-semibold text-foreground">{{ t('repos.settings.quota') }}</h3>
      <RepoQuotaTab :repo-name="repo.name" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import type { Repository } from '../api/repositories'
import { updateRepository } from '../api/repositories'
import FormField from '../components/FormField.vue'
import RepoQuotaTab from './RepoQuotaTab.vue'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ repo: Repository }>()
const emit = defineEmits<{ saved: [repo: Repository] }>()

const form = ref({ description: props.repo.description ?? '', public: props.repo.public ?? false })
const loading = ref(false)
const toasts = useToastStore()

watch(
  () => props.repo,
  (r) => {
    form.value = { description: r.description ?? '', public: r.public ?? false }
  },
)

async function handleSave(): Promise<void> {
  loading.value = true
  try {
    const updated = await updateRepository(props.repo.name, form.value)
    toasts.success(t('common.save'))
    emit('saved', updated)
  } catch (err: unknown) {
    toasts.error(err instanceof Error ? err.message : t('repos.settings.saveFailed'))
  } finally {
    loading.value = false
  }
}
</script>

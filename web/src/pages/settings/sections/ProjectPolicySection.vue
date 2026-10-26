<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Access > Project policy: who may create projects, and what project roles grant. -->
<template>
  <div class="space-y-5">
    <div v-if="loading" class="rounded-lg border border-border bg-surface px-4 py-5 text-sm text-muted" role="status">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="loadError" class="rounded-lg border border-border bg-surface">
      <StateBlock variant="error" :hint="loadError">
        <template #action>
          <AppButton variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton>
        </template>
      </StateBlock>
    </div>
    <section
      v-else
      class="flex items-start justify-between gap-4 rounded-lg border border-border bg-surface p-4"
      data-testid="self-create"
    >
      <div class="min-w-0">
        <h2 id="self-create-label" class="text-sm font-semibold text-foreground">{{ t('sx.pp.selfCreate.title') }}</h2>
        <p id="self-create-hint" class="mt-0.5 text-sm text-muted">{{ t('sx.pp.selfCreate.hint') }}</p>
        <p v-if="saveError" class="mt-1 text-sm text-danger" role="alert">{{ saveError }}</p>
      </div>
      <AppToggle
        :model-value="selfCreate"
        labelledby="self-create-label"
        :disabled="saving"
        @update:model-value="save"
      />
    </section>

    <section class="rounded-lg border border-border bg-surface p-4" data-testid="per-project">
      <h2 class="text-sm font-semibold text-foreground">{{ t('sx.pp.perProject.title') }}</h2>
      <p class="mt-0.5 text-sm text-muted">{{ t('sx.pp.perProject.hint') }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3 max-md:h-11" to="/projects">{{ t('sx.pp.perProject.link') }}</AppButton>
    </section>

    <section class="space-y-1.5 rounded-lg border border-border bg-surface p-4 text-sm text-muted" data-testid="project-effect">
      <h2 class="text-sm font-semibold text-foreground">{{ t('sx.pp.effect.title') }}</h2>
      <p>{{ t('sx.pp.effect.viewer') }}</p>
      <p>{{ t('sx.pp.effect.developer') }}</p>
      <p>{{ t('sx.pp.effect.maintainer') }}</p>
      <p>{{ t('sx.pp.effect.limits') }}</p>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import AppButton from '../../../components/AppButton.vue'
import AppToggle from '../../../components/AppToggle.vue'
import StateBlock from '../../../components/ui/StateBlock.vue'
import { getProjectSettings, updateProjectSettings } from '../../../api/projects'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n/en'

const toasts = useToastStore()

const selfCreate = ref(false)
const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const saveError = ref('')

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    selfCreate.value = (await getProjectSettings()).self_create === true
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('sx.pp.selfCreate.loadFailed')
  } finally {
    loading.value = false
  }
}

async function save(value: boolean): Promise<void> {
  saving.value = true
  saveError.value = ''
  try {
    selfCreate.value = (await updateProjectSettings({ self_create: value })).self_create === true
    toasts.success(t('sx.pp.selfCreate.saved'))
  } catch (err) {
    saveError.value = err instanceof Error ? err.message : t('sx.pp.selfCreate.saveFailed')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

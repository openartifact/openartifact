<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <SectionActions>
        <div class="flex items-center gap-2 shrink-0">
          <AppButton variant="secondary" size="sm" :loading="testing" :disabled="loading || !!loadError" @click="runTest">
            {{ t('adm.dt.test') }}
          </AppButton>
          <AppButton size="sm" :loading="saving" :disabled="loading || !!loadError" @click="save()">
            {{ saving ? t('common.saving') : t('adm.dt.save') }}
          </AppButton>
        </div>
      </SectionActions>

    <div v-if="loading" class="text-muted" role="status">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="bg-surface rounded-xl border border-border p-8 text-center text-danger" role="alert">{{ loadError }}</div>

    <template v-else>
      <section class="bg-surface rounded-xl border border-border p-5 space-y-4" :aria-label="t('adm.dt.global')">
        <h3 class="text-sm font-semibold text-foreground">{{ t('adm.dt.global') }}</h3>

        <div class="flex items-start gap-4">
          <AppToggle v-model="form.enabled" labelledby="dt-enable-label" class="mt-0.5" />
          <div class="min-w-0">
            <p id="dt-enable-label" class="text-sm font-semibold text-foreground leading-tight">{{ t('adm.dt.enable') }}</p>
            <p class="text-xs text-muted mt-0.5">{{ t('adm.dt.enableHint') }}</p>
          </div>
        </div>

        <div class="grid grid-cols-2 gap-x-4 gap-y-4">
          <DependencyTrackFields v-model:form="form" v-model:api-key="apiKey" global :api-key-set="apiKeySet" :saving="saving" @remove-key="removeKey" />
        </div>

        <div class="flex items-start gap-4">
          <AppToggle v-model="form.auto_push" labelledby="dt-auto-label" class="mt-0.5" />
          <div class="min-w-0">
            <p id="dt-auto-label" class="text-sm font-semibold text-foreground leading-tight">{{ t('adm.dt.autoPush') }}</p>
            <p class="text-xs text-muted mt-0.5">{{ t('adm.dt.autoPushHint') }}</p>
          </div>
        </div>
        <div class="flex items-start gap-4">
          <AppToggle v-model="form.push_vex" labelledby="dt-vex-label" class="mt-0.5" />
          <div class="min-w-0">
            <p id="dt-vex-label" class="text-sm font-semibold text-foreground leading-tight">{{ t('adm.dt.pushVex') }}</p>
            <p class="text-xs text-muted mt-0.5">{{ t('adm.dt.pushVexHint') }}</p>
          </div>
        </div>
      </section>

      <DependencyTrackTestResult :result="testResult" :error="testError" />

      <DependencyTrackRepoPanel />
    </template>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import SectionActions from '../../../components/SectionActions.vue'
import AppButton from '../../../components/AppButton.vue'
import AppToggle from '../../../components/AppToggle.vue'
import DependencyTrackFields from './DependencyTrackFields.vue'
import DependencyTrackTestResult from './DependencyTrackTestResult.vue'
import DependencyTrackRepoPanel from './DependencyTrackRepoPanel.vue'
import { useToastStore } from '../../../stores/toast'
import {
  getDependencyTrackSettings,
  saveDependencyTrackSettings,
  testDependencyTrack,
} from '../../../api/dependencyTrack'
import type { DependencyTrackSettings, DependencyTrackTest } from '../../../api/dependencyTrack'
import { t } from '../../../i18n'

const toasts = useToastStore()

const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const testing = ref(false)
const testResult = ref<DependencyTrackTest | null>(null)
const testError = ref('')

const form = reactive({
  enabled: false,
  base_url: '',
  project_name: '',
  project_version: '',
  auto_push: false,
  push_vex: false,
})
// Write-only: empty means "keep the stored key".
const apiKey = ref('')
const apiKeySet = ref(false)

function apply(s: DependencyTrackSettings): void {
  form.enabled = s.enabled ?? false
  form.base_url = s.base_url ?? ''
  form.project_name = s.project_name ?? ''
  form.project_version = s.project_version ?? ''
  form.auto_push = s.auto_push ?? false
  form.push_vex = s.push_vex ?? false
  apiKeySet.value = s.api_key_set ?? false
  apiKey.value = ''
}

/** `key` undefined keeps the stored key, `''` clears it. */
async function save(key?: string): Promise<boolean> {
  if (saving.value) return false
  saving.value = true
  try {
    const typed = key ?? (apiKey.value !== '' ? apiKey.value : undefined)
    const res = await saveDependencyTrackSettings({ ...form, ...(typed !== undefined ? { api_key: typed } : {}) })
    apply(res)
    toasts.success(t('adm.dt.saved'))
    return true
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.dt.saveFailed'))
    return false
  } finally {
    saving.value = false
  }
}

async function removeKey(): Promise<void> {
  await save('')
}

async function runTest(): Promise<void> {
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    testResult.value = await testDependencyTrack()
  } catch (err) {
    testError.value = err instanceof Error ? err.message : t('adm.dt.testFailed')
  } finally {
    testing.value = false
  }
}

onMounted(async () => {
  try {
    apply(await getDependencyTrackSettings())
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.dt.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
})
</script>

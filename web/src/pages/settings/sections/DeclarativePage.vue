<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > System > Declarative config: `oa diff`, `oa apply` and `oa export` from the browser. -->
<template>
  <div class="space-y-5">
    <p class="text-sm text-muted">
      {{ t('sx.declarative.intro') }}
      <DocsLink :url="docsUrl" />
    </p>

    <CardSection :title="t('sx.declarative.manifest')" :description="t('sx.declarative.manifest.hint')">
      <div class="space-y-4 px-5 py-4">
        <div class="flex flex-wrap items-center gap-2">
          <input ref="fileInput" type="file" multiple accept=".yaml,.yml,.json,text/yaml,application/yaml" class="sr-only" data-testid="manifest-file" tabindex="-1" @change="onFiles" />
          <AppButton variant="secondary" size="sm" @click="fileInput?.click()">
            <ArrowUpTrayIcon class="h-4 w-4" aria-hidden="true" />
            {{ t('sx.declarative.upload') }}
          </AppButton>
          <AppButton variant="secondary" size="sm" :loading="state.busy.value === 'export'" @click="state.loadLive()">
            {{ t('sx.declarative.loadLive') }}
          </AppButton>
          <AppButton variant="secondary" size="sm" :disabled="state.busy.value === 'export'" @click="state.download()">
            <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
            {{ t('sx.declarative.export') }}
          </AppButton>
          <label class="flex items-center gap-1.5 text-sm text-foreground">
            <input v-model="state.withMembers.value" type="checkbox" class="h-4 w-4 rounded border-border-strong" data-testid="opt-members" />
            {{ t('sx.declarative.withMembers') }}
          </label>
          <AppButton v-if="state.hasManifest.value" class="ml-auto" variant="ghost" size="sm" @click="state.manifest.value = ''">
            {{ t('sx.declarative.clear') }}
          </AppButton>
        </div>

        <textarea
          v-model="state.manifest.value"
          rows="14"
          spellcheck="false"
          :aria-label="t('sx.declarative.manifest')"
          :placeholder="t('sx.declarative.placeholder')"
          :class="[MONO_INPUT_CLASS, 'resize-y']"
          data-testid="manifest-text"
        />

        <DeclarativeOptions
          v-model:kinds="state.kinds.value"
          v-model:prune="state.prune.value"
          v-model:prune-all="state.pruneAll.value"
          v-model:force-secrets="state.forceSecrets.value"
          v-model:continue-on-error="state.continueOnError.value"
        />

        <div class="flex flex-wrap items-center gap-2">
          <AppButton :disabled="!state.hasManifest.value || state.busy.value !== null" :loading="state.busy.value === 'diff'" data-testid="review" @click="state.review()">
            {{ t('sx.declarative.review') }}
          </AppButton>
          <AppButton
            :variant="removals.length ? 'danger-outline' : 'secondary'"
            :disabled="!state.applicable.value || state.busy.value !== null"
            :loading="state.busy.value === 'apply'"
            data-testid="apply"
            @click="confirmApply = true"
          >
            {{ t('sx.declarative.apply') }}
          </AppButton>
          <span v-if="state.hasManifest.value && !state.plan.value" class="text-xs text-muted">{{ t('sx.declarative.reviewFirst') }}</span>
        </div>
      </div>
    </CardSection>

    <p v-if="state.error.value && !state.plan.value" class="rounded-lg border border-danger bg-danger-soft px-4 py-3 text-sm text-danger" role="alert" data-testid="declarative-error">
      {{ state.error.value }}
    </p>

    <DeclarativePlanView v-if="state.plan.value" :plan="state.plan.value" :applied="state.applied.value" :error="state.error.value" />

    <DeclarativeSecrets v-if="needsSecrets" v-model="state.secrets.value" :secrets="state.plan.value?.secrets ?? []" />

    <DeclarativeManaged :rows="managed" :loading="managedLoading" :error="managedError" @release="releasing = $event" @retry="reloadManaged" />

    <ConfirmDialog
      :open="confirmApply"
      :title="t('sx.declarative.confirm.title')"
      :description="confirmText"
      :confirm-label="t('sx.declarative.apply')"
      :busy="state.busy.value === 'apply'"
      :tone="removals.length ? 'danger' : 'primary'"
      @close="confirmApply = false"
      @confirm="runApply"
    >
      <ul v-if="removals.length" class="list-disc space-y-0.5 pl-5 text-foreground" data-testid="confirm-deletions">
        <li v-for="d in removals" :key="`${d.kind}/${d.name}`" class="break-all font-mono text-xs">{{ d.kind }} {{ d.name }}</li>
      </ul>
    </ConfirmDialog>

    <ConfirmDialog
      :open="releasing !== null"
      :title="t('sx.declarative.release.title')"
      :description="tp('sx.declarative.release.body', { name: releasing?.name ?? '' })"
      :confirm-label="t('sx.declarative.managed.release')"
      :busy="releaseBusy"
      @close="releasing = null"
      @confirm="release"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowDownTrayIcon, ArrowUpTrayIcon } from '@heroicons/vue/20/solid'
import { listManaged, releaseManaged, type DeclarativeObject } from '../../../api/declarative'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import DeclarativeManaged from '../../../components/DeclarativeManaged.vue'
import DeclarativeOptions from '../../../components/DeclarativeOptions.vue'
import DeclarativePlanView from '../../../components/DeclarativePlanView.vue'
import DeclarativeSecrets from '../../../components/DeclarativeSecrets.vue'
import DocsLink from '../../../components/DocsLink.vue'
import CardSection from '../../../components/ui/CardSection.vue'
import { useDeclarative } from '../../../composables/useDeclarative'
import { useDocsLink } from '../../../composables/useDocsLink'
import { useLoadable } from '../../../composables/useLoadable'
import { deletions } from '../../../lib/declarativePlan'
import { MONO_INPUT_CLASS } from '../../../lib/formClasses'
import { useToastStore } from '../../../stores/toast'
import { t, tp } from '../../../i18n'

const toasts = useToastStore()
const docsUrl = useDocsLink('settings-declarative')

const { data: managed, loading: managedLoading, error: managedError, reload: reloadManaged } = useLoadable<DeclarativeObject[]>(listManaged, [])
const state = useDeclarative(() => void reloadManaged())

const fileInput = ref<HTMLInputElement | null>(null)
const confirmApply = ref(false)
const releasing = ref<DeclarativeObject | null>(null)
const releaseBusy = ref(false)

const removals = computed(() => (state.plan.value && !state.applied.value ? deletions(state.plan.value) : []))
const needsSecrets = computed(() => (state.plan.value?.secrets.length ?? 0) > 0)
const confirmText = computed(() =>
  removals.value.length > 0
    ? t('sx.declarative.confirm.withDeletes')
    : t('sx.declarative.confirm.body'),
)

async function onFiles(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement
  await state.readFiles(Array.from(input.files ?? []))
  input.value = ''
}

async function runApply(): Promise<void> {
  await state.apply()
  confirmApply.value = false
}

async function release(): Promise<void> {
  const row = releasing.value
  if (!row) return
  releaseBusy.value = true
  try {
    await releaseManaged(row.kind, row.name)
    toasts.success(tp('sx.declarative.released', { name: row.name }))
    releasing.value = null
    await reloadManaged()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sx.declarative.failed'))
  } finally {
    releaseBusy.value = false
  }
}
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The promotion pipelines: every bundle moves through the stages of one, in order. The drawer lists
  them and defines new ones (an ordered list of stages, each optionally bound to a repository).
-->
<template>
  <Drawer
    :open="open"
    :title="view === 'form' ? (editing ? tp('bundles.pipeline.edit', { pipeline: editing }) : t('bundles.pipeline.new')) : t('bundles.pipelines.title')"
    :eyebrow="t('bundles.nav')"
    :busy="busy"
    @close="emit('close')"
  >
    <form v-if="view === 'form'" id="pipeline-form" class="space-y-4" @submit.prevent="submit">
      <FormField :label="t('bundles.pipeline.name')" id="pipeline-name" :hint="t('bundles.create.labelHint')">
        <input id="pipeline-name" v-model="name" required :readonly="editing !== null" type="text" :class="[INPUT_CLASS, 'font-mono']" autocomplete="off" />
      </FormField>
      <FormField :label="`${t('bundles.pipeline.description')} (${t('common.optional')})`" id="pipeline-description">
        <input id="pipeline-description" v-model="description" type="text" :class="INPUT_CLASS" autocomplete="off" />
      </FormField>

      <fieldset class="space-y-3">
        <legend class="mb-1 text-sm font-medium text-foreground">{{ t('bundles.pipeline.stages') }}</legend>
        <div v-for="(stage, i) in stages" :key="i" class="space-y-2" data-testid="pipeline-stage">
          <div class="flex items-end gap-2">
            <div class="min-w-0 flex-1">
              <label :for="`stage-name-${i}`" class="mb-1 block text-xs text-muted">{{ t('bundles.pipeline.stageName') }}</label>
              <input :id="`stage-name-${i}`" v-model="stage.name" required type="text" :class="INPUT_CLASS" autocomplete="off" />
            </div>
            <div class="min-w-0 flex-1">
              <label :for="`stage-repo-${i}`" class="mb-1 block text-xs text-muted">{{ t('bundles.pipeline.stageRepo') }}</label>
              <select :id="`stage-repo-${i}`" v-model="stage.repo_id" :class="INPUT_CLASS">
                <option value="">{{ t('bundles.pipeline.anyRepo') }}</option>
                <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}</option>
              </select>
            </div>
            <button
              type="button"
              class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-muted hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-50 max-md:h-11 max-md:w-11"
              :disabled="stages.length === 1"
              :aria-label="`${t('bundles.pipeline.removeStage')} ${i + 1}`"
              @click="stages.splice(i, 1)"
            >
              <XMarkIcon class="h-4 w-4" aria-hidden="true" />
            </button>
          </div>
          <div>
            <label :for="`stage-checks-${i}`" class="mb-1 block text-xs text-muted">{{ t('bundles.pipeline.stageChecks') }}</label>
            <input
              :id="`stage-checks-${i}`"
              v-model="stage.checks"
              type="text"
              :class="[INPUT_CLASS, 'font-mono']"
              :aria-invalid="checksError(stage) !== null || undefined"
              :aria-describedby="`stage-checks-hint-${i}`"
              :placeholder="t('bundles.pipeline.stageChecksPlaceholder')"
              autocomplete="off"
              spellcheck="false"
            />
            <p :id="`stage-checks-hint-${i}`" class="mt-1 text-xs" :class="checksError(stage) ? 'text-danger' : 'text-muted'" :role="checksError(stage) ? 'alert' : undefined">
              {{ checksError(stage) ?? t('bundles.pipeline.stageChecksHint') }}
            </p>
          </div>
        </div>
        <AppButton variant="secondary" size="sm" class="max-md:h-11" @click="stages.push(blankStage())">
          <PlusIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('bundles.pipeline.addStage') }}
        </AppButton>
      </fieldset>
      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    </form>

    <div v-else class="space-y-4">
      <p class="text-sm text-muted">{{ t('bundles.pipelines.intro') }}</p>
      <StateBlock v-if="pipelines.length === 0" :title="t('bundles.pipelines.empty')" :hint="t('bundles.pipelines.emptyHint')" />
      <ul v-else class="space-y-3" data-testid="pipeline-list">
        <li v-for="p in pipelines" :key="p.id" class="rounded-lg border border-border p-3">
          <div class="flex items-center justify-between gap-2">
            <p class="min-w-0 truncate font-mono text-sm font-semibold text-foreground">{{ p.name }}</p>
            <RowActions :actions="[{ key: 'edit', label: t('common.edit'), icon: PencilSquareIcon }]" :subject="p.name" @action="startEdit(p)" />
          </div>
          <p v-if="p.description" class="mt-0.5 text-sm text-muted">{{ p.description }}</p>
          <ol class="mt-2 space-y-1" :aria-label="tp('bundles.pipelines.stagesOf', { pipeline: p.name })">
            <li v-for="(s, i) in p.stages" :key="s.name" class="flex flex-wrap items-baseline gap-x-2 text-sm">
              <span class="font-mono text-xs text-muted">{{ i + 1 }}</span>
              <span class="font-mono text-foreground">{{ s.name }}</span>
              <span class="text-xs text-muted">{{ s.repo_id ? tp('bundles.pipelines.into', { repo: repoName(s.repo_id) }) : t('bundles.pipeline.anyRepo') }}</span>
              <span v-if="s.required_checks?.length" class="text-xs text-muted" data-testid="stage-required-checks">
                {{ t('bundles.pipelines.requires') }}
                <span class="font-mono">{{ s.required_checks.join(', ') }}</span>
              </span>
            </li>
          </ol>
        </li>
      </ul>
    </div>

    <template #footer>
      <template v-if="view === 'form'">
        <AppButton variant="secondary" size="sm" :disabled="busy" @click="view = 'list'">{{ t('bundles.pipelines.back') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="pipeline-form" :loading="busy">{{ editing ? t('common.save') : t('common.create') }}</AppButton>
      </template>
      <template v-else>
        <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.close') }}</AppButton>
        <AppButton variant="primary" size="sm" @click="startCreate">
          <PlusIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('bundles.pipeline.new') }}
        </AppButton>
      </template>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { PencilSquareIcon, PlusIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import RowActions from '../ui/RowActions.vue'
import StateBlock from '../ui/StateBlock.vue'
import { createPipeline, updatePipeline, type Pipeline, type PipelineStage } from '../../api/bundles'
import { listAllRepositories, type Repository } from '../../api/repositories'
import { parseRequiredChecks } from '../../lib/bundles'
import { INPUT_CLASS } from '../../lib/formClasses'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const props = defineProps<{ open: boolean; pipelines: Pipeline[] }>()
const emit = defineEmits<{ close: []; saved: [pipeline: Pipeline] }>()

const toasts = useToastStore()
const view = ref<'list' | 'form'>('list')
/** Name of the pipeline being edited; null while a new one is defined. */
const editing = ref<string | null>(null)
const repos = ref<Repository[]>([])
const name = ref('')
const description = ref('')
interface StageDraft {
  name: string
  repo_id: string
  /** Required check names as typed: separated by commas and/or spaces. */
  checks: string
}
const blankStage = (): StageDraft => ({ name: '', repo_id: '', checks: '' })
const stages = ref<StageDraft[]>([blankStage()])
const busy = ref(false)
const error = ref('')

const repoName = (id: string): string => repos.value.find((r) => r.id === id)?.name ?? id

/** The reason a stage's required checks are refused, null when fine. */
const checksError = (s: StageDraft): string | null => parseRequiredChecks(s.checks).error

function startCreate(): void {
  editing.value = null
  name.value = ''
  description.value = ''
  stages.value = [blankStage()]
  error.value = ''
  view.value = 'form'
}

function startEdit(p: Pipeline): void {
  editing.value = p.name
  name.value = p.name
  description.value = p.description
  stages.value = p.stages.map((s) => ({ name: s.name, repo_id: s.repo_id ?? '', checks: (s.required_checks ?? []).join(', ') }))
  error.value = ''
  view.value = 'form'
}

function toStages(): PipelineStage[] {
  return stages.value.map((s) => {
    const stage: PipelineStage = { name: s.name }
    if (s.repo_id) stage.repo_id = s.repo_id
    const { names } = parseRequiredChecks(s.checks)
    // On an edit an empty list is sent too: it is how a stage drops its checks.
    if (names.length > 0 || editing.value !== null) stage.required_checks = names
    return stage
  })
}

// The stage repositories only matter once the drawer opens; a failure leaves ids where names would be.
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    view.value = 'list'
    repos.value = await listAllRepositories().catch(() => [] as Repository[])
  },
)

async function submit(): Promise<void> {
  if (busy.value) return
  const invalid = stages.value.map(checksError).find((e) => e !== null)
  if (invalid) {
    error.value = invalid
    return
  }
  busy.value = true
  error.value = ''
  const wasEditing = editing.value !== null
  try {
    const pipeline = wasEditing
      ? await updatePipeline(editing.value!, { description: description.value.trim(), stages: toStages() })
      : await createPipeline({ name: name.value, description: description.value.trim() || undefined, stages: toStages() })
    toasts.success(wasEditing ? t('bundles.pipeline.updated') : t('bundles.pipeline.done'))
    view.value = 'list'
    emit('saved', pipeline)
  } catch (err) {
    error.value = err instanceof Error ? err.message : wasEditing ? t('bundles.pipeline.updateFailed') : t('bundles.pipeline.failed')
  } finally {
    busy.value = false
  }
}
</script>

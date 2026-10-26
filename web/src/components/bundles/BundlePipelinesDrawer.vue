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
    :title="view === 'create' ? t('bundles.pipeline.new') : t('bundles.pipelines.title')"
    :eyebrow="t('bundles.nav')"
    :busy="busy"
    @close="emit('close')"
  >
    <form v-if="view === 'create'" id="pipeline-create-form" class="space-y-4" @submit.prevent="submit">
      <FormField :label="t('bundles.pipeline.name')" id="pipeline-name" :hint="t('bundles.create.labelHint')">
        <input id="pipeline-name" v-model="name" required type="text" :class="[INPUT_CLASS, 'font-mono']" autocomplete="off" />
      </FormField>
      <FormField :label="`${t('bundles.pipeline.description')} (${t('common.optional')})`" id="pipeline-description">
        <input id="pipeline-description" v-model="description" type="text" :class="INPUT_CLASS" autocomplete="off" />
      </FormField>

      <fieldset class="space-y-3">
        <legend class="mb-1 text-sm font-medium text-foreground">{{ t('bundles.pipeline.stages') }}</legend>
        <div v-for="(stage, i) in stages" :key="i" class="flex items-end gap-2" data-testid="pipeline-stage">
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
        <AppButton variant="secondary" size="sm" class="max-md:h-11" @click="stages.push({ name: '', repo_id: '' })">
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
          <p class="font-mono text-sm font-semibold text-foreground">{{ p.name }}</p>
          <p v-if="p.description" class="mt-0.5 text-sm text-muted">{{ p.description }}</p>
          <ol class="mt-2 space-y-1" :aria-label="tp('bundles.pipelines.stagesOf', { pipeline: p.name })">
            <li v-for="(s, i) in p.stages" :key="s.name" class="flex flex-wrap items-baseline gap-x-2 text-sm">
              <span class="font-mono text-xs text-muted">{{ i + 1 }}</span>
              <span class="font-mono text-foreground">{{ s.name }}</span>
              <span class="text-xs text-muted">{{ s.repo_id ? tp('bundles.pipelines.into', { repo: repoName(s.repo_id) }) : t('bundles.pipeline.anyRepo') }}</span>
            </li>
          </ol>
        </li>
      </ul>
    </div>

    <template #footer>
      <template v-if="view === 'create'">
        <AppButton variant="secondary" size="sm" :disabled="busy" @click="view = 'list'">{{ t('bundles.pipelines.back') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="pipeline-create-form" :loading="busy">{{ t('common.create') }}</AppButton>
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
import { PlusIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import StateBlock from '../ui/StateBlock.vue'
import { createPipeline, type Pipeline } from '../../api/bundles'
import { listAllRepositories, type Repository } from '../../api/repositories'
import { INPUT_CLASS } from '../../lib/formClasses'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const props = defineProps<{ open: boolean; pipelines: Pipeline[] }>()
const emit = defineEmits<{ close: []; created: [pipeline: Pipeline] }>()

const toasts = useToastStore()
const view = ref<'list' | 'create'>('list')
const repos = ref<Repository[]>([])
const name = ref('')
const description = ref('')
const stages = ref<{ name: string; repo_id: string }[]>([{ name: '', repo_id: '' }])
const busy = ref(false)
const error = ref('')

const repoName = (id: string): string => repos.value.find((r) => r.id === id)?.name ?? id

function startCreate(): void {
  name.value = ''
  description.value = ''
  stages.value = [{ name: '', repo_id: '' }]
  error.value = ''
  view.value = 'create'
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
  busy.value = true
  error.value = ''
  try {
    const pipeline = await createPipeline({
      name: name.value,
      description: description.value.trim() || undefined,
      stages: stages.value.map((s) => (s.repo_id ? { name: s.name, repo_id: s.repo_id } : { name: s.name })),
    })
    toasts.success(t('bundles.pipeline.done'))
    view.value = 'list'
    emit('created', pipeline)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('bundles.pipeline.failed')
  } finally {
    busy.value = false
  }
}
</script>

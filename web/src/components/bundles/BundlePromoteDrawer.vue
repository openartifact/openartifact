<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Promote to the next stage: copies the bundle's artifacts into the stage's repository. Open
  vulnerability findings of the source repositories are shown first; they do not block the
  promotion, the reviewer decides.
-->
<template>
  <Drawer :open="open" :title="tp('bundles.promote.title', { stage: toStage })" :eyebrow="`${bundle.name} ${bundle.version}`" :busy="busy" @close="emit('close')">
    <form id="bundle-promote-form" class="space-y-4" @submit.prevent="submit">
      <section :aria-label="t('bundles.promote.scans')" data-testid="promote-scans">
        <h3 class="mb-1.5 text-sm font-medium text-foreground">{{ t('bundles.promote.scans') }}</h3>
        <p v-if="scans.length === 0" class="text-sm text-muted">{{ t('bundles.promote.noScans') }}</p>
        <template v-else>
          <dl class="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
            <dt class="text-muted">{{ t('bundles.promote.itemCount') }}</dt>
            <dd class="tabular-nums text-foreground">{{ itemCount }}</dd>
            <dt class="text-muted">{{ t('bundles.scans.critical') }}</dt>
            <dd class="tabular-nums" :class="critical > 0 ? 'font-medium text-danger' : 'text-foreground'">{{ critical }}</dd>
            <dt class="text-muted">{{ t('bundles.scans.high') }}</dt>
            <dd class="tabular-nums text-foreground">{{ high }}</dd>
          </dl>
          <p v-if="failed > 0" class="mt-1 text-xs text-warning" role="status">{{ t('bundles.promote.scansFailed') }}</p>
          <div v-if="critical > 0" class="mt-2 rounded-lg border border-warning/40 bg-warning-soft px-3 py-2 text-sm text-foreground" role="alert">
            {{ t('bundles.promote.criticalOpen') }}
            <router-link :to="{ path: '/security', query: { type: 'vuln', severity: 'critical' } }" class="font-medium text-primary hover:underline">{{ t('bundles.promote.openSecurity') }}</router-link>
          </div>
        </template>
      </section>

      <FormField :label="t('bundles.promote.stage')" id="promote-stage" :hint="t('bundles.promote.stageHint')">
        <select id="promote-stage" v-model="toStage" required :class="INPUT_CLASS">
          <option v-for="s in stages" :key="s.name" :value="s.name">{{ s.name }}</option>
        </select>
      </FormField>
      <FormField :label="t('bundles.promote.repo')" id="promote-repo" :hint="fixedRepo ? t('bundles.promote.repoFixed') : undefined">
        <select id="promote-repo" v-model="targetRepo" required :disabled="fixedRepo !== undefined" :class="INPUT_CLASS">
          <option value="" disabled>{{ t('bundles.promote.chooseRepo') }}</option>
          <option v-for="r in repos" :key="r.id" :value="r.name">{{ r.name }} ({{ r.format }})</option>
        </select>
      </FormField>
      <FormField :label="`${t('bundles.promote.notes')} (${t('common.optional')})`" id="promote-notes" :hint="t('bundles.promote.notesHint')">
        <textarea id="promote-notes" v-model="notes" rows="3" :class="INPUT_CLASS" />
      </FormField>
      <label class="flex min-h-11 items-start gap-2 text-sm text-foreground">
        <input id="promote-force" v-model="force" type="checkbox" class="mt-0.5 h-4 w-4 rounded border-border-strong max-md:h-5 max-md:w-5" />
        <span>
          {{ t('bundles.promote.force') }}
          <span class="block text-xs text-muted">{{ t('bundles.promote.forceHint') }}</span>
        </span>
      </label>
      <p v-if="error" class="text-sm text-danger" role="alert" data-testid="promote-error">{{ error }}</p>
    </form>

    <template #footer>
      <AppButton variant="secondary" size="sm" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="bundle-promote-form" :loading="busy" :disabled="!toStage">
        {{ tp('bundles.action.promoteTo', { stage: toStage }) }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import { promoteBundle, type Bundle, type PipelineStage } from '../../api/bundles'
import type { Repository } from '../../api/repositories'
import type { BundleScanRow } from '../../composables/useBundleScans'
import { INPUT_CLASS } from '../../lib/formClasses'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n/en'

const props = defineProps<{
  open: boolean
  bundle: Bundle
  /** Stages after the bundle's current one; the first is the default. */
  stages: PipelineStage[]
  repos: Repository[]
  itemCount: number
  scans: BundleScanRow[]
}>()
const emit = defineEmits<{ close: []; promoted: [] }>()

const toasts = useToastStore()
const toStage = ref('')
const targetRepo = ref('')
const notes = ref('')
const force = ref(false)
const busy = ref(false)
const error = ref('')

const critical = computed(() => props.scans.reduce((n, s) => n + s.critical, 0))
const high = computed(() => props.scans.reduce((n, s) => n + s.high, 0))
const failed = computed(() => props.scans.filter((s) => s.failed).length)

/** Name of the repository the chosen stage insists on, when it has one. */
const fixedRepo = computed(() => {
  const id = props.stages.find((s) => s.name === toStage.value)?.repo_id
  return id ? props.repos.find((r) => r.id === id)?.name : undefined
})

watch(fixedRepo, (name) => {
  if (name) targetRepo.value = name
})

// Every opening starts at the next stage with an empty note.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    toStage.value = props.stages[0]?.name ?? ''
    targetRepo.value = fixedRepo.value ?? ''
    notes.value = ''
    force.value = false
    error.value = ''
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  if (busy.value || !toStage.value) return
  busy.value = true
  error.value = ''
  try {
    const res = await promoteBundle(
      props.bundle.name,
      props.bundle.version,
      { to_stage: toStage.value, target_repo: targetRepo.value, notes: notes.value.trim() || undefined },
      force.value,
    )
    toasts.success(
      tp('bundles.promote.done', { stage: toStage.value }),
      tp('bundles.promote.summary', { copied: String(res.copied ?? 0), existing: String(res.existing ?? 0), replaced: String(res.replaced ?? 0) }),
    )
    emit('promoted')
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('bundles.promote.failed')
  } finally {
    busy.value = false
  }
}
</script>

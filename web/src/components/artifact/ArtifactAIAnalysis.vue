<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The latest AI analysis of each file of one version, whatever its outcome: failed and pending runs show
  their status and error, completed ones their summary, tags, license verdict and notes. A file that was
  never analyzed (404) shows nothing. The caller shows this to administrators only (the endpoints need manage).
-->
<template>
  <div>
    <StateBlock v-if="error" variant="error" :title="t('security.ai.loadFailed')" :hint="error">
      <template #action><AppButton variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton></template>
    </StateBlock>
    <p v-else-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <p v-else-if="analyses.length === 0" class="max-w-2xl text-sm text-muted" data-testid="ai-none">{{ t('artifact.ai.none') }}</p>
    <div v-else class="space-y-6">
      <article v-for="a in analyses" :key="a.id" class="max-w-3xl space-y-3 rounded-lg border border-border bg-surface p-4" data-testid="ai-analysis">
        <div class="flex flex-wrap items-center gap-2">
          <StatusBadge :label="statusLabel(a)" :variant="statusVariant(a)" data-testid="ai-status" />
          <StatusBadge
            v-if="a.status === 'completed'"
            :label="a.quarantine_recommended ? t('security.ai.quarantineRecommended') : t('security.ai.noQuarantine')"
            :variant="a.quarantine_recommended ? 'danger' : 'success'"
          />
          <span class="text-xs text-muted">
            {{ a.model_used || '—' }} ·
            <time :datetime="a.updated_at" :title="formatDateTime(a.updated_at)">{{ formatDateTime(a.updated_at) }}</time>
          </span>
        </div>
        <p v-if="a.status === 'failed' || a.error_message" class="rounded-lg border border-danger/20 bg-danger-soft px-4 py-3 font-mono text-xs text-danger" role="alert" data-testid="ai-error">
          {{ a.error_message || t('artifact.ai.failedNoReason') }}
        </p>
        <p v-if="a.summary" class="text-sm leading-relaxed text-foreground">{{ a.summary }}</p>
        <div v-if="a.tags?.length" class="flex flex-wrap gap-1.5" data-testid="ai-tags">
          <span v-for="tag in a.tags" :key="tag" class="rounded-md border border-border px-2 py-0.5 text-caption">{{ tag }}</span>
        </div>
        <div v-if="a.status === 'completed'" class="flex flex-wrap items-center gap-2">
          <span class="text-xs text-muted">{{ t('security.ai.col.license') }}</span>
          <StatusBadge :label="licenseLabel(a)" :variant="licenseVariant(a)" />
          <span v-if="a.license_name" class="font-mono text-sm">{{ a.license_name }}</span>
        </div>
        <div v-if="a.security_notes">
          <h3 class="mb-1 text-xs font-semibold text-muted">{{ t('security.ai.securityNotes') }}</h3>
          <p class="whitespace-pre-wrap text-sm leading-relaxed text-foreground">{{ a.security_notes }}</p>
        </div>
        <AppButton variant="secondary" size="sm" :loading="retriggering === a.artifact_id" :disabled="retriggering !== ''" data-testid="ai-retrigger" @click="retrigger(a)">
          <SparklesIcon class="h-4 w-4" aria-hidden="true" />
          {{ retriggering === a.artifact_id ? t('security.ai.triggering') : t('security.ai.retriggerLong') }}
        </AppButton>
      </article>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { SparklesIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import StatusBadge from '../StatusBadge.vue'
import StateBlock from '../ui/StateBlock.vue'
import { getAIAnalysis, triggerAIAnalysis, type AIAnalysis } from '../../api/ai'
import { ApiError, isFeatureUnavailable } from '../../api/client'
import { formatDateTime } from '../../lib/datetime'
import { aiLicenseLabel, aiLicenseVariant, aiStatusLabel, aiStatusVariant } from '../../lib/aiAnalysis'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n'

const props = defineProps<{ repo: string; artifactIds: string[] }>()

const toasts = useToastStore()
const analyses = ref<AIAnalysis[]>([])
const loading = ref(false)
const error = ref('')
const retriggering = ref('')

const statusLabel = (a: AIAnalysis) => aiStatusLabel(a.status)
const statusVariant = (a: AIAnalysis) => aiStatusVariant(a.status)
const licenseLabel = (a: AIAnalysis) => aiLicenseLabel(a.license_status)
const licenseVariant = (a: AIAnalysis) => aiLicenseVariant(a.license_status)

let generation = 0
async function load(): Promise<void> {
  const mine = ++generation
  error.value = ''
  loading.value = true
  const results = await Promise.allSettled(props.artifactIds.map((id) => getAIAnalysis(props.repo, id)))
  if (mine !== generation) return
  const found: AIAnalysis[] = []
  for (const r of results) {
    if (r.status === 'fulfilled') found.push(r.value)
    else if (!(r.reason instanceof ApiError && r.reason.status === 404) && !error.value) {
      error.value = isFeatureUnavailable(r.reason) ? t('security.ai.unavailable') : r.reason instanceof Error ? r.reason.message : t('security.ai.loadFailed')
    }
  }
  analyses.value = found
  loading.value = false
}

async function retrigger(a: AIAnalysis): Promise<void> {
  if (retriggering.value) return
  retriggering.value = a.artifact_id
  try {
    await triggerAIAnalysis(props.repo, a.artifact_id, {
      artifact_format: a.artifact_format || undefined,
      artifact_name: a.artifact_name || undefined,
      artifact_version: a.artifact_version || undefined,
    })
    toasts.success(t('security.ai.retriggered'))
    await load()
  } catch (err) {
    toasts.error(isFeatureUnavailable(err) ? t('security.ai.unavailable') : err instanceof Error ? err.message : t('security.ai.retriggerFailed'))
  } finally {
    retriggering.value = ''
  }
}

watch(() => [props.repo, props.artifactIds.join(',')], () => void load(), { immediate: true })
</script>

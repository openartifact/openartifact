<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <div v-if="loading && !analysis" class="flex justify-center py-8 text-muted" role="status">
      <ArrowPathIcon class="h-5 w-5 animate-spin" aria-hidden="true" />
      <span class="sr-only">{{ t('security.detail.loading') }}</span>
    </div>
    <p v-else-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <div v-else-if="analysis" class="space-y-5" data-testid="ai-detail">
      <div class="flex flex-wrap items-center gap-2">
        <StatusBadge :label="statusLabel" :variant="statusVariant" />
        <ArrowPathIcon v-if="active" class="h-3.5 w-3.5 animate-spin text-muted" aria-hidden="true" />
        <StatusBadge
          :label="analysis.quarantine_recommended ? t('security.ai.quarantineRecommended') : t('security.ai.noQuarantine')"
          :variant="analysis.quarantine_recommended ? 'danger' : 'success'"
        />
      </div>

      <p v-if="analysis.error_message" class="rounded-lg border border-danger/20 bg-danger-soft px-4 py-3 font-mono text-xs text-danger" role="alert">
        {{ analysis.error_message }}
      </p>

      <section v-if="analysis.summary">
        <h4 class="mb-2 text-xs font-semibold text-muted">{{ t('security.ai.col.summary') }}</h4>
        <p class="text-sm leading-relaxed text-foreground">{{ analysis.summary }}</p>
      </section>

      <section v-if="analysis.tags?.length">
        <h4 class="mb-2 text-xs font-semibold text-muted">{{ t('security.ai.tags') }}</h4>
        <div class="flex flex-wrap gap-1.5">
          <span v-for="tag in analysis.tags" :key="tag" class="rounded-md border border-border px-2 py-0.5 text-caption">{{ tag }}</span>
        </div>
      </section>

      <section>
        <h4 class="mb-2 text-xs font-semibold text-muted">{{ t('security.ai.col.license') }}</h4>
        <div class="flex flex-wrap items-center gap-2">
          <StatusBadge :label="licenseLabel" :variant="licenseVariant" />
          <span v-if="analysis.license_name" class="font-mono text-sm">{{ analysis.license_name }}</span>
          <span v-else class="text-sm text-muted">{{ t('security.ai.unknownLicense') }}</span>
        </div>
      </section>

      <section v-if="analysis.security_notes">
        <h4 class="mb-2 text-xs font-semibold text-muted">{{ t('security.ai.securityNotes') }}</h4>
        <p class="whitespace-pre-wrap text-sm leading-relaxed text-foreground">{{ analysis.security_notes }}</p>
      </section>

      <dl class="grid grid-cols-2 gap-2 rounded-lg border border-border bg-surface-hover px-4 py-3 text-xs">
        <div>
          <dt class="text-muted">{{ t('security.ai.model') }}</dt>
          <dd class="mt-0.5 font-mono">{{ analysis.model_used || '—' }}</dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('security.ai.updated') }}</dt>
          <dd class="mt-0.5"><time :datetime="analysis.updated_at" :title="formatDateTime(analysis.updated_at)">{{ timeAgo(analysis.updated_at) }}</time></dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('security.ai.promptTokens') }}</dt>
          <dd class="mt-0.5 font-mono tabular-nums">{{ analysis.prompt_tokens ?? '—' }}</dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('security.ai.completionTokens') }}</dt>
          <dd class="mt-0.5 font-mono tabular-nums">{{ analysis.completion_tokens ?? '—' }}</dd>
        </div>
      </dl>

      <AppButton v-if="canAct" variant="secondary" :loading="retriggering" :disabled="retriggering" data-testid="ai-retrigger" @click="retrigger">
        <SparklesIcon class="h-4 w-4" aria-hidden="true" />
        {{ retriggering ? t('security.ai.triggering') : t('security.ai.retriggerLong') }}
      </AppButton>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowPathIcon, SparklesIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import StatusBadge from '../StatusBadge.vue'
import { getAIAnalysis, triggerAIAnalysis, type AIAnalysis } from '../../api/ai'
import type { SecurityFinding } from '../../api/security'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import { useToastStore } from '../../stores/toast'
import { t, tDynamic } from '../../i18n/en'

const POLL_MS = 4000

const props = defineProps<{
  finding: SecurityFinding
  canAct: boolean
}>()

const emit = defineEmits<{ changed: [] }>()

const toasts = useToastStore()
const analysis = ref<AIAnalysis | null>(null)
const loading = ref(false)
const error = ref('')
const retriggering = ref(false)
let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined

const active = computed(() => analysis.value?.status === 'pending' || analysis.value?.status === 'processing')

const KNOWN_STATUS = ['pending', 'processing', 'completed', 'failed']
const statusLabel = computed(() => {
  const s = analysis.value?.status ?? ''
  return KNOWN_STATUS.includes(s) ? tDynamic(`security.ai.status.${s}`) : s
})
const statusVariant = computed(() => {
  const s = analysis.value?.status
  return s === 'completed' ? 'success' : s === 'failed' ? 'danger' : 'warning'
})
const licenseLabel = computed(() => {
  const s = analysis.value?.license_status ?? ''
  return ['SAFE', 'REVIEW_REQUIRED', 'UNKNOWN'].includes(s) ? tDynamic(`security.ai.license.${s}`) : s
})
const licenseVariant = computed(() => {
  const s = analysis.value?.license_status
  return s === 'SAFE' ? 'success' : s === 'REVIEW_REQUIRED' ? 'warning' : 'neutral'
})

async function load(silent = false): Promise<void> {
  const mine = ++seq
  clearTimeout(timer)
  if (!silent) {
    analysis.value = null
    loading.value = true
  }
  error.value = ''
  try {
    const result = await getAIAnalysis(props.finding.repository, props.finding.artifact_id)
    if (mine !== seq) return
    analysis.value = result
  } catch (err) {
    if (mine === seq && !silent) error.value = err instanceof Error ? err.message : t('security.ai.loadFailed')
  } finally {
    if (mine === seq) {
      loading.value = false
      if (active.value) timer = setTimeout(() => void load(true), POLL_MS)
    }
  }
}

async function retrigger(): Promise<void> {
  const a = analysis.value
  if (!a || retriggering.value) return
  retriggering.value = true
  try {
    await triggerAIAnalysis(props.finding.repository, a.artifact_id, {
      artifact_format: a.artifact_format || undefined,
      artifact_name: a.artifact_name || undefined,
      artifact_version: a.artifact_version || undefined,
    })
    toasts.success(t('security.ai.retriggered'))
    await load(true)
    emit('changed')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('security.ai.retriggerFailed'))
  } finally {
    retriggering.value = false
  }
}

watch(() => props.finding.id, () => void load(), { immediate: true })
onBeforeUnmount(() => {
  clearTimeout(timer)
  seq++
})
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('tiering.mover.title')">
    <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
      <div class="min-w-0 mr-auto">
        <h2 class="text-sm font-semibold text-foreground">{{ t('tiering.mover.title') }}</h2>
        <p class="text-caption text-muted">{{ t('tiering.mover.description') }}</p>
      </div>
      <AppButton
        size="sm"
        :loading="starting || polling"
        :disabled="!status?.enabled || status.running"
        :title="status && !status.enabled ? t('tiering.mover.disabledHint') : undefined"
        data-testid="mover-run"
        @click="runNow"
      >
        <PlayIcon v-if="!starting && !polling" class="w-4 h-4" aria-hidden="true" />
        {{ t('tiering.mover.runNow') }}
      </AppButton>
    </div>

    <div v-if="loading" class="px-5 py-6"><div class="h-4 w-48 rounded skel" /></div>

    <div v-else-if="loadError && !status" class="px-5 py-6 text-sm text-danger flex items-center gap-3" role="alert">
      <span>{{ loadError }}</span>
      <AppButton variant="secondary" size="xs" @click="load">{{ t('common.refresh') }}</AppButton>
    </div>

    <div v-else-if="status" class="px-5 py-4 space-y-4">
      <div v-if="loadError" class="text-sm text-danger flex items-center gap-3" role="alert" data-testid="mover-poll-error">
        <span>{{ loadError }}</span>
        <AppButton variant="secondary" size="xs" @click="refresh">{{ t('common.refresh') }}</AppButton>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <StatusBadge
          :label="status.enabled ? t('tiering.mover.enabled') : t('tiering.mover.disabled')"
          :variant="status.enabled ? 'success' : 'neutral'"
        />
        <StatusBadge v-if="status.running" :label="t('tiering.mover.running')" variant="warning" />
        <span v-if="status.enabled && status.interval_seconds" class="text-caption text-muted">
          {{ tp('tiering.mover.interval', { d: formatExactDuration(status.interval_seconds) }) }}
        </span>
      </div>
      <p v-if="!status.enabled" class="text-xs text-muted">{{ t('tiering.mover.disabledHint') }}</p>

      <div v-if="pass" data-testid="mover-last-pass">
        <h3 class="text-xs font-semibold text-foreground mb-1.5">
          {{ t('tiering.mover.lastPass') }}
          <span class="font-normal text-muted ml-1">{{ formatDateTime(pass.finished_at ?? pass.started_at) }}</span>
          <StatusBadge v-if="pass.aborted" class="ml-2" :label="t('tiering.mover.aborted')" variant="warning" />
        </h3>
        <dl class="grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-8 gap-3">
          <div v-for="c in counters" :key="c.label">
            <dt class="text-caption text-muted">{{ c.label }}</dt>
            <dd class="text-sm font-semibold tabular-nums" :class="c.danger ? 'text-danger' : 'text-foreground'">{{ c.value }}</dd>
          </div>
        </dl>
      </div>
      <p v-else class="text-xs text-muted">{{ t('tiering.mover.noPass') }}</p>

      <div>
        <h3 class="text-xs font-semibold text-foreground mb-1.5">
          {{ t('tiering.mover.pending') }}
          <span class="font-normal text-muted ml-1">{{ pendingCaption }}</span>
        </h3>
        <p v-if="pending.length === 0" class="text-xs text-muted">{{ t('tiering.mover.noPending') }}</p>
        <div v-else class="overflow-x-auto">
          <table class="w-full text-sm" data-testid="mover-pending">
            <thead>
              <tr class="text-left text-caption font-semibold text-muted">
                <th scope="col" class="py-1.5 pr-3">{{ t('tiering.dryRun.sha') }}</th>
                <th scope="col" class="py-1.5 px-3 text-right">{{ t('tiering.dryRun.size') }}</th>
                <th scope="col" class="py-1.5 px-3">{{ t('tiering.dryRun.route') }}</th>
                <th scope="col" class="py-1.5 px-3">{{ t('tiering.mover.state') }}</th>
                <th scope="col" class="py-1.5 px-3 text-right">{{ t('tiering.mover.attempts') }}</th>
                <th scope="col" class="py-1.5 px-3">{{ t('tiering.mover.reason') }}</th>
                <th scope="col" class="py-1.5 pl-3">{{ t('tiering.mover.updated') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              <tr v-for="m in pending" :key="m.sha256">
                <td class="py-1.5 pr-3"><ShortSha :sha="m.sha256 ?? ''" /></td>
                <td class="py-1.5 px-3 text-right tabular-nums">{{ formatBytes(m.size_bytes ?? 0) }}</td>
                <td class="py-1.5 px-3 font-mono text-xs whitespace-nowrap">{{ m.from }} → {{ m.to }}</td>
                <td class="py-1.5 px-3">
                  <StatusBadge
                    :label="m.state === 'flipped' ? t('tiering.mover.state.flipped') : t('tiering.mover.state.copying')"
                    variant="neutral"
                  />
                </td>
                <td class="py-1.5 px-3 text-right tabular-nums">{{ m.attempts }}</td>
                <td class="py-1.5 px-3 text-xs text-muted break-all">{{ m.reason || '—' }}</td>
                <td class="py-1.5 pl-3 text-muted whitespace-nowrap">{{ formatDateTime(m.updated_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { PlayIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import ShortSha from './ShortSha.vue'
import StatusBadge from './StatusBadge.vue'
import { getMoverStatus, runMover, type TierMoverStatus } from '../api/tiering'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n/en'
import { formatDateTime } from '../lib/datetime'
import { formatBytes } from '../lib/quota'
import { formatExactDuration } from '../lib/duration'
import { formatCount } from '../lib/tiering'

const props = withDefaults(defineProps<{ pollIntervalMs?: number; maxPolls?: number; maxFailures?: number }>(), {
  pollIntervalMs: 3000,
  maxPolls: 20,
  maxFailures: 3,
})
const emit = defineEmits<{ (e: 'pass-finished'): void }>()

const toasts = useToastStore()

const status = ref<TierMoverStatus | null>(null)
const loading = ref(true)
const loadError = ref('')
const starting = ref(false)
const polling = ref(false)

let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
let epoch = 0

const pass = computed(() => status.value?.last_pass ?? null)
const pending = computed(() => status.value?.pending ?? [])
const pendingCaption = computed(() => {
  const total = status.value?.pending_total ?? 0
  if (total <= pending.value.length) return String(total)
  return tp('tiering.mover.pendingOf', { n: String(pending.value.length), total: String(total) })
})

const counters = computed(() => {
  const p = pass.value
  if (!p) return []
  return [
    { label: t('tiering.mover.scanned'), value: formatCount(p.scanned) },
    { label: t('tiering.mover.moved'), value: formatCount(p.moved) },
    { label: t('tiering.mover.promoted'), value: formatCount(p.promoted) },
    { label: t('tiering.mover.recovered'), value: formatCount(p.recovered) },
    { label: t('tiering.mover.skipped'), value: formatCount(p.skipped) },
    { label: t('tiering.mover.failed'), value: formatCount(p.failed), danger: (p.failed ?? 0) > 0 },
    { label: t('tiering.mover.bytes'), value: formatBytes(p.bytes ?? 0) },
  ]
})

/** Reports failures only when asked; a failed poll keeps the last known status on screen. */
async function fetchStatus(): Promise<TierMoverStatus | null> {
  try {
    const res = await getMoverStatus()
    if (!disposed) status.value = res
    return res
  } catch (err) {
    lastError = err instanceof Error ? err.message : t('tiering.mover.loadFailed')
    return null
  }
}

let lastError = ''

async function load(): Promise<void> {
  stopPolling()
  loading.value = true
  loadError.value = ''
  const res = await fetchStatus()
  if (disposed) return
  if (!res) loadError.value = lastError
  loading.value = false
  if (res?.running) poll(res.last_pass?.finished_at)
}

/** Re-reads the status now (page-level Refresh); the initial skeleton is not shown again. */
async function refresh(): Promise<void> {
  stopPolling()
  const res = await fetchStatus()
  if (disposed) return
  loadError.value = res ? '' : lastError
  if (res?.running) poll(res.last_pass?.finished_at)
}

function stopPolling(): void {
  clearTimeout(timer)
  timer = undefined
  polling.value = false
  epoch++
}

/**
 * Re-reads the status until the pass that was running (or just requested)
 * has a newer `finished_at` than `baseline`. While the mover reports
 * `running` it keeps polling with a growing delay; it gives up after
 * `maxFailures` consecutive failed reads (showing an error with Refresh) or
 * `maxPolls` consecutive idle reads where no pass ever started.
 */
function poll(baseline: string | undefined): void {
  if (disposed) return
  stopPolling()
  polling.value = true
  loadError.value = ''
  const mine = epoch
  let idle = 0
  let failures = 0
  let delay = props.pollIntervalMs

  const tick = async (): Promise<void> => {
    const res = await fetchStatus()
    if (disposed || mine !== epoch) return
    if (!res) {
      if (++failures >= props.maxFailures) {
        stopPolling()
        loadError.value = lastError
        return
      }
    } else {
      failures = 0
      if (!res.running && res.last_pass?.finished_at !== baseline) {
        stopPolling()
        emit('pass-finished')
        return
      }
      idle = res.running ? 0 : idle + 1
      if (idle >= props.maxPolls) {
        stopPolling()
        return
      }
    }
    delay = Math.min(delay * 2, props.pollIntervalMs * 8)
    timer = setTimeout(tick, delay)
  }
  timer = setTimeout(tick, delay)
}

async function runNow(): Promise<void> {
  starting.value = true
  const baseline = status.value?.last_pass?.finished_at
  try {
    const res = await runMover()
    if (disposed) return
    toasts.info(res.started ? t('tiering.mover.started') : t('tiering.mover.alreadyRunning'))
    poll(baseline)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('tiering.mover.runFailed'))
  } finally {
    starting.value = false
  }
}

defineExpose({ refresh })

onMounted(load)
onUnmounted(() => {
  disposed = true
  stopPolling()
})
</script>

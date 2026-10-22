<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Project quota ceiling. Global administrators edit it (the server answers 403
  to everyone else); other members see the current limits read-only. 0 means
  unlimited.
-->
<template>
  <section class="rounded-xl border border-border bg-surface p-4 sm:p-5" :aria-label="t('projects.quota.title')" data-testid="project-quota">
    <h2 class="text-sm font-semibold text-foreground">{{ t('projects.quota.title') }}</h2>
    <p class="text-xs text-muted mt-0.5 mb-4">{{ t('projects.quota.hint') }}</p>

    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <p v-else-if="failed" class="text-sm text-danger" role="alert">{{ t('projects.quota.loadFailed') }}</p>

    <form v-else-if="editable" class="space-y-4 max-w-xl" data-testid="quota-form" @submit.prevent="save">
      <div>
        <label for="pq-size" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('projects.quota.maxSize') }}</label>
        <div class="flex flex-wrap items-center gap-2">
          <input
            id="pq-size"
            v-model.number="sizeValue"
            type="number"
            min="0"
            step="any"
            placeholder="0"
            class="flex-1 min-w-32 h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary"
          />
          <div class="inline-flex p-0.5 rounded-lg bg-surface-hover border border-border shrink-0">
            <button
              v-for="unit in SIZE_UNITS"
              :key="unit.label"
              type="button"
              :aria-pressed="sizeUnit === unit.label"
              class="px-2.5 py-1 rounded-md text-xs font-medium transition-colors"
              :class="sizeUnit === unit.label ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
              @click="sizeUnit = unit.label"
            >{{ unit.label }}</button>
          </div>
        </div>
      </div>
      <div>
        <label for="pq-count" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('projects.quota.maxCount') }}</label>
        <input
          id="pq-count"
          v-model.number="count"
          type="number"
          min="0"
          step="1"
          placeholder="0"
          class="w-40 h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary"
        />
      </div>
      <div>
        <label for="pq-warn" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('projects.quota.warn') }}</label>
        <input
          id="pq-warn"
          v-model.number="warn"
          type="number"
          min="0"
          max="100"
          step="1"
          class="w-28 h-9 px-3 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary"
        />
        <p class="mt-1 text-caption text-muted">{{ t('projects.quota.warnHint') }}</p>
      </div>
      <AppButton variant="primary" size="sm" type="submit" :loading="saving" :disabled="!dirty || !valid">
        {{ t('projects.quota.save') }}
      </AppButton>
    </form>

    <template v-else-if="quota">
      <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm" data-testid="quota-readonly">
        <dt class="text-muted">{{ t('projects.quota.maxSize') }}</dt>
        <dd class="text-foreground">{{ (quota.max_size_bytes ?? 0) > 0 ? formatBytes(quota.max_size_bytes ?? 0) : t('projects.quota.unlimited') }}</dd>
        <dt class="text-muted">{{ t('projects.quota.maxCount') }}</dt>
        <dd class="text-foreground">{{ (quota.max_artifact_count ?? 0) > 0 ? quota.max_artifact_count : t('projects.quota.unlimited') }}</dd>
        <dt class="text-muted">{{ t('projects.quota.warn') }}</dt>
        <dd class="text-foreground">{{ quota.warn_threshold_percent ?? 0 }}</dd>
      </dl>
      <p class="mt-3 text-xs text-muted">{{ t('projects.quota.adminOnly') }}</p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import { getProjectQuota, setProjectQuota, type ProjectQuota } from '../api/projects'
import { formatBytes, SIZE_UNITS, splitBytes, toBytes, type SizeUnit } from '../lib/quota'
import { errorStatus } from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ project: string; editable: boolean }>()

const toasts = useToastStore()
const quota = ref<ProjectQuota | null>(null)
const loading = ref(true)
const failed = ref(false)
const saving = ref(false)

const sizeValue = ref<number | ''>('')
const sizeUnit = ref<SizeUnit>('GB')
const count = ref<number | ''>('')
const warn = ref<number | ''>('')

function fill(q: ProjectQuota): void {
  const s = splitBytes(q.max_size_bytes ?? 0)
  sizeValue.value = s.value > 0 ? s.value : 0
  sizeUnit.value = s.unit
  count.value = q.max_artifact_count ?? 0
  warn.value = q.warn_threshold_percent ?? 0
}

const num = (v: number | ''): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0)

const body = computed(() => ({
  max_size_bytes: toBytes(num(sizeValue.value), sizeUnit.value),
  max_artifact_count: Math.trunc(num(count.value)),
  warn_threshold_percent: Math.trunc(num(warn.value)),
}))

const valid = computed(
  () =>
    num(sizeValue.value) >= 0 &&
    num(count.value) >= 0 &&
    num(warn.value) >= 0 &&
    num(warn.value) <= 100,
)

const dirty = computed(() => {
  const q = quota.value
  if (!q) return false
  const b = body.value
  return (
    b.max_size_bytes !== (q.max_size_bytes ?? 0) ||
    b.max_artifact_count !== (q.max_artifact_count ?? 0) ||
    b.warn_threshold_percent !== (q.warn_threshold_percent ?? 0)
  )
})

let generation = 0

async function load(): Promise<void> {
  const mine = ++generation
  loading.value = true
  failed.value = false
  try {
    const q = await getProjectQuota(props.project)
    if (mine !== generation) return
    quota.value = q
    fill(q)
  } catch {
    if (mine === generation) failed.value = true
  } finally {
    if (mine === generation) loading.value = false
  }
}

async function save(): Promise<void> {
  if (saving.value || !dirty.value || !valid.value) return
  saving.value = true
  try {
    const q = await setProjectQuota(props.project, body.value)
    quota.value = q
    fill(q)
    toasts.success(t('projects.quota.saved'))
  } catch (err) {
    const status = errorStatus(err)
    toasts.error(
      status === 403
        ? t('projects.quota.forbidden')
        : status === 400
          ? t('projects.quota.invalid')
          : t('projects.quota.saveFailed'),
    )
  } finally {
    saving.value = false
  }
}

watch(() => props.project, load, { immediate: true })
</script>

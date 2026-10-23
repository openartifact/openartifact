<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="bg-surface rounded-xl border border-border p-5 space-y-4" :aria-label="t('adm.dt.repos')">
    <div>
      <h3 class="text-sm font-semibold text-foreground">{{ t('adm.dt.repos') }}</h3>
      <p class="text-caption text-muted mt-0.5">{{ t('adm.dt.reposHint') }}</p>
    </div>

    <div class="max-w-sm">
      <label for="dt-repo" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.repository') }}</label>
      <select id="dt-repo" v-model="repo" :class="inputClass" @change="onRepoChange">
        <option value="">{{ t('adm.dt.selectRepo') }}</option>
        <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }}</option>
      </select>
    </div>

    <div v-if="repoError" class="text-danger text-sm" role="alert">{{ repoError }}</div>
    <div v-else-if="repoLoading" class="text-muted text-sm" role="status">{{ t('common.loading') }}</div>

    <template v-else-if="repo && effective">
      <!-- Effective configuration -->
      <div class="rounded-lg border border-border p-4 space-y-1.5" data-testid="dt-effective">
        <div class="flex items-center gap-2">
          <h4 class="text-xs font-semibold text-foreground">{{ t('adm.dt.effective') }}</h4>
          <StatusBadge :label="t(`adm.dt.source.${effective.source ?? 'none'}`)" variant="neutral" />
          <StatusBadge :label="effective.ready ? t('adm.dt.ready') : t('adm.dt.notReady')" :variant="effective.ready ? 'success' : 'warning'" />
        </div>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
          <dt class="text-muted">{{ t('adm.dt.baseUrl') }}</dt>
          <dd class="font-mono text-foreground break-all">{{ effective.base_url || '—' }}</dd>
          <dt class="text-muted">{{ t('adm.dt.apiKey') }}</dt>
          <dd class="text-foreground">{{ effective.api_key_set ? t('adm.dt.apiKeyStored') : t('adm.dt.apiKeyNone') }}</dd>
          <dt class="text-muted">{{ t('adm.dt.projectName') }}</dt>
          <dd class="font-mono text-foreground">{{ effective.project_name || '—' }}</dd>
          <dt class="text-muted">{{ t('adm.dt.projectVersion') }}</dt>
          <dd class="font-mono text-foreground">{{ effective.project_version || '—' }}</dd>
          <dt class="text-muted">{{ t('adm.dt.autoPush') }}</dt>
          <dd class="text-foreground">{{ effective.auto_push ? t('adm.dt.on') : t('adm.dt.off') }}</dd>
          <dt class="text-muted">{{ t('adm.dt.pushVex') }}</dt>
          <dd class="text-foreground">{{ effective.push_vex ? t('adm.dt.on') : t('adm.dt.off') }}</dd>
        </dl>
      </div>

      <!-- Override form -->
      <form class="rounded-lg border border-border p-4 space-y-4" @submit.prevent="saveOverride()">
        <div>
          <h4 class="text-xs font-semibold text-foreground">{{ t('adm.dt.override') }}</h4>
          <p class="text-caption text-muted mt-0.5">{{ t('adm.dt.overrideHint') }}</p>
        </div>

        <div class="flex items-center gap-3">
          <AppToggle v-model="form.enabled" labelledby="dt-ovr-enable" />
          <span id="dt-ovr-enable" class="text-sm text-foreground">{{ t('adm.dt.overrideEnabled') }}</span>
        </div>

        <div class="grid grid-cols-2 gap-x-4 gap-y-4">
          <div class="col-span-2 sm:col-span-1">
            <label for="dt-ovr-url" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.baseUrl') }}</label>
            <input id="dt-ovr-url" v-model="form.base_url" type="text" :class="inputClass" />
          </div>
          <div class="col-span-2 sm:col-span-1">
            <label for="dt-ovr-key" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.apiKey') }}</label>
            <input id="dt-ovr-key" v-model="apiKey" type="password" autocomplete="new-password" :placeholder="t('adm.dt.apiKeyPlaceholder')" :class="inputClass" />
            <div class="flex items-center gap-3 mt-1">
              <span class="text-caption" :class="apiKeySet ? 'text-success' : 'text-muted'">
                {{ apiKeySet ? t('adm.dt.apiKeyStored') : t('adm.dt.apiKeyNone') }}
              </span>
              <button
                v-if="apiKeySet"
                type="button"
                class="text-caption font-medium text-muted hover:text-danger transition-colors"
                :disabled="saving"
                @click="saveOverride('')"
              >
                {{ t('adm.dt.removeKey') }}
              </button>
            </div>
          </div>
          <div class="col-span-2 sm:col-span-1">
            <label for="dt-ovr-name" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.projectName') }}</label>
            <input id="dt-ovr-name" v-model="form.project_name" type="text" :placeholder="t('adm.dt.projectNamePlaceholder')" :class="inputClass" />
          </div>
          <div class="col-span-2 sm:col-span-1">
            <label for="dt-ovr-version" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.projectVersion') }}</label>
            <input id="dt-ovr-version" v-model="form.project_version" type="text" :placeholder="t('adm.dt.projectVersionPlaceholder')" :class="inputClass" />
          </div>
          <p class="col-span-2 text-caption text-muted -mt-2">{{ t('adm.dt.templateHint') }}</p>
          <div class="col-span-2 sm:col-span-1">
            <label for="dt-ovr-auto" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.autoPush') }}</label>
            <select id="dt-ovr-auto" v-model="form.auto_push" :class="inputClass">
              <option v-for="o in triOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
          </div>
          <div class="col-span-2 sm:col-span-1">
            <label for="dt-ovr-vex" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.pushVex') }}</label>
            <select id="dt-ovr-vex" v-model="form.push_vex" :class="inputClass">
              <option v-for="o in triOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
          </div>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <AppButton type="submit" size="sm" :loading="saving">{{ t('adm.dt.saveOverride') }}</AppButton>
          <AppButton v-if="hasOverride" variant="danger-outline" size="sm" :disabled="saving" @click="removeOverride">
            {{ t('adm.dt.removeOverride') }}
          </AppButton>
          <AppButton variant="secondary" size="sm" :loading="testing" @click="runTest">{{ t('adm.dt.test') }}</AppButton>
        </div>
      </form>

      <DependencyTrackTestResult v-if="testResult" :result="testResult" />
      <div v-if="testError" class="rounded-xl border border-danger/30 bg-danger-soft p-4 text-sm text-danger" role="alert">
        <p class="font-semibold">{{ t('adm.dt.testFailed') }}</p>
        <p class="text-xs mt-0.5 font-mono break-all">{{ testError }}</p>
      </div>

      <!-- Manual push -->
      <form class="rounded-lg border border-border p-4 space-y-3" @submit.prevent="push">
        <div>
          <h4 class="text-xs font-semibold text-foreground">{{ t('adm.dt.push') }}</h4>
          <p class="text-caption text-muted mt-0.5">{{ t('adm.dt.pushHint') }}</p>
        </div>
        <div class="flex items-end gap-2">
          <div class="flex-1 max-w-md">
            <label for="dt-artifact-id" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.dt.artifactId') }}</label>
            <input id="dt-artifact-id" v-model="artifactId" type="text" :placeholder="t('adm.dt.artifactIdPlaceholder')" :class="inputClass" />
          </div>
          <AppButton type="submit" size="sm" :loading="pushing" :disabled="artifactId.trim() === ''">{{ t('adm.dt.pushButton') }}</AppButton>
        </div>
      </form>

      <!-- Pushes -->
      <div class="space-y-2">
        <h4 class="text-xs font-semibold text-foreground">{{ t('adm.dt.pushes') }}</h4>
        <p v-if="pushes.length === 0" class="text-sm text-muted">{{ t('adm.dt.noPushes') }}</p>
        <div v-else class="overflow-x-auto rounded-lg border border-border">
          <table class="w-full text-xs" data-testid="dt-pushes">
            <thead class="bg-surface-hover/50 text-muted text-left">
              <tr>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.artifact') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.state') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.detail') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.project') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.components') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.vex') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.bomPushed') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.vexPushed') }}</th>
                <th class="px-3 py-2 font-medium">{{ t('adm.dt.col.updated') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              <tr v-for="p in pushes" :key="p.artifact_id">
                <td class="px-3 py-2 font-mono text-foreground">{{ p.artifact_id }}</td>
                <td class="px-3 py-2"><StatusBadge :label="stateLabel(p.state)" :variant="stateVariant(p.state)" /></td>
                <td class="px-3 py-2 text-foreground break-words max-w-xs">{{ p.detail || '—' }}</td>
                <td class="px-3 py-2 font-mono text-foreground">
                  {{ p.project_name ? `${p.project_name} ${p.project_version ?? ''}`.trim() : '—' }}
                </td>
                <td class="px-3 py-2 text-foreground">{{ p.components ?? 0 }}</td>
                <td class="px-3 py-2 text-foreground">{{ p.vex_applied ?? 0 }} / {{ p.vex_statements ?? 0 }}</td>
                <td class="px-3 py-2 text-muted whitespace-nowrap">{{ formatDateTime(p.bom_pushed_at) }}</td>
                <td class="px-3 py-2 text-muted whitespace-nowrap">{{ formatDateTime(p.vex_pushed_at) }}</td>
                <td class="px-3 py-2 text-muted whitespace-nowrap">{{ formatDateTime(p.updated_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import AppButton from '../../components/AppButton.vue'
import AppToggle from '../../components/AppToggle.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import DependencyTrackTestResult from './DependencyTrackTestResult.vue'
import { useToastStore } from '../../stores/toast'
import { listAllRepositories } from '../../api/repositories'
import type { Repository } from '../../api/repositories'
import {
  getRepoDependencyTrack,
  saveRepoDependencyTrack,
  deleteRepoDependencyTrack,
  testRepoDependencyTrack,
  pushRepoDependencyTrack,
  listRepoDependencyTrackPushes,
} from '../../api/dependencyTrack'
import type {
  DependencyTrackRepository,
  DependencyTrackPush,
  DependencyTrackTest,
} from '../../api/dependencyTrack'
import { formatDateTime } from '../../lib/datetime'
import { t } from '../../i18n/en'

const inputClass =
  'w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors'

// Pushes are polled while one is queued/running, at most MAX_POLLS times.
const POLL_MS = 3000
const MAX_POLLS = 100

type Tri = 'inherit' | 'on' | 'off'

const toasts = useToastStore()

const repos = ref<Repository[]>([])
const repo = ref('')
const repoLoading = ref(false)
const repoError = ref('')
const effective = ref<DependencyTrackRepository['effective'] | null>(null)
const hasOverride = ref(false)
const saving = ref(false)
const testing = ref(false)
const pushing = ref(false)
const testResult = ref<DependencyTrackTest | null>(null)
const testError = ref('')
const artifactId = ref('')
const pushes = ref<DependencyTrackPush[]>([])

const apiKey = ref('')
const apiKeySet = ref(false)
const form = reactive({
  enabled: false,
  base_url: '',
  project_name: '',
  project_version: '',
  auto_push: 'inherit' as Tri,
  push_vex: 'inherit' as Tri,
})

const triOptions = computed<Array<{ value: Tri; label: string }>>(() => [
  { value: 'inherit', label: t('adm.dt.inherit') },
  { value: 'on', label: t('adm.dt.on') },
  { value: 'off', label: t('adm.dt.off') },
])

function toTri(v: boolean | null | undefined): Tri {
  return v == null ? 'inherit' : v ? 'on' : 'off'
}
function fromTri(v: Tri): boolean | null {
  return v === 'inherit' ? null : v === 'on'
}

function stateLabel(state: DependencyTrackPush['state']): string {
  return t(`adm.dt.state.${state ?? 'queued'}`)
}
function stateVariant(state: DependencyTrackPush['state']): 'success' | 'warning' | 'neutral' | 'danger' {
  switch (state) {
    case 'done': return 'success'
    case 'failed': return 'danger'
    case 'skipped': return 'neutral'
    default: return 'warning'
  }
}

function applyRepo(res: DependencyTrackRepository): void {
  effective.value = res.effective ?? null
  const o = res.override
  hasOverride.value = !!o
  form.enabled = o?.enabled ?? false
  form.base_url = o?.base_url ?? ''
  form.project_name = o?.project_name ?? ''
  form.project_version = o?.project_version ?? ''
  form.auto_push = toTri(o?.auto_push)
  form.push_vex = toTri(o?.push_vex)
  apiKeySet.value = o?.api_key_set ?? false
  apiKey.value = ''
}

// --- polling -----------------------------------------------------------------

let timer: ReturnType<typeof setInterval> | null = null
let polls = 0
let unmounted = false
// Guards against a slow response for a previously selected repository.
let loadSeq = 0

function stopPolling(): void {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
  }
}

const hasActivePush = (): boolean => pushes.value.some((p) => p.state === 'queued' || p.state === 'running')

function syncPolling(): void {
  if (unmounted || !hasActivePush() || polls >= MAX_POLLS) {
    stopPolling()
    return
  }
  if (timer === null) {
    timer = setInterval(() => void refreshPushes(), POLL_MS)
  }
}

async function refreshPushes(): Promise<void> {
  if (!repo.value) return
  const seq = loadSeq
  polls++
  try {
    const list = await listRepoDependencyTrackPushes(repo.value)
    if (seq !== loadSeq || unmounted) return
    pushes.value = list
  } catch {
    // transient: keep the last list, the next tick retries
  }
  syncPolling()
}

// --- actions -----------------------------------------------------------------

async function loadRepo(): Promise<void> {
  stopPolling()
  polls = 0
  const seq = ++loadSeq
  const name = repo.value
  effective.value = null
  pushes.value = []
  testResult.value = null
  testError.value = ''
  repoError.value = ''
  if (!name) return
  repoLoading.value = true
  try {
    const [cfg, list] = await Promise.all([getRepoDependencyTrack(name), listRepoDependencyTrackPushes(name)])
    if (seq !== loadSeq || unmounted) return
    applyRepo(cfg)
    pushes.value = list
    syncPolling()
  } catch (err) {
    if (seq !== loadSeq) return
    repoError.value = err instanceof Error ? err.message : t('adm.dt.repoLoadFailed')
  } finally {
    if (seq === loadSeq) repoLoading.value = false
  }
}

function onRepoChange(): void {
  void loadRepo()
}

/** `key` undefined keeps the stored key, `''` clears it. */
async function saveOverride(key?: string): Promise<void> {
  if (saving.value || !repo.value) return
  saving.value = true
  try {
    const typed = key ?? (apiKey.value !== '' ? apiKey.value : undefined)
    const res = await saveRepoDependencyTrack(repo.value, {
      enabled: form.enabled,
      base_url: form.base_url,
      ...(typed !== undefined ? { api_key: typed } : {}),
      project_name: form.project_name,
      project_version: form.project_version,
      auto_push: fromTri(form.auto_push),
      push_vex: fromTri(form.push_vex),
    })
    applyRepo(res)
    toasts.success(t('adm.dt.overrideSaved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.dt.overrideSaveFailed'))
  } finally {
    saving.value = false
  }
}

async function removeOverride(): Promise<void> {
  if (saving.value || !repo.value) return
  saving.value = true
  try {
    await deleteRepoDependencyTrack(repo.value)
    toasts.success(t('adm.dt.overrideRemoved'))
    saving.value = false
    await loadRepo()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.dt.overrideRemoveFailed'))
  } finally {
    saving.value = false
  }
}

async function runTest(): Promise<void> {
  if (!repo.value) return
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    testResult.value = await testRepoDependencyTrack(repo.value)
  } catch (err) {
    testError.value = err instanceof Error ? err.message : t('adm.dt.testFailed')
  } finally {
    testing.value = false
  }
}

async function push(): Promise<void> {
  const id = artifactId.value.trim()
  if (!id || !repo.value || pushing.value) return
  pushing.value = true
  try {
    await pushRepoDependencyTrack(repo.value, id)
    toasts.success(t('adm.dt.pushQueued'))
    artifactId.value = ''
    polls = 0
    await refreshPushes()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.dt.pushFailed'))
  } finally {
    pushing.value = false
  }
}

onMounted(async () => {
  try {
    const all = await listAllRepositories()
    if (!unmounted) repos.value = all
  } catch (err) {
    repoError.value = err instanceof Error ? err.message : t('adm.dt.repoLoadFailed')
  }
})

onUnmounted(() => {
  unmounted = true
  stopPolling()
})
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Usage of a project for every member: totals, the project quota against its
  warning threshold, and the per-repository breakdown. Repositories that are
  not metered (proxy and group) are flagged instead of showing a limit.
-->
<template>
  <div class="space-y-6">
    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="failed" class="rounded-xl border border-danger/30 bg-surface p-8 text-center" role="alert">
      <p class="text-sm text-danger">{{ t('projects.usage.loadFailed') }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="load">{{ t('browser.retry') }}</AppButton>
    </div>

    <template v-else-if="usage">
      <section class="rounded-xl border border-border bg-surface p-4 sm:p-5 space-y-5" :aria-label="t('projects.usage.title')" data-testid="usage-totals">
        <h2 class="text-sm font-semibold text-foreground">{{ t('projects.usage.title') }}</h2>

        <div class="grid gap-6 sm:grid-cols-2">
          <div>
            <p class="text-xs text-muted">{{ t('projects.usage.size') }}</p>
            <p class="text-xl font-bold tabular-nums text-foreground" data-testid="usage-size">
              {{ formatBytes(usage.size_bytes ?? 0) }}
              <span v-if="sizeLimited" class="text-sm font-normal text-muted">
                {{ t('projects.usage.sizeOfLimit').replace('{limit}', formatBytes(quota?.max_size_bytes ?? 0)) }}
              </span>
            </p>
            <template v-if="sizeLimited">
              <div
                class="relative mt-3 h-2.5 rounded-full bg-surface-hover overflow-hidden"
                role="progressbar"
                :aria-label="t('projects.usage.size')"
                aria-valuemin="0"
                aria-valuemax="100"
                :aria-valuenow="Math.round(Math.min(sizePercent, 100))"
              >
                <div class="absolute left-0 top-0 bottom-0 rounded-full" :class="usageBarClass(sizePercent, warn)" :style="{ width: `${Math.min(sizePercent, 100)}%` }" data-testid="size-bar" />
              </div>
              <p class="mt-1.5 text-caption text-muted font-mono tabular-nums">{{ t('projects.usage.percentUsed').replace('{n}', sizePercent.toFixed(1)) }}</p>
            </template>
          </div>

          <div>
            <p class="text-xs text-muted">{{ t('projects.usage.artifacts') }}</p>
            <p class="text-xl font-bold tabular-nums text-foreground" data-testid="usage-count">
              {{ usage.artifact_count ?? 0 }}
              <span v-if="countLimited" class="text-sm font-normal text-muted">
                {{ t('projects.usage.countOfLimit').replace('{limit}', String(quota?.max_artifact_count)) }}
              </span>
            </p>
            <template v-if="countLimited">
              <div
                class="relative mt-3 h-2.5 rounded-full bg-surface-hover overflow-hidden"
                role="progressbar"
                :aria-label="t('projects.usage.artifacts')"
                aria-valuemin="0"
                aria-valuemax="100"
                :aria-valuenow="Math.round(Math.min(countPercent, 100))"
              >
                <div class="absolute left-0 top-0 bottom-0 rounded-full" :class="usageBarClass(countPercent, warn)" :style="{ width: `${Math.min(countPercent, 100)}%` }" data-testid="count-bar" />
              </div>
              <p class="mt-1.5 text-caption text-muted font-mono tabular-nums">{{ t('projects.usage.percentUsed').replace('{n}', countPercent.toFixed(1)) }}</p>
            </template>
          </div>
        </div>

        <p v-if="!sizeLimited && !countLimited" class="text-xs text-muted" data-testid="usage-no-limit">
          {{ t('projects.usage.noLimit') }}
        </p>
        <p v-else-if="warn > 0" class="text-xs text-muted">{{ t('projects.usage.warnAt').replace('{n}', String(warn)) }}</p>
      </section>

      <section class="rounded-xl border border-border bg-surface overflow-hidden" :aria-label="t('projects.usage.repositories')">
        <div class="px-4 py-3 border-b border-border">
          <h2 class="text-sm font-semibold text-foreground">{{ t('projects.usage.repositories') }}</h2>
        </div>
        <p v-if="rows.length === 0" class="px-4 py-6 text-sm text-muted">{{ t('projects.usage.empty') }}</p>
        <div v-else class="overflow-x-auto">
          <table class="w-full">
            <thead>
              <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
                <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.usage.column.repository') }}</th>
                <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.usage.column.type') }}</th>
                <th scope="col" class="text-right px-4 py-2.5">{{ t('projects.usage.column.size') }}</th>
                <th scope="col" class="text-right px-4 py-2.5">{{ t('projects.usage.column.artifacts') }}</th>
                <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.usage.column.limit') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              <tr v-for="r in rows" :key="r.name" class="hover:bg-surface-hover/40 transition-colors" data-testid="usage-row">
                <td class="px-4 py-3">
                  <div class="flex items-center gap-2">
                    <router-link :to="`/repos/${encodeURIComponent(r.name ?? '')}`" class="font-mono text-sm font-semibold text-foreground hover:text-primary">{{ r.name }}</router-link>
                    <FormatBadge v-if="r.format" :format="r.format" size="sm" />
                  </div>
                </td>
                <td class="px-4 py-3 text-sm text-muted">{{ r.repo_type || 'hosted' }}</td>
                <td class="px-4 py-3 text-sm text-foreground text-right tabular-nums">{{ formatBytes(r.size_bytes ?? 0) }}</td>
                <td class="px-4 py-3 text-sm text-foreground text-right tabular-nums">{{ r.artifact_count ?? 0 }}</td>
                <td class="px-4 py-3 text-sm text-muted" data-testid="usage-limit">
                  <span v-if="r.quota_enforced === false" :title="t('projects.usage.notMeteredHint')" class="italic">{{ t('projects.usage.notMetered') }}</span>
                  <span v-else>{{ limitLabel(r) }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import FormatBadge from './FormatBadge.vue'
import { getProjectUsage, type ProjectUsage, type ProjectUsageRepository } from '../api/projects'
import { formatBytes, percentOf, usageBarClass } from '../lib/quota'
import { t } from '../i18n/en'

const props = defineProps<{ project: string }>()

const usage = ref<ProjectUsage | null>(null)
const loading = ref(true)
const failed = ref(false)

const quota = computed(() => usage.value?.quota)
const sizeLimited = computed(() => (quota.value?.max_size_bytes ?? 0) > 0)
const countLimited = computed(() => (quota.value?.max_artifact_count ?? 0) > 0)
const warn = computed(() => quota.value?.warn_threshold_percent ?? 0)
const sizePercent = computed(() => percentOf(usage.value?.size_bytes, quota.value?.max_size_bytes))
const countPercent = computed(() => percentOf(usage.value?.artifact_count, quota.value?.max_artifact_count))
const rows = computed(() => usage.value?.repositories ?? [])

function limitLabel(r: ProjectUsageRepository): string {
  const parts: string[] = []
  if ((r.max_size_bytes ?? 0) > 0) parts.push(formatBytes(r.max_size_bytes ?? 0))
  if ((r.max_artifact_count ?? 0) > 0) parts.push(t('projects.usage.countLimit').replace('{n}', String(r.max_artifact_count)))
  return parts.length > 0 ? parts.join(' · ') : t('projects.usage.noRepoLimit')
}

let generation = 0

async function load(): Promise<void> {
  const mine = ++generation
  loading.value = true
  failed.value = false
  try {
    const res = await getProjectUsage(props.project)
    if (mine === generation) usage.value = res
  } catch {
    if (mine === generation) {
      usage.value = null
      failed.value = true
    }
  } finally {
    if (mine === generation) loading.value = false
  }
}

watch(() => props.project, load, { immediate: true })
</script>

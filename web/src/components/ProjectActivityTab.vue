<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Activity of a project for every member: audit events of the project and of the
  repositories it owns, newest first. Keyset paging: "Load more" appends the
  next page; the server returns no total.
-->
<template>
  <section class="rounded-xl border border-border bg-surface overflow-hidden" :aria-label="t('projects.activity.title')">
    <div class="px-4 py-3 border-b border-border flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-sm font-semibold text-foreground">{{ t('projects.activity.title') }}</h2>
        <p class="mt-0.5 text-xs text-muted">{{ t('projects.activity.hint') }}</p>
      </div>
      <AuditExportButtons :download="exportActivity" />
    </div>

    <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="failed && entries.length === 0" class="px-4 py-8 text-center" role="alert">
      <p class="text-sm text-danger">{{ t('projects.activity.loadFailed') }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="loadFirst">{{ t('browser.retry') }}</AppButton>
    </div>
    <p v-else-if="entries.length === 0" class="px-4 py-6 text-sm text-muted" data-testid="activity-empty">{{ t('projects.activity.empty') }}</p>

    <template v-else>
      <div class="overflow-x-auto">
        <table class="w-full">
          <thead>
            <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.activity.column.when') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.activity.column.actor') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.activity.column.action') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.activity.column.details') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="e in entries" :key="e.id" class="hover:bg-surface-hover/40 transition-colors" data-testid="activity-row">
              <td class="px-4 py-3 text-xs text-muted whitespace-nowrap">{{ formatDateTime(e.timestamp) }}</td>
              <td class="px-4 py-3 text-sm font-mono text-foreground">{{ e.actor || '—' }}</td>
              <td class="px-4 py-3 text-sm font-mono text-foreground">{{ e.action }}</td>
              <td class="px-4 py-3 text-sm text-muted">{{ e.message || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="failed" class="px-4 py-3 border-t border-border text-sm text-danger" role="alert">{{ t('projects.activity.loadFailed') }}</div>
      <div v-if="nextCursor" class="px-4 py-3 border-t border-border flex justify-center">
        <AppButton variant="secondary" size="sm" :disabled="loadingMore" data-testid="activity-more" @click="loadMore">
          {{ t('projects.activity.loadMore') }}
        </AppButton>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import AuditExportButtons from './AuditExportButtons.vue'
import { downloadProjectActivityExport, listProjectActivity, type ProjectActivityEntry } from '../api/projects'
import type { ExportFormat } from '../api/audit'
import { formatDateTime } from '../lib/datetime'
import { t } from '../i18n/en'

const PAGE_SIZE = 25

const props = defineProps<{ project: string }>()

const entries = ref<ProjectActivityEntry[]>([])
const nextCursor = ref<string | null>(null)
const loading = ref(true)
const loadingMore = ref(false)
const failed = ref(false)

// Only the latest request for the current project may write state: a slow
// response for a previous project or page must not overwrite newer data.
let generation = 0

async function loadFirst(): Promise<void> {
  const mine = ++generation
  loading.value = true
  loadingMore.value = false
  failed.value = false
  entries.value = []
  nextCursor.value = null
  try {
    const page = await listProjectActivity(props.project, { limit: PAGE_SIZE })
    if (mine !== generation) return
    entries.value = page.items
    nextCursor.value = page.hasMore ? page.nextCursor : null
  } catch {
    if (mine === generation) failed.value = true
  } finally {
    if (mine === generation) loading.value = false
  }
}

async function loadMore(): Promise<void> {
  if (!nextCursor.value || loadingMore.value) return
  const mine = generation
  loadingMore.value = true
  failed.value = false
  try {
    const page = await listProjectActivity(props.project, { cursor: nextCursor.value, limit: PAGE_SIZE })
    if (mine !== generation) return
    entries.value = [...entries.value, ...page.items]
    nextCursor.value = page.hasMore ? page.nextCursor : null
  } catch {
    if (mine === generation) failed.value = true
  } finally {
    if (mine === generation) loadingMore.value = false
  }
}

const exportActivity = (format: ExportFormat) => downloadProjectActivityExport(props.project, format)

watch(() => props.project, loadFirst, { immediate: true })
</script>

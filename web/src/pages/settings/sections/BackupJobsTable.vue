<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <TableScroller>
        <table class="w-full">
          <thead>
            <tr class="bg-surface-hover/30">
              <th scope="col" class="text-left text-caption font-semibold text-muted px-5 py-2.5">{{ t('adm.backup.column.type') }}</th>
              <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.backup.column.status') }}</th>
              <th scope="col" data-drop-order="1" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.backup.column.size') }}</th>
              <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.backup.column.created') }}</th>
              <th scope="col" class="text-right text-caption font-semibold text-muted px-5 py-2.5" :class="ACTIONS_HEAD">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="job in items" :key="job.id" class="group/row hover:bg-surface-hover/30 transition-colors">
              <td class="px-5 py-3.5">
                <span class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border border-border bg-surface-hover text-caption font-mono font-medium text-foreground">
                  <component :is="job.type === 'full' ? ServerStackIcon : CircleStackIcon" class="w-3.5 h-3.5 text-muted" aria-hidden="true" />
                  {{ job.type === 'full' ? t('adm.backup.typeFull') : t('adm.backup.typeMetadata') }}
                </span>
              </td>
              <td class="px-3 py-3.5">
                <span class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-caption font-medium" :class="statusBadgeClass(job.status)">
                  <span class="w-1.5 h-1.5 rounded-full" :class="[statusDotClass(job.status), job.status === 'running' ? 'animate-pulse' : '']" aria-hidden="true" />
                  {{ statusLabel(job.status) }}
                </span>
                <p v-if="job.error_message" class="text-micro text-danger font-mono mt-0.5 max-w-[180px] truncate" :title="job.error_message">
                  {{ job.error_message }}
                </p>
              </td>
              <td class="px-3 py-3.5 text-sm text-muted font-mono">{{ job.size_bytes ? formatBytes(job.size_bytes) : '—' }}</td>
              <td class="px-3 py-3.5">
                <p class="text-sm text-foreground">{{ formatDate(job.created_at) }}</p>
                <p v-if="job.completed_at" class="text-caption text-muted font-mono mt-0.5">
                  {{ t('adm.backup.done') }} {{ timeAgo(job.completed_at) }}
                </p>
              </td>
              <td class="px-5 py-3.5" :class="ACTIONS_CELL">
                <div class="flex items-center gap-0.5 justify-end">
                  <a
                    v-if="job.status === 'completed'"
                    :href="backupDownloadUrl(job.id)"
                    :title="t('adm.backup.download')"
                    :aria-label="t('adm.backup.download')"
                    class="inline-flex h-9 w-9 items-center justify-center rounded-md text-muted hover:text-primary hover:bg-primary/10 transition-colors max-md:h-11 max-md:w-11"
                    download
                  >
                    <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
                  </a>
                  <button
                    v-else
                    type="button"
                    disabled
                    class="inline-flex h-9 w-9 items-center justify-center rounded-md text-muted opacity-30 cursor-not-allowed max-md:h-11 max-md:w-11"
                    :title="t('adm.backup.availableWhenCompleted')"
                    :aria-label="t('adm.backup.availableWhenCompleted')"
                  >
                    <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        </TableScroller>
</template>

<script setup lang="ts">
import { formatDate, timeAgo } from '../../../lib/datetime'
import { formatBytes } from '../../../lib/quota'
import { backupDownloadUrl, type BackupJob } from '../../../api/backup'
import { t } from '../../../i18n'
import TableScroller from '../../../components/ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD } from '../../../components/ui/tableStyles'
import { ArrowDownTrayIcon, ServerStackIcon, CircleStackIcon } from '@heroicons/vue/24/outline'

defineProps<{ items: BackupJob[] }>()

function statusLabel(s: string): string {
  const map: Record<string, string> = {
    pending: t('adm.backup.status.pending'),
    running: t('adm.backup.status.running'),
    completed: t('adm.backup.status.completed'),
    failed: t('adm.backup.status.failed'),
  }
  return map[s] ?? s
}

function statusBadgeClass(s: string): string {
  const map: Record<string, string> = {
    pending: 'bg-surface-hover text-muted',
    running: 'bg-warning-soft text-warning',
    completed: 'bg-success-soft text-success',
    failed: 'bg-danger-soft text-danger',
  }
  return map[s] ?? 'bg-surface-hover text-muted'
}

function statusDotClass(s: string): string {
  const map: Record<string, string> = {
    pending: 'bg-muted',
    running: 'bg-warning',
    completed: 'bg-success',
    failed: 'bg-danger',
  }
  return map[s] ?? 'bg-muted'
}
</script>

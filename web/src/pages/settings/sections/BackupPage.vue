<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Page Head -->
    <SectionActions>
        <div class="flex items-center gap-2 shrink-0">
          <AppButton variant="secondary" size="sm" @click="openRestoreModal">
            <ArrowUpTrayIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.backup.restoreFromArchive') }}
          </AppButton>
          <AppButton size="sm" @click="openCreateModal">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.backup.create') }}
          </AppButton>
        </div>
      </SectionActions>

    <!-- Stat Tiles -->
    <StatTiles class="grid-cols-2 md:grid-cols-3" :tiles="statTiles" />

    <!-- Loading Skeleton -->
    <SkeletonRows v-if="loading" header="h-4 w-32">
      <div class="h-5 w-20 rounded-full skel" />
      <div class="h-5 w-20 rounded-full skel" />
      <div class="h-3 w-24 rounded skel" />
      <div class="h-3 w-28 rounded skel" />
    </SkeletonRows>

    <div v-else-if="loadError && items.length === 0" class="bg-surface rounded-xl border border-border px-5 py-8 text-center text-danger" role="alert">
      {{ loadError }}
    </div>

    <!-- Table -->
    <CardSection v-else :title="t('adm.backup.jobs')">
      <template #badge>
        <span class="px-1.5 py-0.5 rounded-md bg-surface-hover text-caption font-mono font-medium text-muted">{{ items.length }}</span>
      </template>
      <template v-if="polling" #action>
        <span class="flex items-center gap-1.5 text-caption text-muted font-mono" role="status">
          <span class="w-1.5 h-1.5 rounded-full bg-warning animate-pulse" aria-hidden="true" />
          {{ t('adm.backup.autoRefreshing') }}
        </span>
      </template>

      <IconEmptyState
        v-if="items.length === 0"
        :icon="ArchiveBoxIcon"
        :title="t('adm.backup.empty')"
        :hint="t('adm.backup.emptyHint')"
        :action-label="t('adm.backup.create')"
        @action="openCreateModal"
      />

      <TableScroller v-else>
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
    </CardSection>

    <!-- Create backup -->
    <AdminDialog :open="createModalOpen" max-width="max-w-sm" :title="t('adm.backup.create')" @close="closeCreateModal">
      <fieldset class="space-y-2">
        <legend class="block text-xs font-semibold text-foreground mb-2">{{ t('adm.backup.type') }}</legend>
        <button
          v-for="opt in backupTypes"
          :key="opt.value"
          type="button"
          role="radio"
          :aria-checked="createForm.type === opt.value"
          class="w-full text-left p-3 rounded-lg border transition-all focus-visible:ring-2 focus-visible:ring-primary"
          :class="createForm.type === opt.value ? 'border-primary bg-primary/5 ring-1 ring-primary/30' : 'border-border bg-surface hover:bg-surface-hover'"
          @click="createForm.type = opt.value"
        >
          <div class="flex items-start gap-2.5">
            <component :is="opt.icon" class="w-4 h-4 mt-0.5 shrink-0" :class="createForm.type === opt.value ? 'text-primary' : 'text-muted'" aria-hidden="true" />
            <div>
              <p class="text-ui font-semibold text-foreground leading-tight">{{ opt.label }}</p>
              <p class="text-caption text-muted mt-0.5 leading-snug">{{ opt.desc }}</p>
            </div>
          </div>
        </button>
      </fieldset>
      <template #footer>
        <AppButton variant="ghost" @click="closeCreateModal">{{ t('common.cancel') }}</AppButton>
        <AppButton :loading="creating" @click="confirmCreate">
          <ArchiveBoxArrowDownIcon v-if="!creating" class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.backup.create') }}
        </AppButton>
      </template>
    </AdminDialog>

    <!-- Restore from an uploaded archive -->
    <ConfirmDialog
      :open="restoreOpen"
      :title="t('adm.backup.restoreTitle')"
      :description="t('adm.backup.restoreHint')"
      :confirm-label="t('adm.backup.restoreNow')"
      :cancel-label="t('common.cancel')"
      :busy="restoring"
      :confirm-disabled="!restoreFile || restoreConfirmInput !== RESTORE_PHRASE"
      @confirm="confirmRestore"
      @close="cancelRestore"
    >
      <div class="space-y-4">
        <div class="rounded-lg border border-danger/30 bg-danger-soft p-3">
          <div class="flex items-start gap-2.5">
            <ExclamationCircleIcon class="w-4 h-4 text-danger mt-0.5 shrink-0" aria-hidden="true" />
            <div>
              <p class="text-xs font-semibold text-danger">{{ t('adm.backup.destructive') }}</p>
              <p class="text-caption text-danger mt-0.5 leading-relaxed">{{ t('adm.backup.destructiveBody') }}</p>
            </div>
          </div>
        </div>

        <div>
          <label for="restore-file" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.backup.archiveFile') }}</label>
          <input
            id="restore-file"
            type="file"
            accept=".tar.gz,.tgz,application/gzip"
            class="block w-full text-sm text-foreground file:mr-3 file:px-3 file:py-1.5 file:rounded-lg file:border file:border-border-strong file:bg-surface-hover file:text-foreground"
            @change="onFileChosen"
          />
        </div>

        <div>
          <label for="restore-confirm" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('adm.backup.typeToConfirm') }}
            <code class="font-mono text-danger bg-danger-soft px-1 py-0.5 rounded mx-1 text-caption">{{ RESTORE_PHRASE }}</code>
          </label>
          <input
            id="restore-confirm"
            v-model="restoreConfirmInput"
            type="text"
            :placeholder="RESTORE_PHRASE"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-danger transition-colors"
          />
        </div>
      </div>
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import SectionActions from '../../../components/SectionActions.vue'
import { formatDate, timeAgo } from '../../../lib/datetime'
import { formatBytes } from '../../../lib/quota'
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { backupDownloadUrl, createBackup, listBackups, restoreBackup, type BackupJob, type BackupType } from '../../../api/backup'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import AdminDialog from '../../../components/AdminDialog.vue'
import CardSection from '../../../components/ui/CardSection.vue'
import IconEmptyState from '../../../components/ui/IconEmptyState.vue'
import SkeletonRows from '../../../components/ui/SkeletonRows.vue'
import StatTiles, { type StatTile } from '../../../components/ui/StatTiles.vue'
import TableScroller from '../../../components/ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD } from '../../../components/ui/tableStyles'
import {
  PlusIcon,
  ArrowDownTrayIcon,
  ArrowUpTrayIcon,
  ArchiveBoxIcon,
  ArchiveBoxArrowDownIcon,
  ServerStackIcon,
  CircleStackIcon,
  ExclamationCircleIcon,
} from '@heroicons/vue/24/outline'

const RESTORE_PHRASE = 'RESTORE'
const POLL_INTERVAL_MS = 5000

const backupTypes = computed(() => [
  { value: 'full' as BackupType, label: t('adm.backup.typeFull'), desc: t('adm.backup.typeFullDesc'), icon: ServerStackIcon },
  { value: 'metadata-only' as BackupType, label: t('adm.backup.typeMetadata'), desc: t('adm.backup.typeMetadataDesc'), icon: CircleStackIcon },
])

const toasts = useToastStore()

const items = ref<BackupJob[]>([])
const loading = ref(true)
const loadError = ref('')
const polling = ref(false)
let pollTimer: ReturnType<typeof setInterval> | null = null
// A load still in flight when the page unmounts must not restart the poll timer.
let unmounted = false
let inFlight = false

const createModalOpen = ref(false)
const creating = ref(false)
const createForm = ref<{ type: BackupType }>({ type: 'full' })

const restoreOpen = ref(false)
const restoreFile = ref<File | null>(null)
const restoreConfirmInput = ref('')
const restoring = ref(false)

const lastBackup = computed<BackupJob | undefined>(() =>
  items.value.find((j) => j.status === 'completed'),
)

const statTiles = computed<StatTile[]>(() => [
  {
    label: t('adm.backup.stat.total'),
    value: String(items.value.length),
    icon: ArchiveBoxIcon,
    tone: 'primary',
  },
  {
    label: t('adm.backup.stat.last'),
    value: lastBackup.value ? formatDate(lastBackup.value.created_at) : '—',
    icon: ServerStackIcon,
    tone: 'success',
  },
  {
    label: t('adm.backup.stat.lastSize'),
    value: lastBackup.value?.size_bytes ? formatBytes(lastBackup.value.size_bytes) : '—',
    icon: CircleStackIcon,
    tone: 'warning',
  },
])

const hasActiveJobs = computed(() =>
  items.value.some((j) => j.status === 'pending' || j.status === 'running'),
)

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




// Loads the job list. Overlapping calls (slow response vs. the 5s poll) are
// dropped, and failures surface a toast instead of an unhandled rejection.
async function load(): Promise<void> {
  if (inFlight) return
  inFlight = true
  try {
    items.value = [...(await listBackups())].sort(
      (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
    )
    loadError.value = ''
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.backup.loadFailed')
    if (!polling.value) toasts.error(loadError.value)
  } finally {
    loading.value = false
    inFlight = false
  }

  if (unmounted) return
  if (hasActiveJobs.value) startPolling()
  else stopPolling()
}

function startPolling(): void {
  if (pollTimer || unmounted) return
  polling.value = true
  pollTimer = setInterval(() => void load(), POLL_INTERVAL_MS)
}

function stopPolling(): void {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  polling.value = false
}

function openCreateModal(): void {
  createForm.value = { type: 'full' }
  createModalOpen.value = true
}

function closeCreateModal(): void {
  createModalOpen.value = false
}

async function confirmCreate(): Promise<void> {
  if (creating.value) return
  creating.value = true
  try {
    await createBackup(createForm.value.type)
    toasts.success(t('adm.backup.started'))
    closeCreateModal()
    await load()
    startPolling()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.backup.createFailed'))
  } finally {
    creating.value = false
  }
}

function openRestoreModal(): void {
  restoreFile.value = null
  restoreConfirmInput.value = ''
  restoreOpen.value = true
}

function cancelRestore(): void {
  restoreOpen.value = false
}

function onFileChosen(event: Event): void {
  restoreFile.value = (event.target as HTMLInputElement).files?.[0] ?? null
}

// The restore endpoint takes a multipart upload (field "file") and answers
// 202 once the archive is received; the restore itself runs in the background.
async function confirmRestore(): Promise<void> {
  if (!restoreFile.value || restoreConfirmInput.value !== RESTORE_PHRASE || restoring.value) return
  restoring.value = true
  try {
    await restoreBackup(restoreFile.value)
    toasts.success(t('adm.backup.restoreStarted'))
    cancelRestore()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.backup.restoreFailed'))
  } finally {
    restoring.value = false
  }
}

onMounted(load)
onUnmounted(() => {
  unmounted = true
  stopPolling()
})
</script>

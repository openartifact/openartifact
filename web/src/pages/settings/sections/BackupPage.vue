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

      <BackupJobsTable v-else :items="items" />
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

    <BackupRestoreDialog :open="restoreOpen" @close="restoreOpen = false" />
  </div>
</template>

<script setup lang="ts">
import SectionActions from '../../../components/SectionActions.vue'
import { formatDate } from '../../../lib/datetime'
import { formatBytes } from '../../../lib/quota'
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { createBackup, listBackups, type BackupJob, type BackupType } from '../../../api/backup'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n'
import AppButton from '../../../components/AppButton.vue'
import BackupJobsTable from './BackupJobsTable.vue'
import BackupRestoreDialog from './BackupRestoreDialog.vue'
import AdminDialog from '../../../components/AdminDialog.vue'
import CardSection from '../../../components/ui/CardSection.vue'
import IconEmptyState from '../../../components/ui/IconEmptyState.vue'
import SkeletonRows from '../../../components/ui/SkeletonRows.vue'
import StatTiles, { type StatTile } from '../../../components/ui/StatTiles.vue'
import { skipWhileHidden } from '../../../lib/visibility'
import {
  PlusIcon,
  ArrowUpTrayIcon,
  ArchiveBoxIcon,
  ArchiveBoxArrowDownIcon,
  ServerStackIcon,
  CircleStackIcon,
} from '@heroicons/vue/24/outline'

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
  pollTimer = setInterval(skipWhileHidden(() => void load()), POLL_INTERVAL_MS)
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
  restoreOpen.value = true
}

onMounted(load)
onUnmounted(() => {
  unmounted = true
  stopPolling()
})
</script>

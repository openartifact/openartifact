<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <SectionActions>
        <div class="flex items-center gap-2 shrink-0">
          <AppButton variant="secondary" size="sm" :loading="testingAll" :disabled="counts.total === 0" @click="testAll">
            <ArrowPathIcon v-if="!testingAll" class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.storage.testAll') }}
          </AppButton>
          <AppButton size="sm" @click="openCreate">
            <PlusIcon class="w-4 h-4" />
            {{ t('adm.storage.addTitle') }}
          </AppButton>
        </div>
      </SectionActions>

    <!-- ─── Stat Tiles ──────────────────────────────────────────────────────── -->
    <StatTiles class="grid-cols-2 md:grid-cols-4 mt-5" :tiles="statTiles" />

    <!-- ─── Loading Skeleton ────────────────────────────────────────────────── -->
    <SkeletonRows v-if="loading" class="mt-5" header="h-4 w-40" actions>
      <div class="w-8 h-8 rounded-md skel" />
      <div class="h-3 w-32 rounded skel" />
      <div class="h-5 w-24 rounded skel" />
      <div class="h-3 w-40 rounded skel" />
      <div class="flex-1" />
      <div class="h-5 w-16 rounded-full skel" />
    </SkeletonRows>

    <!-- ─── Table ───────────────────────────────────────────────────────────── -->
    <CardSection v-else class="mt-5" :title="t('adm.storage.stat.configured')">
      <template #badge>
        <span class="px-1.5 py-0.5 rounded-md bg-surface-hover text-caption font-mono font-medium text-muted">
          {{ counts.total }}
        </span>
      </template>

      <p v-if="primary" class="px-5 py-3 text-xs text-muted border-b border-border">
        {{ t('adm.storage.primaryHint') }}
      </p>

      <IconEmptyState
        v-if="items.length === 0 && !primary"
        :icon="ServerIcon"
        :title="t('adm.storage.empty')"
        :hint="t('adm.storage.emptyHint')"
        :action-label="t('adm.storage.addTitle')"
        @action="openCreate"
      />

      <StorageBackendsTable
        v-else
        :primary="primary"
        :items="items"
        :testing-one="testingOne"
        :testing-primary="testingPrimary"
        @test="testOne"
        @test-primary="testPrimary"
        @edit="openEdit"
        @delete="openDelete"
      />
    </CardSection>

    <StorageBackendDrawer
      :open="drawerOpen"
      :mode="drawerMode"
      :backend="editingBackend"
      :reload="load"
      @close="closeDrawer"
    />

    <StorageBackendDeleteDialog
      v-model:confirm-name="deleteConfirmName"
      :target="deleteTarget"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="cancelDelete"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ServerIcon, PlusIcon, CheckIcon, ArrowPathIcon, ExclamationCircleIcon } from '@heroicons/vue/24/outline'
import SectionActions from '../../../components/SectionActions.vue'
import AppButton from '../../../components/AppButton.vue'
import CardSection from '../../../components/ui/CardSection.vue'
import IconEmptyState from '../../../components/ui/IconEmptyState.vue'
import SkeletonRows from '../../../components/ui/SkeletonRows.vue'
import StatTiles, { type StatTile } from '../../../components/ui/StatTiles.vue'
import StorageBackendsTable from '../../../components/StorageBackendsTable.vue'
import StorageBackendDrawer from '../../../components/StorageBackendDrawer.vue'
import StorageBackendDeleteDialog from '../../../components/StorageBackendDeleteDialog.vue'
import { useStorageBackends } from '../../../composables/useStorageBackends'
import type { DrawerMode } from '../../../composables/useStorageBackendForm'
import { t } from '../../../i18n'
import { countStatuses, type StorageBackend } from '../../../lib/storageBackends'

const {
  items,
  primary,
  loading,
  testingOne,
  testingPrimary,
  testingAll,
  deleteTarget,
  deleteConfirmName,
  deleting,
  load,
  testOne,
  testPrimary,
  testAll,
  openDelete,
  cancelDelete,
  confirmDelete,
} = useStorageBackends()

// Drawer
const drawerOpen = ref(false)
const drawerMode = ref<DrawerMode>('create')
const editingBackend = ref<StorageBackend | null>(null)

function openCreate(): void {
  drawerMode.value = 'create'
  editingBackend.value = null
  drawerOpen.value = true
}

function openEdit(backend: StorageBackend): void {
  drawerMode.value = 'edit'
  editingBackend.value = backend
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
  editingBackend.value = null
}

// The primary store counts with the records: it is one of the stores.
const counts = computed(() => countStatuses(items.value, primary.value))

const statTiles = computed<StatTile[]>(() => [
  {
    label: t('adm.storage.stat.configured'),
    value: counts.value.total,
    icon: ServerIcon,
    tone: 'primary',
  },
  {
    label: t('adm.storage.status.healthy'),
    value: counts.value.healthy,
    icon: CheckIcon,
    tone: 'success',
  },
  {
    label: t('adm.storage.status.unknown'),
    value: counts.value.unknown,
    icon: ExclamationCircleIcon,
    tone: 'warning',
  },
  {
    label: t('adm.storage.status.unreachable'),
    value: counts.value.unreachable,
    icon: ExclamationCircleIcon,
    tone: 'danger',
  },
])

onMounted(load)
</script>

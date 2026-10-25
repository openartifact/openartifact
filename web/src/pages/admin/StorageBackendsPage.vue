<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('admin.storage.title')" :description="t('adm.storage.description')">
      <template #actions>
        <div class="flex items-center gap-2 shrink-0">
          <AppButton variant="secondary" size="sm" :loading="testingAll" :disabled="items.length === 0" @click="testAll">
            <ArrowPathIcon v-if="!testingAll" class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.storage.testAll') }}
          </AppButton>
          <AppButton size="sm" @click="openCreate">
            <PlusIcon class="w-4 h-4" />
            {{ t('adm.storage.addTitle') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <!-- ─── Stat Tiles ──────────────────────────────────────────────────────── -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3 mt-5">
      <div
        v-for="tile in statTiles"
        :key="tile.label"
        class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3"
      >
        <div
          class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0"
          :style="{ background: tile.iconBg }"
        >
          <component :is="tile.icon" class="w-5 h-5" :style="{ color: tile.iconColor }" />
        </div>
        <div class="min-w-0">
          <p class="text-micro font-mono font-semibold text-muted truncate">
            {{ tile.label }}
          </p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ tile.value }}</p>
        </div>
      </div>
    </div>

    <!-- ─── Loading Skeleton ────────────────────────────────────────────────── -->
    <div v-if="loading" class="mt-5 bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-4 border-b border-border">
        <div class="h-4 w-40 rounded skel" />
      </div>
      <div v-for="i in 3" :key="i" class="flex items-center gap-4 px-5 py-4 border-b border-border last:border-0">
        <div class="w-8 h-8 rounded-md skel" />
        <div class="h-3 w-32 rounded skel" />
        <div class="h-5 w-24 rounded skel" />
        <div class="h-3 w-40 rounded skel" />
        <div class="flex-1" />
        <div class="h-5 w-16 rounded-full skel" />
        <div class="flex gap-1">
          <div class="w-7 h-7 rounded-md skel" />
          <div class="w-7 h-7 rounded-md skel" />
          <div class="w-7 h-7 rounded-md skel" />
        </div>
      </div>
    </div>

    <!-- ─── Table ───────────────────────────────────────────────────────────── -->
    <div v-else class="bg-surface rounded-xl border border-border overflow-hidden mt-5">
      <!-- Table caption row -->
      <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
        <h2 class="text-sm font-semibold text-foreground">{{ t('adm.storage.stat.configured') }}</h2>
        <span class="px-1.5 py-0.5 rounded-md bg-surface-hover text-caption font-mono font-medium text-muted">
          {{ items.length }}
        </span>
      </div>

      <!-- Empty state -->
      <div v-if="items.length === 0" class="py-16 text-center">
        <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
          <ServerIcon class="w-6 h-6 text-muted" />
        </div>
        <p class="text-sm font-medium text-foreground">{{ t('adm.storage.empty') }}</p>
        <p class="text-xs text-muted mt-1 mb-4">{{ t('adm.storage.emptyHint') }}</p>
        <AppButton size="sm" @click="openCreate">
          <PlusIcon class="w-4 h-4" />
          {{ t('adm.storage.addTitle') }}
        </AppButton>
      </div>

      <StorageBackendsTable
        v-else
        :items="items"
        :testing-one="testingOne"
        @test="testOne"
        @edit="openEdit"
        @set-default="setDefault"
        @delete="openDelete"
      />
    </div>

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
import PageHeader from '../../components/PageHeader.vue'
import AppButton from '../../components/AppButton.vue'
import StorageBackendsTable from '../../components/StorageBackendsTable.vue'
import StorageBackendDrawer from '../../components/StorageBackendDrawer.vue'
import StorageBackendDeleteDialog from '../../components/StorageBackendDeleteDialog.vue'
import { useStorageBackends } from '../../composables/useStorageBackends'
import type { DrawerMode } from '../../composables/useStorageBackendForm'
import { t } from '../../i18n/en'
import type { StorageBackend } from '../../lib/storageBackends'

const {
  items,
  loading,
  testingOne,
  testingAll,
  deleteTarget,
  deleteConfirmName,
  deleting,
  load,
  testOne,
  testAll,
  setDefault,
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

const statTiles = computed(() => [
  {
    label: t('adm.storage.stat.configured'),
    value: items.value.length,
    icon: ServerIcon,
    iconBg: 'rgb(var(--color-primary) / 0.1)',
    iconColor: 'rgb(var(--color-primary))',
  },
  {
    label: t('adm.storage.status.healthy'),
    value: items.value.filter((i) => i.status === 'healthy').length,
    icon: CheckIcon,
    iconBg: 'rgb(var(--color-success-soft))',
    iconColor: 'rgb(var(--color-success))',
  },
  {
    label: t('adm.storage.status.unknown'),
    value: items.value.filter((i) => i.status !== 'healthy' && i.status !== 'unreachable').length,
    icon: ExclamationCircleIcon,
    iconBg: 'rgb(var(--color-warning-soft))',
    iconColor: 'rgb(var(--color-warning))',
  },
  {
    label: t('adm.storage.status.unreachable'),
    value: items.value.filter((i) => i.status === 'unreachable').length,
    icon: ExclamationCircleIcon,
    iconBg: 'rgb(var(--color-danger-soft))',
    iconColor: 'rgb(var(--color-danger))',
  },
])

onMounted(load)
</script>

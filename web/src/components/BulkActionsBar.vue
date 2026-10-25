<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <!-- Floating bulk-actions bar – appears at the bottom of the viewport -->
  <Teleport to="body">
    <Transition
      enter-active-class="transition-all duration-300 ease-out"
      enter-from-class="translate-y-full opacity-0"
      enter-to-class="translate-y-0 opacity-100"
      leave-active-class="transition-all duration-200 ease-in"
      leave-from-class="translate-y-0 opacity-100"
      leave-to-class="translate-y-full opacity-0"
    >
      <div
        v-if="selectedItems.length > 0"
        class="fixed bottom-6 left-1/2 -translate-x-1/2 z-50 w-full max-w-2xl px-4"
        role="toolbar"
        :aria-label="`${selectedItems.length} items selected`"
      >
        <div
          class="flex items-center gap-2 px-4 py-3 rounded-xl border border-border bg-surface shadow-2xl shadow-black/20 ring-1 ring-black/5"
        >
          <!-- Count badge -->
          <div class="flex items-center gap-2 shrink-0">
            <span
              class="inline-flex items-center justify-center w-6 h-6 rounded-full bg-primary text-primary-contrast text-xs font-bold"
            >
              {{ selectedItems.length > 99 ? '99+' : selectedItems.length }}
            </span>
            <span class="text-sm font-medium text-foreground whitespace-nowrap">
              {{ selectedItems.length === 1 ? t('bulk.selectedOne') : tp('bulk.selectedMany', { n: String(selectedItems.length) }) }}
            </span>
          </div>

          <div class="w-px h-5 bg-border mx-1 shrink-0" />

          <!-- Actions -->
          <div class="flex items-center gap-1.5 flex-wrap flex-1">
            <!-- Delete -->
            <AppButton variant="danger-outline" size="sm" :disabled="busy" @click="confirmDelete = true">
              <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('bulk.deleteSelected') }}
            </AppButton>
          </div>

          <!-- Progress bar -->
          <div v-if="busy && progress > 0" class="flex items-center gap-2 shrink-0">
            <div class="w-20 h-1.5 bg-border rounded-full overflow-hidden">
              <div
                class="h-full bg-primary rounded-full transition-all duration-300"
                :style="{ width: `${progress}%` }"
              />
            </div>
            <span class="text-xs text-muted tabular-nums">{{ progress }}%</span>
          </div>

          <!-- Dismiss / clear -->
          <AppButton
            variant="ghost"
            size="xs"
            class="shrink-0 w-7 ml-auto"
            :title="t('bulk.clearSelection')"
            :aria-label="t('bulk.clearSelection')"
            @click="emit('clear')"
          >
            <XMarkIcon class="w-4 h-4" aria-hidden="true" />
          </AppButton>
        </div>
      </div>
    </Transition>

    <ConfirmDialog
      :open="confirmDelete"
      :title="tp('bulk.confirmDelete', { n: String(selectedItems.length) })"
      :description="t('bulk.confirmDeleteHint')"
      :confirm-label="t('common.delete')"
      :busy-label="t('bulk.deleting')"
      :busy="busy"
      @confirm="doDelete"
      @close="confirmDelete = false"
    />
  </Teleport>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { TrashIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import { t, tp } from '../i18n/en'
import { deletePackage } from '../api/repositories'
import { ApiError } from '../api/client'

const props = defineProps<{
  selectedItems: string[]
  repoName: string
}>()

const emit = defineEmits<{
  clear: []
  deleted: [ids: string[]]
  error: [message: string]
}>()

const confirmDelete = ref(false)
const busy = ref(false)
const progress = ref(0)

async function doDelete(): Promise<void> {
  busy.value = true
  progress.value = 0
  const toDelete = [...props.selectedItems]
  const succeeded: string[] = []

  try {
    for (let i = 0; i < toDelete.length; i++) {
      const id = toDelete[i]
      try {
        // ids are package (component) ids as returned by the packages list
        await deletePackage(props.repoName, id)
        succeeded.push(id)
      } catch (err) {
        // Session expired: the rest would fail the same way.
        if (err instanceof ApiError && err.status === 401) break
        // Otherwise continue deleting remaining items even if one fails
      }
      progress.value = Math.round(((i + 1) / toDelete.length) * 100)
    }

    confirmDelete.value = false
    emit('deleted', succeeded)

    if (succeeded.length < toDelete.length) {
      emit('error', t('bulk.deleteFailed'))
    }
  } finally {
    busy.value = false
    progress.value = 0
  }
}
</script>

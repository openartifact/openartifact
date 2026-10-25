<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ConfirmDialog
    :open="!!target"
    :title="t('adm.storage.deleteTitle')"
    :description="t('adm.storage.deleteHint')"
    :confirm-label="t('adm.storage.deleteAction')"
    :cancel-label="t('common.cancel')"
    :busy="busy"
    :confirm-disabled="!target || confirmName !== target.name"
    @confirm="emit('confirm')"
    @close="emit('close')"
  >
    <div v-if="target" class="space-y-4">
      <!-- Danger alert -->
      <div class="rounded-lg border border-danger/30 bg-danger-soft p-3">
        <div class="flex items-start gap-2.5">
          <ExclamationCircleIcon class="w-4 h-4 text-danger mt-0.5 shrink-0" aria-hidden="true" />
          <div>
            <p class="text-xs font-semibold text-danger">{{ t('adm.storage.deleteWarnTitle') }}</p>
            <p class="text-caption text-danger mt-0.5 leading-relaxed">{{ t('adm.storage.deleteWarnBody') }}</p>
          </div>
        </div>
      </div>

      <!-- Backend summary -->
      <div class="flex items-center gap-2.5 p-2.5 rounded-lg bg-surface-hover">
        <span class="inline-flex items-center pl-1.5 pr-2 py-1 rounded-md bg-surface border border-border relative overflow-hidden shrink-0">
          <span
            class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-md"
            :style="{ background: typeColor(target.kind) }"
          />
          <span class="pl-1.5 flex items-center gap-1.5">
            <component
              :is="typeIcon(target.kind)"
              class="w-3.5 h-3.5"
              :style="{ color: typeColor(target.kind) }"
            />
            <span class="font-mono text-caption font-medium text-foreground">
              {{ typeMeta(target.kind)?.label }}
            </span>
          </span>
        </span>
        <span class="font-mono text-xs text-muted truncate">
          {{ locationSummary(target) }}
        </span>
      </div>

      <!-- Confirmation input -->
      <div>
        <label for="storage-delete-confirm" class="block text-xs font-semibold text-foreground mb-1.5">
          {{ t('adm.storage.typeToConfirm') }}
          <code class="font-mono text-danger bg-danger-soft px-1 py-0.5 rounded mx-1">{{ target.name }}</code>
        </label>
        <input
          id="storage-delete-confirm"
          v-model="confirmName"
          type="text"
          :placeholder="target.name"
          class="w-full px-3 py-2 rounded-lg border bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 transition-colors border-border-strong focus:ring-danger"
        />
      </div>
    </div>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { ExclamationCircleIcon } from '@heroicons/vue/24/outline'
import ConfirmDialog from './ConfirmDialog.vue'
import { t } from '../i18n/en'
import { locationSummary, typeColor, typeIcon, typeMeta, type StorageBackend } from '../lib/storageBackends'

defineProps<{
  /** The backend pending deletion; the dialog is open while set. */
  target: StorageBackend | null
  busy: boolean
}>()

/** What the user typed to confirm; must equal the backend name. */
const confirmName = defineModel<string>('confirmName', { required: true })

const emit = defineEmits<{
  (e: 'confirm'): void
  (e: 'close'): void
}>()
</script>

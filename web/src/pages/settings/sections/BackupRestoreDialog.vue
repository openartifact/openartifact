<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ConfirmDialog
    :open="open"
    :title="t('adm.backup.restoreTitle')"
    :description="t('adm.backup.restoreHint')"
    :confirm-label="t('adm.backup.restoreNow')"
    :cancel-label="t('common.cancel')"
    :busy="restoring"
    :confirm-disabled="!restoreFile || restoreConfirmInput !== RESTORE_PHRASE"
    @confirm="confirmRestore"
    @close="emit('close')"
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
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { restoreBackup } from '../../../api/backup'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import { ExclamationCircleIcon } from '@heroicons/vue/24/outline'

const RESTORE_PHRASE = 'RESTORE'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const toasts = useToastStore()

const restoreFile = ref<File | null>(null)
const restoreConfirmInput = ref('')
const restoring = ref(false)

// Each opening starts from an empty form.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    restoreFile.value = null
    restoreConfirmInput.value = ''
  },
)

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
    emit('close')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.backup.restoreFailed'))
  } finally {
    restoring.value = false
  }
}
</script>

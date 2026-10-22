<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Shows a freshly minted token once, with a copy button. Open while `token`
  is set; emits `close` to clear it.
-->
<template>
  <AdminDialog :open="token !== null" :title="title" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-warning">{{ t('admin.tokens.reveal') }}</p>
      <div class="bg-surface-hover rounded-lg p-3 font-mono text-xs break-all text-foreground flex items-start gap-3">
        <span class="flex-1" data-testid="token-secret">{{ token }}</span>
        <button
          type="button"
          class="flex-shrink-0 p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover/60 transition-colors"
          :aria-label="t('common.copy')"
          :title="t('common.copy')"
          @click="copy"
        >
          <ClipboardDocumentIcon class="w-4 h-4" aria-hidden="true" />
        </button>
      </div>
    </div>
    <template #footer>
      <AppButton variant="primary" @click="emit('close')">
        {{ t('common.close') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { ClipboardDocumentIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{
  title: string
  /** The clear-text secret; null keeps the dialog closed. */
  token: string | null
}>()
const emit = defineEmits<{ close: [] }>()
const toasts = useToastStore()

async function copy(): Promise<void> {
  if (!props.token) return
  try {
    await navigator.clipboard.writeText(props.token)
    toasts.success(t('settings.tokens.copied'))
  } catch {
    toasts.error(t('settings.tokens.copyFailed'))
  }
}
</script>

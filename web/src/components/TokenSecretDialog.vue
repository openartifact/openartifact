<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Shows a freshly minted token once, with a copy button. Open while `token`
  is set; emits `close` to clear it. The secret cannot be shown again, so the
  dialog only closes (Escape, backdrop, button) after "I have stored this
  token" is ticked.
-->
<template>
  <AdminDialog :open="token !== null" :title="title" @close="requestClose">
    <div class="space-y-4">
      <p class="text-sm text-warning">{{ t('tok.reveal.warning') }}</p>
      <div class="bg-surface-hover rounded-lg p-3 font-mono text-xs break-all text-foreground flex items-start gap-3">
        <span class="flex-1" data-testid="token-secret">{{ token }}</span>
        <CopyButton v-if="token" :text="token" :label="t('tok.reveal.copy')" />
      </div>
      <p v-if="hint" class="text-sm text-muted">{{ hint }}</p>
      <label class="flex min-h-9 items-center gap-2 text-sm text-foreground max-md:min-h-11">
        <input v-model="stored" type="checkbox" data-testid="token-stored" />
        {{ t('tok.reveal.stored') }}
      </label>
    </div>
    <template #footer>
      <AppButton variant="primary" :disabled="!stored" @click="requestClose">
        {{ t('tok.reveal.done') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import CopyButton from './ui/CopyButton.vue'
import { t } from '../i18n'

const props = defineProps<{
  title: string
  /** The clear-text secret; null keeps the dialog closed. */
  token: string | null
  /** One line on what to do with the token. */
  hint?: string
}>()
const emit = defineEmits<{ close: [] }>()

const stored = ref(false)
watch(
  () => props.token,
  () => {
    stored.value = false
  },
)

function requestClose(): void {
  if (stored.value) emit('close')
}
</script>

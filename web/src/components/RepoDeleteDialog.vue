<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog :open="open" :title="t('repoDelete.title')" @close="close">
    <div class="space-y-3 text-sm">
      <p class="text-foreground">{{ bodyText }}</p>
      <p class="text-muted">{{ t('repoDelete.trashHint') }}</p>
      <label for="repo-delete-confirm" class="block text-foreground">
        {{ t('repoDelete.typeName').replace('{name}', name) }}
      </label>
      <input
        id="repo-delete-confirm"
        v-model="typed"
        type="text"
        autocomplete="off"
        spellcheck="false"
        class="w-full px-3 py-2 border border-border rounded-md text-sm font-mono bg-bg text-foreground focus:outline-none focus:ring-2 focus:ring-primary"
      />
    </div>
    <template #footer>
      <button
        type="button"
        class="px-4 py-2 text-sm rounded-md border border-border text-foreground hover:bg-surface-hover disabled:opacity-50"
        :disabled="busy"
        @click="close"
      >{{ t('common.cancel') }}</button>
      <button
        type="button"
        class="px-4 py-2 text-sm rounded-md bg-danger text-danger-contrast hover:opacity-90 disabled:opacity-50"
        :disabled="busy || !matches"
        @click="emit('confirm')"
      >{{ busy ? t('repoDetail.deleting') : t('common.delete') }}</button>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AdminDialog from './AdminDialog.vue'
import { confirmNameMatches } from '../lib/trash'
import { t } from '../i18n/en'

const props = defineProps<{ open: boolean; name: string; busy?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'confirm'): void }>()

const typed = ref('')
const matches = computed(() => confirmNameMatches(typed.value, props.name))
const bodyText = computed(() => t('repoDelete.body').replace('{name}', props.name))

watch(
  () => props.open,
  (o) => {
    if (o) typed.value = ''
  },
)

function close(): void {
  if (!props.busy) emit('close')
}
</script>

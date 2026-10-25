<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ConfirmDialog
    :open="open"
    :title="t('repoDelete.title')"
    :description="bodyText"
    :confirm-label="t('common.delete')"
    :busy-label="t('repoDetail.deleting')"
    :busy="busy"
    :confirm-disabled="!matches"
    @confirm="emit('confirm')"
    @close="emit('close')"
  >
    <div class="space-y-3 text-sm">
      <p class="text-muted">{{ t('repoDelete.trashHint') }}</p>
      <label for="repo-delete-confirm" class="block text-foreground">
        {{ tp('repoDelete.typeName', { name: name }) }}
      </label>
      <input
        id="repo-delete-confirm"
        v-model="typed"
        type="text"
        autocomplete="off"
        spellcheck="false"
        class="w-full px-3 py-2 border border-border-strong rounded-md text-sm font-mono bg-bg text-foreground focus:ring-2 focus:ring-primary"
      />
    </div>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ConfirmDialog from './ConfirmDialog.vue'
import { confirmNameMatches } from '../lib/trash'
import { t, tp } from '../i18n/en'

const props = defineProps<{ open: boolean; name: string; busy?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'confirm'): void }>()

const typed = ref('')
const matches = computed(() => confirmNameMatches(typed.value, props.name))
const bodyText = computed(() => tp('repoDelete.body', { name: props.name }))

watch(
  () => props.open,
  (o) => {
    if (o) typed.value = ''
  },
)
</script>

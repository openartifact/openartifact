<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Confirmation before a cleanup policy is deleted, shared by the repository and the project policy lists. -->
<template>
  <ConfirmDialog
    :open="name !== null"
    :title="t('cleanup.deleteTitle')"
    :confirm-label="t('common.delete')"
    :busy="busy"
    @confirm="emit('confirm')"
    @close="emit('close')"
  >
    <p class="text-sm text-muted">
      <span class="font-mono font-semibold text-foreground">{{ name }}</span>
      {{ t('cleanup.deleteBody') }}
    </p>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import ConfirmDialog from './ConfirmDialog.vue'
import { t } from '../i18n'

/** Name of the policy to delete; `null` keeps the dialog closed. */
defineProps<{ name: string | null; busy: boolean }>()

const emit = defineEmits<{ confirm: []; close: [] }>()
</script>

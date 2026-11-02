<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- A copyable command or config block, optionally labelled with the client it is for. -->
<template>
  <div>
    <p v-if="label" class="mb-1 text-xs font-medium text-muted">{{ label }}</p>
    <div class="flex items-start gap-1 rounded-md border border-border bg-surface-hover pl-3">
      <pre data-testid="snippet" class="min-w-0 flex-1 overflow-x-auto whitespace-pre py-2 font-mono text-xs text-foreground">{{ text }}</pre>
      <CopyButton :text="text" :label="t('ui.state.copyCommand')" />
    </div>
  </div>
</template>

<script setup lang="ts">
import CopyButton from '../ui/CopyButton.vue'
import { t } from '../../i18n'

defineProps<{ text: string; label?: string }>()
</script>

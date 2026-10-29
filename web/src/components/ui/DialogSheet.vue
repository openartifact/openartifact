<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Content of a titled dialog or drawer panel inside `DialogFrame`: a header
  (eyebrow, title, description, close button), a scrolling body and an optional
  `footer` slot for the actions.
-->
<template>
  <div class="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
    <div class="min-w-0">
      <p v-if="eyebrow" class="font-mono text-caption text-muted">{{ eyebrow }}</p>
      <DialogTitle as="h2" class="text-lg font-semibold tracking-tight text-foreground">{{ title }}</DialogTitle>
      <DialogDescription v-if="description" class="mt-1 text-sm text-muted">{{ description }}</DialogDescription>
    </div>
    <button
      type="button"
      :aria-label="t('common.close')"
      class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded text-muted hover:bg-surface-hover hover:text-foreground max-md:h-11 max-md:w-11"
      @click="emit('close')"
    >
      <XMarkIcon class="h-5 w-5" aria-hidden="true" />
    </button>
  </div>
  <div class="nice-scroll flex-1 overflow-y-auto px-5 py-5">
    <slot />
  </div>
  <div
    v-if="$slots.footer"
    class="flex flex-wrap items-center justify-end gap-2 border-t border-border bg-surface-hover/30 px-5 py-3"
  >
    <slot name="footer" />
  </div>
</template>

<script setup lang="ts">
import { DialogDescription, DialogTitle } from '@headlessui/vue'
import { XMarkIcon } from '@heroicons/vue/24/outline'
import { t } from '../../i18n'

defineProps<{ title: string; eyebrow?: string; description?: string }>()

const emit = defineEmits<{ close: [] }>()
</script>

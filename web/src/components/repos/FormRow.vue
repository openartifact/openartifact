<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- One collapsed "default applied" row: label, live summary and an expandable editor. -->
<template>
  <div class="border-b border-border last:border-b-0" :data-row="id">
    <button
      type="button"
      class="flex min-h-11 w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary"
      :aria-expanded="open"
      :aria-controls="`${id}-panel`"
      @click="open = !open"
    >
      <span class="w-32 shrink-0 text-sm font-medium text-foreground max-sm:w-24">{{ label }}</span>
      <span class="min-w-0 flex-1 truncate text-sm text-muted" data-testid="row-summary">{{ summary }}</span>
      <ChevronDownIcon class="h-4 w-4 shrink-0 text-muted transition-transform" :class="{ 'rotate-180': open }" aria-hidden="true" />
    </button>
    <div v-show="open" :id="`${id}-panel`" class="space-y-3 border-t border-border bg-surface-hover/40 px-4 py-3">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ChevronDownIcon } from '@heroicons/vue/20/solid'

defineProps<{ id: string; label: string; summary: string }>()
const open = defineModel<boolean>('open', { default: false })
</script>

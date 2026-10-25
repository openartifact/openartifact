<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  A few mutually exclusive filter choices shown side by side (scope, state).
  Native buttons with `aria-pressed`, so it is a toggle group for assistive
  technology; 44px targets below 768px.
-->
<template>
  <div role="group" :aria-label="label" class="inline-flex rounded-lg border border-border-strong bg-surface p-0.5">
    <button
      v-for="option in options"
      :key="option.value"
      type="button"
      :aria-pressed="option.value === modelValue"
      class="inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
      :class="option.value === modelValue ? 'bg-primary-soft text-primary' : 'text-muted hover:text-foreground'"
      @click="emit('update:modelValue', option.value)"
    >
      {{ option.label }}
      <span v-if="option.count !== undefined" class="font-mono text-micro text-muted">{{ option.count }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
export interface SegmentOption {
  value: string
  label: string
  count?: number
}

defineProps<{
  options: SegmentOption[]
  modelValue: string
  /** Accessible name of the group. */
  label: string
}>()

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
</script>

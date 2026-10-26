<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <span
    class="inline-flex items-center whitespace-nowrap px-2.5 py-0.5 rounded-full text-xs font-medium"
    :class="variantClass"
  >
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

type Variant = 'success' | 'warning' | 'neutral' | 'danger'

const props = withDefaults(
  defineProps<{
    label: string
    variant?: Variant
  }>(),
  { variant: 'neutral' },
)

const variantClass = computed<string>(() => {
  // The semantic tokens already flip on dark/light, so a single class set works
  // for both themes.
  const map: Record<Variant, string> = {
    success: 'bg-success-soft text-success',
    warning: 'bg-warning-soft text-warning',
    neutral: 'bg-surface-hover text-foreground',
    danger: 'bg-danger-soft text-danger',
  }
  return map[props.variant]
})
</script>

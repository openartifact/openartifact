<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <component
    :is="to ? RouterLink : 'button'"
    :to="to"
    :type="to ? undefined : type"
    :disabled="to ? undefined : disabled || loading"
    :aria-busy="loading || undefined"
    :class="classes"
  >
    <ArrowPathIcon v-if="loading" class="w-3.5 h-3.5 animate-spin shrink-0" aria-hidden="true" />
    <slot />
  </component>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, type RouteLocationRaw } from 'vue-router'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'

export type AppButtonVariant = 'primary' | 'secondary' | 'danger' | 'danger-outline' | 'ghost'
export type AppButtonSize = 'xs' | 'sm' | 'md'

const props = withDefaults(
  defineProps<{
    variant?: AppButtonVariant
    size?: AppButtonSize
    /** Shows a spinner and blocks clicks; the label slot stays so the width does not jump. */
    loading?: boolean
    disabled?: boolean
    type?: 'button' | 'submit' | 'reset'
    /** Renders a router link instead of a `<button>`. */
    to?: RouteLocationRaw
    block?: boolean
  }>(),
  { variant: 'primary', size: 'md', loading: false, disabled: false, type: 'button', to: undefined, block: false },
)

const VARIANTS: Record<AppButtonVariant, string> = {
  primary: 'bg-primary text-primary-contrast hover:bg-primary/90',
  // Labelled, so the outline is decorative (unlike an empty input box, which needs border-strong).
  secondary: 'border border-border text-foreground hover:bg-surface-hover',
  danger: 'bg-danger text-danger-contrast hover:bg-danger/90',
  'danger-outline': 'border border-danger/40 text-danger hover:bg-danger/10',
  ghost: 'text-muted hover:text-foreground hover:bg-surface-hover',
}

const SIZES: Record<AppButtonSize, string> = {
  xs: 'h-7 px-2.5 text-caption rounded-md',
  sm: 'h-8 px-3 text-sm rounded-lg',
  md: 'h-9 px-4 text-sm rounded-lg',
}

const classes = computed(() => [
  'inline-flex items-center justify-center gap-1.5 font-medium transition-colors',
  'disabled:opacity-50 disabled:cursor-not-allowed',
  props.block ? 'w-full' : 'shrink-0',
  SIZES[props.size],
  'max-md:min-h-11',
  VARIANTS[props.variant],
])
</script>

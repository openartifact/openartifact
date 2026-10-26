<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div
    class="flex flex-col items-center justify-center px-4 py-12 text-center"
    :role="variant === 'error' ? 'alert' : 'status'"
    :data-state="variant"
  >
    <component :is="icon" class="mb-3 h-8 w-8" :class="iconClass" aria-hidden="true" />
    <h3 class="text-base font-semibold text-foreground">{{ heading }}</h3>
    <p v-if="body" class="mt-1 max-w-md text-sm text-muted">{{ body }}</p>
    <div
      v-if="command"
      class="mt-3 flex max-w-full items-start gap-1 rounded-md border border-border bg-surface-hover pl-3 text-left"
    >
      <code data-testid="state-command" class="min-w-0 overflow-x-auto whitespace-pre py-2 font-mono text-xs text-foreground">{{ command }}</code>
      <CopyButton :text="command" :label="t('ui.state.copyCommand')" />
    </div>
    <div v-if="$slots.action" class="mt-4 flex flex-wrap items-center justify-center gap-2">
      <slot name="action" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  ExclamationTriangleIcon,
  FunnelIcon,
  InboxIcon,
  LockClosedIcon,
  PowerIcon,
} from '@heroicons/vue/24/outline'
import CopyButton from './CopyButton.vue'
import { t } from '../../i18n/en'

export type StateVariant = 'empty' | 'filtered' | 'error' | 'forbidden' | 'off'

const props = withDefaults(
  defineProps<{
    variant?: StateVariant
    /** Overrides the variant's default title. */
    title?: string
    /** Overrides the variant's default hint. */
    hint?: string
    /** A command or config line that gets the user out of this state; rendered with a copy button. */
    command?: string
  }>(),
  { variant: 'empty', title: undefined, hint: undefined, command: undefined },
)

const ICONS = {
  empty: InboxIcon,
  filtered: FunnelIcon,
  error: ExclamationTriangleIcon,
  forbidden: LockClosedIcon,
  off: PowerIcon,
}
const icon = computed(() => ICONS[props.variant])
const iconClass = computed(() => (props.variant === 'error' ? 'text-danger' : 'text-muted'))

const heading = computed(() => props.title ?? t(`ui.state.${props.variant}.title` as 'ui.state.empty.title'))
const body = computed(() => {
  if (props.hint !== undefined) return props.hint
  if (props.variant === 'empty') return ''
  return t(`ui.state.${props.variant}.hint` as 'ui.state.filtered.hint')
})
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  One item of the primary navigation. A locked item (the role cannot use the
  area) stays in the list as a disabled link that says why on a second line;
  in the collapsed rail that reason moves into the tooltip and the accessible
  name, so nothing is conveyed by an icon alone.
-->
<template>
  <component
    :is="locked ? 'span' : RouterLink"
    v-bind="locked ? lockedAttrs : { to, 'aria-current': active ? 'page' : undefined }"
    class="group relative flex w-full items-center gap-3 rounded-md text-ui font-medium transition-[background,color] duration-150
      min-h-11 md:min-h-9 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
    :class="[
      collapsed ? 'justify-center px-0' : 'px-2.5 py-1.5',
      locked
        ? 'cursor-not-allowed text-muted'
        : active
          ? 'bg-surface-hover text-foreground'
          : 'text-foreground/75 hover:bg-surface-hover hover:text-foreground',
    ]"
    v-tooltip="tooltip"
    :data-testid="`nav-${id}`"
  >
    <component
      :is="icon"
      class="h-[18px] w-[18px] shrink-0"
      :class="active && !locked ? 'text-foreground' : 'text-muted group-hover:text-foreground'"
      aria-hidden="true"
    />

    <span v-if="!collapsed" class="min-w-0 flex-1">
      <span class="block truncate">{{ label }}</span>
      <span v-if="locked && reason" :id="`${id}-reason`" class="block text-xs font-normal leading-snug text-muted" data-testid="nav-reason">{{ reason }}</span>
      <span v-else-if="note" class="block text-xs font-normal leading-snug text-muted" data-testid="nav-note">{{ note }}</span>
    </span>
    <span v-else class="sr-only">{{ label }}<template v-if="locked && reason">, {{ reason }}</template></span>
  </component>
</template>

<script setup lang="ts">
import type { Component } from 'vue'
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { vTooltip } from '../lib/tooltip'

const props = defineProps<{
  id: string
  to: string
  label: string
  icon: Component
  collapsed?: boolean
  /** The route lives in this area (decided by the caller from the route prefix). */
  active?: boolean
  /** The role cannot use the area: rendered disabled with `reason`. */
  locked?: boolean
  reason?: string | null
  /** A remark on a usable item, shown under its label. */
  note?: string | null
}>()

// The collapsed rail shows only icons: the label (and why an item is locked, or its note) moves into the
// tooltip, which opens beside the rail on hover and on keyboard focus (lib/tooltip.ts, drawn in the body so the
// 64px rail and its scrolling nav neither clip it nor grow a scrollbar).
const tooltip = computed(() =>
  props.collapsed
    ? { text: props.label, detail: (props.locked ? props.reason : props.note) || undefined, placement: 'right' as const, offset: 16 }
    : null,
)

const lockedAttrs = computed(() => ({
  role: 'link',
  'aria-disabled': 'true',
  tabindex: 0,
  'aria-describedby': props.collapsed ? undefined : `${props.id}-reason`,
}))
</script>

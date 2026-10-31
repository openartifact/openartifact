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
    :data-testid="`nav-${id}`"
    @mouseenter="showTip"
    @mouseleave="hideTip"
    @focusin="showTip"
    @focusout="hideTip"
    @keydown.esc="hideTip"
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

    <!--
      Tooltip of the collapsed rail, on hover and on keyboard focus. It is
      teleported to the body with fixed coordinates: inside the scrolling nav
      it would be clipped by the 64px rail and widen the nav's scroll area
      (a horizontal scrollbar under the icons).
    -->
    <Teleport v-if="collapsed && tip" to="body">
      <span
        role="tooltip"
        class="pointer-events-none fixed z-50 max-w-56 -translate-y-1/2 rounded-md bg-foreground px-2 py-1 text-xs text-background shadow-lg"
        :style="{ top: `${tip.top}px`, left: `${tip.left}px` }"
      >
        {{ label }}<span v-if="(locked && reason) || note" class="block">{{ locked ? reason : note }}</span>
      </span>
    </Teleport>
  </component>
</template>

<script setup lang="ts">
import type { Component } from 'vue'
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'

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

/** Viewport position of the rail tooltip while it is shown, else null. */
const tip = ref<{ top: number; left: number } | null>(null)

function showTip(e: Event) {
  if (!props.collapsed) return
  const link = e.currentTarget as HTMLElement
  const r = link.getBoundingClientRect()
  // Clear of the rail's border, not just of the link inside its padding.
  const edge = link.closest('#app-sidebar')?.getBoundingClientRect().right ?? r.right
  tip.value = { top: r.top + r.height / 2, left: edge + 8 }
}

function hideTip() {
  tip.value = null
}

const lockedAttrs = computed(() => ({
  role: 'link',
  'aria-disabled': 'true',
  tabindex: 0,
  'aria-describedby': props.collapsed ? undefined : `${props.id}-reason`,
}))
</script>

<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Draws the tooltip that `v-tooltip` asks for (lib/tooltip.ts). One instance lives in App.vue;
  the tooltip is teleported to the body with fixed coordinates, so no scrolling or clipping
  ancestor can cut it off, and it flips at the viewport edges.
-->
<template>
  <Teleport to="body">
    <div
      v-if="tip"
      :id="TOOLTIP_ID"
      ref="el"
      role="tooltip"
      class="pointer-events-none z-[70] max-w-64 rounded-md bg-foreground px-2 py-1 text-xs text-background shadow-lg"
      :style="style"
      :data-placement="placement"
    >
      {{ tip.text }}<span v-if="tip.detail" class="block">{{ tip.detail }}</span>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { activeTooltip as tip, hideTooltip, TOOLTIP_ID } from '../../lib/tooltip'
import { useFloating, type ElementRef } from '../../composables/useFloating'
import { useDismiss } from '../../composables/useDismiss'

const el = ref<ElementRef>(null)
const anchor = computed<ElementRef>(() => tip.value?.anchor ?? null)

const { style, placement } = useFloating(anchor, el, () => ({
  open: tip.value !== null,
  placement: tip.value?.placement,
  offset: tip.value?.offset,
}))

// Escape dismisses it (WCAG 1.4.13) and still reaches whatever is underneath; focus stays where it is.
useDismiss({
  open: () => tip.value !== null,
  anchor,
  floating: el,
  onDismiss: () => hideTooltip(),
  consumeEscape: false,
  outsidePress: false,
  returnFocus: false,
})
</script>

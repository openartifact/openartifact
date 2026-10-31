<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  A button that opens a panel of arbitrary content (not a menu of actions: those are Headless UI
  menus placed with `useFloating`). The panel is teleported to the body and fixed next to the
  button, so no overflow container clips it; it flips and shifts at the viewport edges and follows
  the button on scroll and resize.

  Keyboard and focus: opening moves focus into the panel; Escape closes and hands focus back to the
  button; a press outside closes; Tab past either end of the panel closes it and continues from the
  button (the panel is not next to the button in the DOM). Attributes on this component (class,
  aria-label, id, data-testid) land on the button, the default slot is its content, the `panel` slot
  the panel with `close()` (which also returns focus to the button).
-->
<template>
  <button
    :id="id"
    ref="trigger"
    v-bind="$attrs"
    type="button"
    :aria-expanded="open"
    :aria-controls="open ? panelId : undefined"
    @click="toggle"
  >
    <slot :open="open" />
  </button>
  <Teleport to="body">
    <PopupTransition>
      <div
        v-if="open"
        :id="panelId"
        ref="panel"
        tabindex="-1"
        class="z-50 focus:outline-none"
        :class="panelClass"
        :style="style"
        :data-testid="panelTestid"
        :data-placement="placement"
        @keydown.tab="onTab"
      >
        <slot name="panel" :close="close" />
      </div>
    </PopupTransition>
  </Teleport>
</template>

<script setup lang="ts">
import { nextTick, ref, useId } from 'vue'
import PopupTransition from './PopupTransition.vue'
import { useFloating, type ElementRef } from '../../composables/useFloating'
import { useDismiss } from '../../composables/useDismiss'
import type { Placement } from '../../lib/floating'

defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    id?: string
    placement?: Placement
    panelClass?: string
    panelTestid?: string
  }>(),
  { id: undefined, placement: 'bottom-end', panelClass: '', panelTestid: undefined },
)

const open = ref(false)
const trigger = ref<ElementRef>(null)
const panel = ref<ElementRef>(null)
const panelId = useId()

const { style, placement } = useFloating(trigger, panel, () => ({ open: open.value, placement: props.placement, offset: 8 }))

const { restoreFocus } = useDismiss({
  open,
  anchor: trigger,
  floating: panel,
  onDismiss: () => {
    open.value = false
  },
})

async function toggle(): Promise<void> {
  open.value = !open.value
  if (!open.value) return
  await nextTick()
  const el = (panel.value as HTMLElement | null) ?? null
  el?.focus({ preventScroll: true })
}

/** Closes the panel and gives the focus back to the button. */
function close(): void {
  open.value = false
  restoreFocus()
}

function focusables(): HTMLElement[] {
  const el = panel.value as HTMLElement | null
  if (!el) return []
  return [...el.querySelectorAll<HTMLElement>('a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter(
    (n) => !n.hasAttribute('disabled') && n.getAttribute('aria-hidden') !== 'true',
  )
}

// The panel sits at the end of the body: leaving it with Tab must continue from the button, not from the page's last element.
function onTab(e: KeyboardEvent): void {
  const items = focusables()
  const active = document.activeElement
  if (e.shiftKey) {
    if (active === panel.value || active === items[0]) {
      e.preventDefault()
      close()
    }
  } else if (items.length === 0 || active === items[items.length - 1]) {
    // The button has the focus again by the time the browser moves it on, so Tab lands after the button.
    close()
  }
}
</script>

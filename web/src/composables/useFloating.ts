// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref, toValue, watch, type ComponentPublicInstance, type CSSProperties, type MaybeRefOrGetter, type Ref } from 'vue'
import { computePosition, type FloatingOptions, type Placement } from '../lib/floating'

/** An element, or the component instance rendering it (a Headless UI button or panel). */
export type ElementRef = HTMLElement | ComponentPublicInstance | null | undefined

export function elementOf(target: ElementRef): HTMLElement | null {
  if (!target) return null
  const el = target instanceof HTMLElement ? target : (target as ComponentPublicInstance).$el
  return el instanceof HTMLElement ? el : null
}

export interface UseFloatingOptions extends FloatingOptions {
  /**
   * Whether the layer is open. Omitted: it is open while the floating element exists, which is
   * how Headless UI panels behave (rendered only while open).
   */
  open?: MaybeRefOrGetter<boolean>
}

/**
 * Places a floating element (rendered under `<Teleport to="body">`) next to its anchor with
 * fixed viewport coordinates, so no `overflow` ancestor can clip it. It flips to the other
 * side and shifts along the edge when the preferred spot does not fit, and follows the anchor
 * while the page, a scroll container or the window changes (scroll, resize, the box's own size).
 *
 * Bind `style` to the floating element: it stays hidden until the first measurement so the
 * box never flashes at the origin.
 */
export function useFloating(anchor: Ref<ElementRef>, floating: Ref<ElementRef>, options: MaybeRefOrGetter<UseFloatingOptions> = {}) {
  const placed = ref<{ top: number; left: number } | null>(null)
  const placement = ref<Placement>(toValue(options).placement ?? 'bottom')

  const isOpen = computed(() => {
    const open = toValue(options).open
    return open === undefined ? elementOf(floating.value) !== null : toValue(open)
  })

  function update(): void {
    const anchorEl = elementOf(anchor.value)
    const floatingEl = elementOf(floating.value)
    if (!anchorEl || !floatingEl) return
    const rect = anchorEl.getBoundingClientRect()
    const next = computePosition(
      rect,
      { width: floatingEl.offsetWidth, height: floatingEl.offsetHeight },
      { width: document.documentElement.clientWidth, height: document.documentElement.clientHeight },
      toValue(options),
    )
    placed.value = { top: next.top, left: next.left }
    placement.value = next.placement
  }

  watch(
    () => [isOpen.value, elementOf(anchor.value), elementOf(floating.value), toValue(options)] as const,
    ([open, anchorEl, floatingEl], _old, onCleanup) => {
      // The last position is kept while closed: a panel leaving through a transition stays where it was.
      if (!open || !anchorEl || !floatingEl) return
      update()
      const scroll = { capture: true, passive: true } as const
      window.addEventListener('scroll', update, scroll)
      window.addEventListener('resize', update, { passive: true })
      // The box can grow or shrink while open (async content, a wrapped label).
      const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(update)
      observer?.observe(floatingEl)
      onCleanup(() => {
        window.removeEventListener('scroll', update, scroll)
        window.removeEventListener('resize', update)
        observer?.disconnect()
      })
    },
    { flush: 'post', immediate: true },
  )

  const style = computed<CSSProperties>(() =>
    placed.value
      ? { position: 'fixed', top: `${placed.value.top}px`, left: `${placed.value.left}px` }
      : { position: 'fixed', top: '0px', left: '0px', visibility: 'hidden' },
  )

  return { style, placement, update }
}

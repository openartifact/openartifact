// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { toValue, watch, type MaybeRefOrGetter, type Ref } from 'vue'
import { elementOf, type ElementRef } from './useFloating'

export type DismissReason = 'escape' | 'outside' | 'focus'

export interface UseDismissOptions {
  open: MaybeRefOrGetter<boolean>
  /** The trigger: a press on it is not "outside" (it toggles the layer itself) and it receives the focus back. */
  anchor: Ref<ElementRef>
  floating: Ref<ElementRef>
  onDismiss: (reason: DismissReason) => void
  /** Escape dismisses (default true). */
  escape?: boolean
  /**
   * Escape is consumed by the layer (default true): a popover inside a dialog closes before the
   * dialog does. A tooltip sets it to false, so Escape still reaches whatever is underneath.
   */
  consumeEscape?: boolean
  /** A press outside the trigger and the layer dismisses (default true). */
  outsidePress?: boolean
  /** Focus moving to an element outside the trigger and the layer dismisses (default false). */
  focusOutside?: boolean
  /** Escape hands the focus back to the trigger when it was in the layer (default true). */
  returnFocus?: boolean
}

/**
 * Dismissal of a floating layer: Escape, a press outside, optionally focus leaving; and the
 * focus handling that goes with it. Listeners exist only while the layer is open.
 * `restoreFocus` gives the trigger the focus (after a choice was made in the layer).
 */
export function useDismiss(options: UseDismissOptions): { restoreFocus: () => void } {
  const { escape = true, consumeEscape = true, outsidePress = true, focusOutside = false, returnFocus = true } = options

  function inside(target: EventTarget | null): boolean {
    if (!(target instanceof Node)) return false
    return !!(elementOf(options.anchor.value)?.contains(target) || elementOf(options.floating.value)?.contains(target))
  }

  function restoreFocus(): void {
    elementOf(options.anchor.value)?.focus({ preventScroll: true })
  }

  function onKeydown(e: KeyboardEvent): void {
    if (e.key !== 'Escape' || e.isComposing) return
    const active = document.activeElement
    const focusWasInLayer = active === document.body || inside(active)
    if (consumeEscape) e.stopPropagation()
    options.onDismiss('escape')
    if (returnFocus && focusWasInLayer) restoreFocus()
  }

  function onPointerDown(e: Event): void {
    if (!inside(e.target)) options.onDismiss('outside')
  }

  function onFocusIn(e: FocusEvent): void {
    if (!inside(e.target)) options.onDismiss('focus')
  }

  watch(
    () => toValue(options.open),
    (open, _old, onCleanup) => {
      if (!open) return
      // Capture phase on the document: the topmost layer sees Escape first.
      if (escape) document.addEventListener('keydown', onKeydown, true)
      if (outsidePress) document.addEventListener('pointerdown', onPointerDown, true)
      if (focusOutside) document.addEventListener('focusin', onFocusIn, true)
      onCleanup(() => {
        document.removeEventListener('keydown', onKeydown, true)
        document.removeEventListener('pointerdown', onPointerDown, true)
        document.removeEventListener('focusin', onFocusIn, true)
      })
    },
    { immediate: true, flush: 'post' },
  )

  return { restoreFocus }
}

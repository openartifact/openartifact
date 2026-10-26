// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { watch, type Ref } from 'vue'

/**
 * Focus returns to the element that opened a dialog or drawer. Headless UI does
 * not reliably do this after the leave transition, so the opener is remembered
 * when `open` turns true and refocused by `restoreFocus` (wire it to the
 * transition's `after-leave`).
 */
export function useRestoreFocus(open: Ref<boolean>): { restoreFocus: () => void } {
  let opener: HTMLElement | null = null
  watch(
    open,
    (isOpen) => {
      const active = document.activeElement
      if (isOpen && active instanceof HTMLElement && active !== document.body) opener = active
    },
    { flush: 'sync', immediate: true },
  )

  // Deferred: the dialog still marks the page inert until it has unmounted.
  function restoreFocus(): void {
    const target = opener
    opener = null
    setTimeout(() => {
      if (target?.isConnected) target.focus()
    }, 0)
  }

  return { restoreFocus }
}

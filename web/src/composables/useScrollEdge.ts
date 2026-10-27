// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { onMounted, ref, type Ref } from 'vue'
import { useEventListener, useResizeObserver } from '@vueuse/core'

/**
 * Whether a horizontally scrolling element has content hidden to its right
 * (true while the element overflows and is not scrolled to the end). A table uses it to
 * draw the border of its pinned actions column only when that column covers something.
 */
export function useScrollEdge(el: Ref<HTMLElement | null>): Ref<boolean> {
  const stuck = ref(false)

  function update(): void {
    const node = el.value
    if (!node) return
    stuck.value = node.scrollWidth > node.clientWidth + 1 && node.scrollLeft + node.clientWidth < node.scrollWidth - 1
  }

  useEventListener(el, 'scroll', update, { passive: true })
  // The container resizes with the window and the sidebar; its content with the rows and the language.
  useResizeObserver(el, update)
  useResizeObserver(() => el.value?.firstElementChild as HTMLElement | null | undefined, update)
  onMounted(update)

  return stuck
}

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { onBeforeUnmount, onMounted, type Ref } from 'vue'
import { useEventListener, useResizeObserver } from '@vueuse/core'

/** Set on the header and every cell of a column that does not fit; `style.css` hides it. */
export const COL_HIDDEN = 'data-col-hidden'

/**
 * Drops columns, lowest `data-drop-order` first, until the table fits its scroll container.
 * A header opts in with `data-drop-order="1"` (1 goes first); columns without it are never
 * dropped. Measuring the real table instead of guessing breakpoints makes it independent of
 * the language, the data, the font and the sidebar. Returns the dropped column indexes.
 */
export function fitColumns(scroller: HTMLElement): number[] {
  const table = scroller.querySelector('table')
  const headRows = table?.querySelectorAll<HTMLTableRowElement>('thead tr')
  const head = headRows?.[headRows.length - 1]
  if (!table || !head) return []

  for (const el of table.querySelectorAll(`[${COL_HIDDEN}]`)) el.removeAttribute(COL_HIDDEN)
  const heads = [...head.children] as HTMLElement[]
  const order = heads
    .map((th, index) => ({ index, rank: th.dataset.dropOrder === undefined ? NaN : Number(th.dataset.dropOrder) }))
    .filter((c) => Number.isFinite(c.rank))
    .sort((a, b) => a.rank - b.rank)
  if (order.length === 0) return []

  // Rows with a colspan (an empty-state line) are not column-aligned and are left alone.
  const rows = [...table.querySelectorAll<HTMLTableRowElement>('tbody tr')].filter((row) => row.closest('table') === table && row.children.length === heads.length)
  const dropped: number[] = []
  for (const { index } of order) {
    if (scroller.scrollWidth <= scroller.clientWidth + 1) break
    heads[index].setAttribute(COL_HIDDEN, '')
    for (const row of rows) row.children[index].setAttribute(COL_HIDDEN, '')
    dropped.push(index)
  }
  return dropped
}

/**
 * Keeps `fitColumns` current: on mount, when the container or the table resizes (window,
 * sidebar, language, web fonts) and when rows or text change. `enabled` switches it off, for a
 * table that is not a table at that width (the stacked cards of a phone).
 */
export function useColumnFit(scroller: Ref<HTMLElement | null>, enabled: () => boolean = () => true): void {
  let frame = 0

  function run(): void {
    frame = 0
    const el = scroller.value
    if (!el) return
    if (enabled()) fitColumns(el)
    else for (const hidden of el.querySelectorAll(`[${COL_HIDDEN}]`)) hidden.removeAttribute(COL_HIDDEN)
  }
  // One pass per frame: a resize and the mutations it causes collapse into a single measurement.
  function schedule(): void {
    if (!frame) frame = requestAnimationFrame(run)
  }

  useResizeObserver(scroller, schedule)
  useResizeObserver(() => scroller.value?.querySelector('table'), schedule)
  useEventListener(window, 'resize', schedule, { passive: true })

  let mutations: MutationObserver | undefined
  onMounted(() => {
    run()
    if (scroller.value && typeof MutationObserver !== 'undefined') {
      // Only structure and text: the attributes this module sets must not retrigger it.
      mutations = new MutationObserver(schedule)
      mutations.observe(scroller.value, { childList: true, subtree: true, characterData: true })
    }
    void document.fonts?.ready.then(schedule)
  })
  onBeforeUnmount(() => {
    mutations?.disconnect()
    if (frame) cancelAnimationFrame(frame)
  })
}

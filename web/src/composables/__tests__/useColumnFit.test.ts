// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { COL_HIDDEN, fitColumns } from '../useColumnFit'

// happy-dom has no layout, so the scroller reports a width computed from the columns left visible.
const WIDTHS = [300, 120, 100, 90, 150]
let scroller: HTMLElement
let container = 0

function setup(width: number, dropOrder: (number | undefined)[] = [undefined, 2, 1, 3, undefined], bodyRows = 2): void {
  container = width
  scroller = document.createElement('div')
  const head = dropOrder.map((order, i) => `<th${order === undefined ? '' : ` data-drop-order="${order}"`}>H${i}</th>`).join('')
  const row = dropOrder.map((_, i) => `<td>C${i}</td>`).join('')
  scroller.innerHTML = `<table><thead><tr>${head}</tr></thead><tbody>${`<tr>${row}</tr>`.repeat(bodyRows)}<tr><td colspan="5">empty</td></tr></tbody></table>`
  const natural = (): number =>
    [...scroller.querySelectorAll('thead th')].reduce((sum, th, i) => sum + (th.hasAttribute(COL_HIDDEN) ? 0 : WIDTHS[i]), 0)
  Object.defineProperty(scroller, 'scrollWidth', { get: natural })
  Object.defineProperty(scroller, 'clientWidth', { get: () => container })
}

const hiddenHeads = (): string[] => [...scroller.querySelectorAll('thead th')].filter((th) => th.hasAttribute(COL_HIDDEN)).map((th) => th.textContent ?? '')

describe('fitColumns', () => {
  beforeEach(() => document.body.replaceChildren())

  it('leaves a table that fits alone', () => {
    setup(800)
    expect(fitColumns(scroller)).toEqual([])
    expect(hiddenHeads()).toEqual([])
  })

  it('drops the lowest drop order first and stops as soon as the table fits', () => {
    setup(700) // 760 wide: dropping H2 (order 1) is enough
    expect(fitColumns(scroller)).toEqual([2])
    expect(hiddenHeads()).toEqual(['H2'])
    setup(600) // H2 then H1 (order 2)
    expect(fitColumns(scroller)).toEqual([2, 1])
    expect(hiddenHeads()).toEqual(['H1', 'H2'])
  })

  it('hides the cells of a dropped column in every aligned row, not the colspan row', () => {
    setup(700)
    fitColumns(scroller)
    const rows = [...scroller.querySelectorAll('tbody tr')]
    expect(rows.slice(0, 2).map((r) => r.children[2].hasAttribute(COL_HIDDEN))).toEqual([true, true])
    expect(rows[2].children[0].hasAttribute(COL_HIDDEN)).toBe(false)
  })

  it('never drops a column without a drop order, however narrow the container is', () => {
    setup(100)
    expect(fitColumns(scroller)).toEqual([2, 1, 3])
    expect(hiddenHeads()).toEqual(['H1', 'H2', 'H3'])
  })

  it('brings the columns back when the container grows', () => {
    setup(600)
    fitColumns(scroller)
    container = 900
    expect(fitColumns(scroller)).toEqual([])
    expect(hiddenHeads()).toEqual([])
  })

  it('does nothing without a table or without any opted-in column', () => {
    expect(fitColumns(document.createElement('div'))).toEqual([])
    setup(100, [undefined, undefined, undefined, undefined, undefined])
    expect(fitColumns(scroller)).toEqual([])
  })
})

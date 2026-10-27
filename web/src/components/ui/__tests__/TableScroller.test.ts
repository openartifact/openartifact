// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import TableScroller from '../TableScroller.vue'

const TABLE = '<table><thead><tr><th>Name</th><th data-drop-order="1">Size</th><th>Actions</th></tr></thead><tbody><tr><td>a</td><td>1</td><td>x</td></tr></tbody></table>'

type Metrics = { scrollWidth: number; clientWidth: number; scrollLeft: number }

// happy-dom has no layout: the metrics are set by hand and a scroll event makes the component read them.
function metrics(el: Element, m: Metrics): void {
  for (const key of ['scrollWidth', 'clientWidth', 'scrollLeft'] as const) {
    Object.defineProperty(el, key, { value: m[key], configurable: true })
  }
}

describe('TableScroller', () => {
  it('flags a table that has content hidden to its right, so the pinned actions column gets its border', async () => {
    const w = mount(TableScroller, { slots: { default: TABLE } })
    const root = w.element as HTMLElement
    await flushPromises()
    expect(root.hasAttribute('data-stuck')).toBe(false)

    metrics(root, { scrollWidth: 1200, clientWidth: 900, scrollLeft: 0 })
    root.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(root.hasAttribute('data-stuck')).toBe(true)

    // Scrolled to the end: the pinned column covers nothing any more.
    metrics(root, { scrollWidth: 1200, clientWidth: 900, scrollLeft: 300 })
    root.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(root.hasAttribute('data-stuck')).toBe(false)
  })

  it('does not flag a table that fits', async () => {
    const w = mount(TableScroller, { slots: { default: TABLE } })
    const root = w.element as HTMLElement
    await flushPromises()
    metrics(root, { scrollWidth: 900, clientWidth: 900, scrollLeft: 0 })
    root.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(root.hasAttribute('data-stuck')).toBe(false)
  })

  it('drops the opted-in column of a table that does not fit once mounted', async () => {
    const w = mount(TableScroller, { slots: { default: TABLE } })
    const root = w.element as HTMLElement
    // The table is 1200 wide with Size and 1000 without it, in a 1100 wide container.
    Object.defineProperty(root, 'clientWidth', { value: 1100, configurable: true })
    Object.defineProperty(root, 'scrollWidth', { get: () => (root.querySelector('[data-col-hidden]') ? 1000 : 1200), configurable: true })
    root.querySelector('tbody')!.appendChild(document.createElement('tr')).innerHTML = '<td>b</td><td>2</td><td>y</td>'
    await new Promise((resolve) => setTimeout(resolve, 60))
    expect(w.findAll('[data-col-hidden]').map((el) => el.text())).toEqual(['Size', '1', '2'])
  })
})

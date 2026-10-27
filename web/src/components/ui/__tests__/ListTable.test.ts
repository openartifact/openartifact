// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import ListTable, { type ListColumn } from '../ListTable.vue'

const columns: ListColumn[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'size', label: 'Size', sortable: true, align: 'end' },
  { key: 'actions', label: 'Actions', actions: true },
]
const rows = [
  { id: 'a', name: 'alpha', size: 10 },
  { id: 'b', name: 'beta', size: 20 },
]

function render(props: Record<string, unknown> = {}, slots: Record<string, unknown> = {}) {
  return mount(ListTable as never, { props: { columns, caption: 'Packages', rows, ...props }, slots } as never)
}

describe('ListTable', () => {
  it('renders a captioned table with a row per item and cell slots', () => {
    const w = render({}, { 'cell-name': '<template #cell-name="{ row }">[{{ row.name }}]</template>' })
    expect(w.find('caption').text()).toBe('Packages')
    expect(w.findAll('tbody tr')).toHaveLength(2)
    expect(w.find('tbody tr td').text()).toBe('[alpha]')
    expect(w.find('[data-testid=list-summary]').text()).toBe('Showing 1 to 2 of 2')
  })

  it('labels every cell but the first and the actions cell, for the mobile stacked layout', () => {
    const w = render()
    expect(w.findAll('tbody tr:first-child td').map((td) => td.attributes('data-label'))).toEqual(['', 'Size', ''])
  })

  it('drops the stacked label of a self-explanatory cell', () => {
    const w = render({ columns: [columns[0], { key: 'size', label: 'Size', hideLabel: true }, columns[2]] })
    expect(w.findAll('tbody tr:first-child td').map((td) => td.attributes('data-label'))).toEqual(['', '', ''])
  })

  it('places the title cell first and the actions beside it in the mobile card', () => {
    const w = render()
    const [name, size, actions] = w.findAll('tbody tr:first-child td').map((td) => td.classes())
    expect(name).toContain('max-md:flex-1')
    expect(actions).toContain('max-md:order-1')
    expect(size).toContain('max-md:order-3')
  })

  it('shows a skeleton while the first load runs, then rows', async () => {
    const w = render({ rows: [], loading: true })
    expect(w.find('[data-testid=list-loading]').exists()).toBe(true)
    expect(w.attributes('aria-busy')).toBe('true')
    await w.setProps({ rows, loading: false })
    expect(w.find('[data-testid=list-loading]').exists()).toBe(false)
    expect(w.findAll('tbody tr')).toHaveLength(2)
  })

  it('shows an error state with a retry', async () => {
    const w = render({ rows: [], error: 'boom' })
    expect(w.text()).toContain('boom')
    const retry = w.findAll('button').find((b) => /retry|try again/i.test(b.text()))
    expect(retry).toBeDefined()
    await retry!.trigger('click')
    expect(w.emitted('retry')).toHaveLength(1)
  })

  it('tells "nothing yet" from "no matches" and offers clear for the latter', async () => {
    const empty = render({ rows: [], emptyTitle: 'No packages yet' })
    expect(empty.text()).toContain('No packages yet')
    expect(empty.findAll('button')).toHaveLength(0)

    const filtered = render({ rows: [], filtered: true })
    expect(filtered.text()).not.toContain('No packages yet')
    const clear = filtered.findAll('button')[0]
    await clear.trigger('click')
    expect(filtered.emitted('clear')).toHaveLength(1)
  })

  it('exposes sort state with aria-sort and emits sort on header click', async () => {
    const w = render({ sort: 'name', dir: 'desc' })
    const heads = w.findAll('th')
    expect(heads[0].attributes('aria-sort')).toBe('descending')
    expect(heads[1].attributes('aria-sort')).toBe('none')
    expect(heads[2].attributes('aria-sort')).toBeUndefined()
    await heads[1].find('button').trigger('click')
    expect(w.emitted('sort')?.[0]).toEqual(['size'])
  })

  it('pages with previous/next and disables them at the ends', async () => {
    const w = render({ total: 5, limit: 2, page: 1 })
    const [prev, next] = w.findAll('button').filter((b) => /previous|next/i.test(b.text()))
    expect(prev.attributes('disabled')).toBeDefined()
    await next.trigger('click')
    expect(w.emitted('update:page')?.[0]).toEqual([2])
    await w.setProps({ page: 3 })
    const buttons = w.findAll('button').filter((b) => /previous|next/i.test(b.text()))
    expect(buttons[1].attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('Page 3 of 3')
  })

  it('has no pager for a single page', () => {
    expect(render({ total: 2, limit: 25 }).text()).not.toMatch(/Previous|Next/)
  })

  it('loads more with cursor paging', async () => {
    const w = render({ paging: 'cursor', hasMore: true })
    expect(w.find('[data-testid=list-summary]').text()).toBe('Showing 2')
    await w.findAll('button').find((b) => /load more/i.test(b.text()))!.trigger('click')
    expect(w.emitted('load-more')).toHaveLength(1)
    await w.setProps({ hasMore: false })
    expect(w.text()).not.toMatch(/Load more/)
  })

  it('selects rows, all rows, and reports the selection', async () => {
    const w = render({ selectable: true, selected: [] })
    const boxes = w.findAll('input[type=checkbox]')
    expect(boxes).toHaveLength(3)
    await boxes[1].setValue(true)
    expect(w.emitted('update:selected')?.[0]).toEqual([['a']])
    await boxes[0].setValue(true)
    expect(w.emitted('update:selected')?.[1]).toEqual([['a', 'b']])
    await w.setProps({ selected: ['a', 'b'] })
    await w.findAll('input[type=checkbox]')[0].setValue(false)
    expect(w.emitted('update:selected')?.[2]).toEqual([[]])
    await w.setProps({ selected: ['a'] })
    expect((w.find('thead input').element as HTMLInputElement).indeterminate).toBe(true)
  })

  describe('narrow containers', () => {
    it('pins the actions column to the right edge, header and cells alike, and announces it as "Actions"', () => {
      const w = render()
      const head = w.findAll('thead th')[2]
      expect(head.text()).toBe('Actions')
      expect(head.classes()).toEqual(expect.arrayContaining(['md:sticky', 'md:right-0']))
      for (const td of w.findAll('tbody tr td:last-child')) {
        expect(td.classes()).toEqual(expect.arrayContaining(['md:sticky', 'md:right-0', 'md:bg-surface']))
      }
      // Only the actions column is pinned.
      expect(w.findAll('thead th')[0].classes()).not.toContain('md:sticky')
    })

    it('lets the identifier column take the leftover width and truncate', () => {
      const w = render({ columns: [{ ...columns[0], grow: true }, columns[1], columns[2]] })
      expect(w.find('tbody tr td').classes()).toEqual(expect.arrayContaining(['md:w-[40%]', 'md:max-w-0']))
      expect(w.findAll('tbody tr:first-child td')[1].classes()).not.toContain('md:max-w-0')
    })

    it('marks the droppable columns for the fitter and no other', () => {
      const w = render({ columns: [columns[0], { ...columns[1], dropOrder: 2 }, columns[2]] })
      expect(w.findAll('thead th').map((th) => th.attributes('data-drop-order'))).toEqual([undefined, '2', undefined])
    })

    it('keeps the stacked card free of the pin: every pin class is a md: variant', () => {
      const w = render()
      const actions = w.find('tbody tr td:last-child').classes()
      expect(actions.filter((c) => /sticky|right-0|border-l/.test(c)).every((c) => c.startsWith('md:'))).toBe(true)
    })
  })
})

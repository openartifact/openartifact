// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { Cog6ToothIcon, PencilIcon, TrashIcon, ArrowDownTrayIcon } from '@heroicons/vue/20/solid'
import TooltipHost from '../TooltipHost.vue'
import RowActions, { type RowAction } from '../RowActions.vue'

const base: RowAction[] = [
  { key: 'edit', label: 'Edit', icon: PencilIcon },
  { key: 'settings', label: 'Settings', icon: Cog6ToothIcon, to: '/settings' },
  { key: 'delete', label: 'Delete', icon: TrashIcon, tone: 'danger' },
]

async function render(props: Record<string, unknown>, slots: Record<string, string> = {}): Promise<VueWrapper> {
  const page = { template: '<div/>' }
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: page }, { path: '/settings', component: page }] })
  await router.push('/')
  return mount(RowActions as never, { props, slots, attachTo: document.body, global: { plugins: [router] } } as never)
}

describe('RowActions', () => {
  beforeEach(() => document.body.replaceChildren())

  it('renders icon-only controls named "<action>: <subject>"', async () => {
    const w = await render({ actions: base, subject: 'charts' })
    const controls = w.findAll('[data-testid="row-actions"] > *')
    expect(controls.map((c) => c.attributes('aria-label'))).toEqual(['Edit: charts', 'Settings: charts', 'Delete: charts'])
    // No visible text: the label never takes column width.
    expect(controls.map((c) => c.text())).toEqual(['', '', ''])
    expect(w.find('[data-testid="action-more"]').exists()).toBe(false)
  })

  it('shows the bare action as a tooltip on keyboard focus, outside the table', async () => {
    const host = mount(TooltipHost, { attachTo: document.body })
    const w = await render({ actions: base, subject: 'charts' })
    ;(w.get('[data-testid="action-edit"]').element as HTMLElement).focus()
    await flushPromises()
    const tip = document.body.querySelector('[role="tooltip"]')
    expect(tip?.textContent?.trim()).toBe('Edit')
    expect(w.element.contains(tip)).toBe(false)
    // The button is already named "Edit: charts": the tooltip is not read a second time.
    expect(w.get('[data-testid="action-edit"]').attributes('aria-describedby')).toBeUndefined()
    host.unmount()
  })

  it('uses a native button for actions and a link for navigation', async () => {
    const w = await render({ actions: base, subject: 'charts' })
    expect(w.get('[data-testid="action-edit"]').element.tagName).toBe('BUTTON')
    const link = w.get('[data-testid="action-settings"]')
    expect(link.element.tagName).toBe('A')
    expect(link.attributes('href')).toBe('/settings')
  })

  it('reports a clicked button by key', async () => {
    const w = await render({ actions: base })
    await w.get('[data-testid="action-delete"]').trigger('click')
    expect(w.emitted('action')).toEqual([['delete']])
  })

  it('leaves out hidden actions and disables disabled or loading ones', async () => {
    const w = await render({
      actions: [
        { ...base[0], hidden: true },
        { ...base[1], disabled: true },
        { ...base[2], loading: true },
      ],
    })
    expect(w.find('[data-testid="action-edit"]').exists()).toBe(false)
    // A disabled link degrades to a disabled button instead of navigating.
    const settings = w.get('[data-testid="action-settings"]')
    expect(settings.element.tagName).toBe('BUTTON')
    expect(settings.attributes('disabled')).toBeDefined()
    const del = w.get('[data-testid="action-delete"]')
    expect(del.attributes('disabled')).toBeDefined()
    expect(del.attributes('aria-busy')).toBe('true')
  })

  it('reports a toggle as aria-pressed', async () => {
    const w = await render({ actions: [{ ...base[0], pressed: true }, { ...base[2], pressed: false }, base[1]] })
    expect(w.get('[data-testid="action-edit"]').attributes('aria-pressed')).toBe('true')
    expect(w.get('[data-testid="action-delete"]').attributes('aria-pressed')).toBe('false')
  })

  it('has no name suffix without a subject', async () => {
    const w = await render({ actions: base })
    expect(w.get('[data-testid="action-edit"]').attributes('aria-label')).toBe('Edit')
  })

  it('renders the default slot before the actions', async () => {
    const w = await render({ actions: base }, { default: '<button data-testid="extra">Copy</button>' })
    expect(w.get('[data-testid="row-actions"]').element.firstElementChild?.getAttribute('data-testid')).toBe('extra')
  })

  describe('overflow menu', () => {
    const many: RowAction[] = [...base, { key: 'download', label: 'Download', icon: ArrowDownTrayIcon }]

    it('keeps the first actions inline and moves the rest into a "More actions" menu', async () => {
      const w = await render({ actions: many, subject: 'charts' })
      expect(w.findAll('[data-testid="row-actions"] > button, [data-testid="row-actions"] > a').map((c) => c.attributes('data-testid'))).toEqual(['action-edit', 'action-settings'])
      const more = w.get('[data-testid="action-more"]')
      expect(more.attributes('aria-label')).toBe('More actions: charts')
      expect(more.attributes('aria-haspopup')).toBe('menu')
      expect(more.attributes('aria-expanded')).toBe('false')
      expect(document.body.querySelector('[role="menu"]')).toBeNull()
    })

    it('opens a real menu on the body (not clipped by the table) and reports the chosen action', async () => {
      const w = await render({ actions: many, subject: 'charts' })
      await w.get('[data-testid="action-more"]').trigger('click')
      await flushPromises()
      const menu = document.body.querySelector('[role="menu"]')
      expect(menu).not.toBeNull()
      expect(w.element.contains(menu)).toBe(false)
      expect(w.get('[data-testid="action-more"]').attributes('aria-expanded')).toBe('true')
      const items = [...document.body.querySelectorAll('[role="menuitem"]')]
      expect(items.map((i) => i.textContent?.trim())).toEqual(['Delete', 'Download'])

      ;(items[1] as HTMLElement).click()
      await flushPromises()
      expect(w.emitted('action')).toEqual([['download']])
      expect(document.body.querySelector('[role="menu"]')).toBeNull()
    })

    it('closes on Escape and hands focus back to the menu button', async () => {
      const w = await render({ actions: many, subject: 'charts' })
      const more = w.get('[data-testid="action-more"]')
      ;(more.element as HTMLElement).focus()
      await more.trigger('click')
      await flushPromises()
      document.body.querySelector('[role="menu"]')?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await flushPromises()
      expect(document.body.querySelector('[role="menu"]')).toBeNull()
      expect(document.activeElement).toBe(more.element)
    })

    it('honours a different inline limit', async () => {
      const w = await render({ actions: base, inline: 2 })
      expect(w.findAll('[data-testid="row-actions"] > button, [data-testid="row-actions"] > a')).toHaveLength(1)
      expect(w.find('[data-testid="action-more"]').exists()).toBe(true)
    })
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import AppTabs from '../AppTabs.vue'
import type { TabDef } from '../../../composables/useUrlTab'

const tabs: TabDef[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'files', label: 'Files', count: 4 },
  { id: 'security', label: 'Security', flag: '2 critical' },
  { id: 'trash', label: 'Trash', disabled: true, disabledReason: 'Trash is off' },
  { id: 'secret', label: 'Secret', hidden: true },
]

async function mountAt(path: string, props: Record<string, unknown> = {}) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
  await router.push(path)
  const rendered: string[] = []
  const slots = Object.fromEntries(
    tabs.map((t) => [`panel-${t.id}`, () => { rendered.push(t.id); return `panel ${t.id}` }]),
  )
  const wrapper = mount(AppTabs, { props: { tabs, label: 'Artifact', ...props }, slots, attachTo: document.body, global: { plugins: [router] } })
  return { router, wrapper, rendered }
}

describe('AppTabs', () => {
  it('opens the first tab by default and renders only its panel', async () => {
    const { wrapper, rendered } = await mountAt('/')
    expect(wrapper.find('[role=tablist]').attributes('aria-label')).toBe('Artifact')
    expect(wrapper.findAll('[role=tab]').map((t) => t.text())).toEqual(['Overview', 'Files 4', 'Security 2 critical', 'Trash Trash is off'])
    expect(wrapper.find('[role=tab][aria-selected=true]').text()).toBe('Overview')
    expect(rendered).toEqual(['overview'])
    wrapper.unmount()
  })

  it('restores the tab from ?tab= and falls back for unknown, hidden and disabled values', async () => {
    for (const [raw, label] of [['files', 'Files 4'], ['nope', 'Overview'], ['secret', 'Overview'], ['trash', 'Overview']]) {
      const { wrapper } = await mountAt(`/?tab=${raw}`)
      expect(wrapper.find('[role=tab][aria-selected=true]').text()).toBe(label)
      wrapper.unmount()
    }
  })

  it('pushes ?tab= (back steps through tabs); the first tab keeps the URL clean', async () => {
    const { router, wrapper } = await mountAt('/')
    await wrapper.findAll('[role=tab]')[1].trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/?tab=files')
    expect(wrapper.emitted('change')?.[0]).toEqual(['files'])
    await wrapper.findAll('[role=tab]')[0].trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/')
    router.back()
    await new Promise((r) => setTimeout(r, 10))
    expect(router.currentRoute.value.fullPath).toBe('/?tab=files')
    wrapper.unmount()
  })

  it('drops the old tab filters but keeps the declared keys', async () => {
    const { router, wrapper } = await mountAt('/?q=x&env=prod', { keep: ['env'] })
    await wrapper.findAll('[role=tab]')[2].trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ env: 'prod', tab: 'security' })
    wrapper.unmount()
  })

  it('uses roving tabindex and moves between tabs with the arrow keys, Home and End', async () => {
    const { router, wrapper } = await mountAt('/')
    const tabEls = () => wrapper.findAll('[role=tab]')
    expect(tabEls().map((t) => t.attributes('tabindex'))).toEqual(['0', '-1', '-1', '-1'])
    ;(tabEls()[0].element as HTMLElement).focus()
    await tabEls()[0].trigger('keydown', { key: 'ArrowRight' })
    await flushPromises()
    expect(router.currentRoute.value.query.tab).toBe('files')
    expect(document.activeElement).toBe(tabEls()[1].element)
    await tabEls()[1].trigger('keydown', { key: 'End' })
    await flushPromises()
    // End lands on the disabled tab (focus only): the selection stays.
    expect(document.activeElement).toBe(tabEls()[3].element)
    expect(router.currentRoute.value.query.tab).toBe('files')
    await tabEls()[3].trigger('keydown', { key: 'Home' })
    await flushPromises()
    expect(router.currentRoute.value.query.tab).toBeUndefined()
    wrapper.unmount()
  })

  it('does not select a disabled tab and explains why', async () => {
    const { router, wrapper } = await mountAt('/')
    const trash = wrapper.findAll('[role=tab]')[3]
    expect(trash.attributes('aria-disabled')).toBe('true')
    expect(trash.attributes('title')).toBe('Trash is off')
    await trash.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.tab).toBeUndefined()
    wrapper.unmount()
  })

  it('mounts panels lazily, and keeps visited ones with keepAlive', async () => {
    const lazy = await mountAt('/')
    await lazy.wrapper.findAll('[role=tab]')[1].trigger('click')
    await flushPromises()
    expect(lazy.wrapper.findAll('[role=tabpanel]')).toHaveLength(1)
    expect(lazy.wrapper.find('[role=tabpanel]').text()).toBe('panel files')
    lazy.wrapper.unmount()

    const kept = await mountAt('/', { keepAlive: true })
    await kept.wrapper.findAll('[role=tab]')[1].trigger('click')
    await flushPromises()
    const panels = kept.wrapper.findAll('[role=tabpanel]')
    expect(panels).toHaveLength(2)
    expect(panels.map((p) => p.isVisible())).toEqual([false, true])
    kept.wrapper.unmount()
  })

  it('links each tab to its panel through ARIA', async () => {
    const { wrapper } = await mountAt('/')
    const selected = wrapper.find('[role=tab][aria-selected=true]')
    const panel = wrapper.find('[role=tabpanel]')
    expect(selected.attributes('aria-controls')).toBe(panel.attributes('id'))
    expect(panel.attributes('aria-labelledby')).toBe(selected.attributes('id'))
    wrapper.unmount()
  })
})

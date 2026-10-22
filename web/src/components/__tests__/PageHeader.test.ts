// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import PageHeader from '../PageHeader.vue'
import type { CrumbSpec } from '../../lib/crumbs'

async function mountAt(path: string, crumbs: CrumbSpec[] | undefined, props: Record<string, unknown> = {}, slots = {}) {
  const view = { template: '<div />' }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: view },
      { path: '/repos', component: view },
      { path: '/repos/:name', component: view, meta: { crumbs } },
    ],
  })
  await router.push(path)
  await router.isReady()
  return mount(PageHeader, { props: { title: 'Page title', ...props }, slots, global: { plugins: [router] } })
}

const trail: CrumbSpec[] = [
  { key: 'nav.repositories', to: '/repos' },
  { param: 'name', to: '/repos/:name' },
  { key: 'common.edit' },
]

describe('PageHeader', () => {
  it('renders a labelled breadcrumb nav with links and aria-current on the last crumb', async () => {
    const w = await mountAt('/repos/demo', trail)
    const nav = w.get('nav')
    expect(nav.attributes('aria-label')).toBe('Breadcrumbs')
    const items = nav.findAll('li')
    expect(items.map((i) => i.text())).toEqual(['Repositories', 'demo', 'Edit'])
    const links = nav.findAll('a')
    expect(links.map((a) => a.attributes('href'))).toEqual(['/repos', '/repos/demo'])
    // The last crumb is plain text marked as the current page, not a link.
    const current = nav.findAll('span[aria-current="page"]')
    expect(current).toHaveLength(1)
    expect(current[0].text()).toBe('Edit')
    expect(nav.findAll('a').some((a) => a.text() === 'Edit')).toBe(false)
  })

  it('renders the title as the page h1 with description, meta and actions slots', async () => {
    const w = await mountAt('/repos/demo', trail, { description: 'About' }, {
      meta: '<span id="m">meta</span>',
      actions: '<button id="a">act</button>',
    })
    expect(w.get('h1').text()).toBe('Page title')
    expect(w.text()).toContain('About')
    expect(w.find('#m').exists()).toBe(true)
    expect(w.find('#a').exists()).toBe(true)
  })

  it('omits the nav when the route declares no crumbs', async () => {
    const w = await mountAt('/repos/demo', undefined)
    expect(w.find('nav').exists()).toBe(false)
    expect(w.get('h1').text()).toBe('Page title')
  })

  it('lets the crumbs prop override the route trail', async () => {
    const w = await mountAt('/repos/demo', trail, { crumbs: [{ label: 'A', to: '/' }, { label: 'B' }] })
    expect(w.findAll('li').map((i) => i.text())).toEqual(['A', 'B'])
    expect(w.get('span[aria-current="page"]').text()).toBe('B')
  })
})

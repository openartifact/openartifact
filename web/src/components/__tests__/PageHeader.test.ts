// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import PageHeader from '../PageHeader.vue'
import type { CrumbSpec } from '../../lib/crumbs'
import { resetAppInfo } from '../../composables/useAppVersion'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

function serveInfo(body: unknown) {
  return spyOn(globalThis, 'fetch').mockImplementation((async () => json(body)) as never)
}

beforeEach(() => resetAppInfo())

async function mountAt(path: string, crumbs: CrumbSpec[] | undefined, props: Record<string, unknown> = {}, slots = {}) {
  const view = { template: '<div />' }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'dashboard', component: view },
      { path: '/repos', name: 'repos', component: view },
      { path: '/repos/:name', name: 'repo-detail', component: view, meta: { crumbs } },
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

  describe('documentation link', () => {
    it('renders a labelled new-tab link to the page that explains the route', async () => {
      serveInfo({ version: '1.2.3', docs_url: 'https://docs.example.com/v1.2/' })
      const w = await mountAt('/repos/demo', trail)
      await flushPromises()
      const help = w.get('[data-testid="page-help"]')
      expect(help.element.tagName).toBe('A')
      expect(help.attributes('href')).toBe('https://docs.example.com/v1.2/formats/client-setup')
      expect(help.attributes('target')).toBe('_blank')
      expect(help.attributes('rel')).toBe('noopener')
      expect(help.attributes('aria-label')).toBe('Open the documentation for this page (new tab)')
      expect(help.get('svg').attributes('aria-hidden')).toBe('true')
    })

    it('follows the current route', async () => {
      serveInfo({ version: '1.2.3', docs_url: 'https://docs.example.com/v1.2/' })
      const w = await mountAt('/repos', undefined)
      await flushPromises()
      expect(w.get('[data-testid="page-help"]').attributes('href')).toBe(
        'https://docs.example.com/v1.2/repositories/hosted-proxy-group',
      )
    })

    it('is hidden when the server has docs links disabled', async () => {
      serveInfo({ version: '1.2.3' })
      const w = await mountAt('/repos/demo', trail)
      await flushPromises()
      expect(w.find('[data-testid="page-help"]').exists()).toBe(false)
    })

    it('is hidden while the docs base is unknown (request failed)', async () => {
      const w = await mountAt('/repos/demo', trail)
      await flushPromises()
      expect(w.find('[data-testid="page-help"]').exists()).toBe(false)
    })

    it('is hidden on a route that maps to no docs page', async () => {
      serveInfo({ version: '1.2.3', docs_url: 'https://docs.example.com/v1.2/' })
      const w = await mountAt('/', undefined)
      await flushPromises()
      expect(w.find('[data-testid="page-help"]').exists()).toBe(false)
    })
  })
})

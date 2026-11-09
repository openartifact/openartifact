// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import PackagesPage from '../PackagesPage.vue'

const json = (body: unknown): Response => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

const pkg = (name: string, version: string, repository: string) => ({ repository, format: 'npm', name, version, created_at: '2026-01-01T00:00:00Z' })

async function mountPage(recent: unknown[]) {
  setActivePinia(createPinia())
  spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
    const url = String(input)
    if (url.includes('/packages/recent')) return json({ packages: recent })
    if (url.includes('/repositories')) return json({ repositories: [], total: 0 })
    return json({})
  }) as never)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: PackagesPage }] })
  await router.push('/packages')
  await router.isReady()
  const w = mount(PackagesPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

describe('PackagesPage start screen', () => {
  it('offers names to try from the latest publications, one per package', async () => {
    const w = await mountPage([pkg('ui-kit', '2.0.0', 'web'), pkg('ui-kit', '1.0.0', 'web'), pkg('left-pad', '1.0.0', 'libs')])
    const examples = w.findAll('[data-testid="search-example"]')
    expect(examples.map((e) => e.find('code').text())).toEqual(['ui-kit', 'left-pad'])
    expect(w.text()).not.toContain('Nothing here yet')
    w.unmount()
  })

  it('teaches how to publish when nothing was published yet', async () => {
    const w = await mountPage([])
    expect(w.findAll('[data-testid="search-example"]')).toHaveLength(0)
    expect(w.text()).toContain('Nothing here yet')
    w.unmount()
  })

  it('shows an error with a retry instead of the empty-registry hint when the lists fail to load', async () => {
    setActivePinia(createPinia())
    let failing = true
    spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
      const url = String(input)
      if (failing) return new Response(JSON.stringify({ error: 'database down' }), { status: 500, headers: { 'Content-Type': 'application/json' } })
      if (url.includes('/packages/recent')) return json({ packages: [pkg('ui-kit', '1.0.0', 'web')] })
      return json({ repositories: [], total: 0 })
    }) as never)
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: PackagesPage }] })
    await router.push('/packages')
    await router.isReady()
    const w = mount(PackagesPage, { attachTo: document.body, global: { plugins: [router] } })
    await flushPromises()
    expect(w.find('[data-testid="search-load-error"]').text()).toContain('database down')
    expect(w.text()).not.toContain('Nothing here yet')

    failing = false
    await w.find('[data-testid="search-load-error"] button').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="search-load-error"]').exists()).toBe(false)
    expect(w.findAll('[data-testid="search-example"]')).toHaveLength(1)
    w.unmount()
  })
})

describe('PackagesPage project filters', () => {
  const projects = [
    { name: 'sara-research', display_name: 'Sara Research', my_role: 'viewer' },
    { name: 'acme', display_name: 'Acme', my_role: 'owner' },
  ]

  async function mountWith(path: string) {
    setActivePinia(createPinia())
    const f = spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
      const url = String(input)
      if (url.includes('/api/v1/projects')) return json({ projects })
      if (url.includes('/packages/recent')) return json({ packages: [] })
      if (url.includes('/repositories')) return json({ repositories: [], total: 0 })
      return json({ packages: [], total: 0 })
    }) as never)
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: PackagesPage }] })
    await router.push(path)
    await router.isReady()
    const w = mount(PackagesPage, { attachTo: document.body, global: { plugins: [router] } })
    await flushPromises()
    await new Promise((r) => setTimeout(r, 300)) // the query is debounced
    await flushPromises()
    const searches = () =>
      f.mock.calls.map((c) => new URL(String(c[0]), 'http://x')).filter((u) => u.pathname === '/api/v1/search')
    return { w, router, searches }
  }

  it('lists the caller\'s projects by display name and sends the chosen one with "shared with me"', async () => {
    const { w, searches } = await mountWith('/packages?q=lodash&project=acme&shared=1')
    const options = w.findAll('[data-testid="search-project"] option').map((o) => o.text())
    expect(options).toEqual(['All projects', 'Acme', 'Sara Research'])
    expect(w.find('[data-testid="search-shared"]').attributes('aria-pressed')).toBe('true')

    const last = searches().at(-1)!
    expect(last.searchParams.get('project')).toBe('acme')
    expect(last.searchParams.getAll('project_role')).toEqual(['viewer'])
    w.unmount()
  })

  it('puts the filters in the URL when chosen', async () => {
    const { w, router } = await mountWith('/packages?q=lodash')
    await w.find('[data-testid="search-project"]').setValue('sara-research')
    await flushPromises() // each change lands in the URL before the next one
    await w.find('[data-testid="search-shared"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.project).toBe('sara-research')
    expect(router.currentRoute.value.query.shared).toBe('1')
    w.unmount()
  })

  it('keeps a project from the URL that is not in the list', async () => {
    const { w } = await mountWith('/packages?q=lodash&project=elsewhere')
    expect(w.findAll('[data-testid="search-project"] option').map((o) => o.text())).toContain('elsewhere')
    w.unmount()
  })

  it('hides the project filters for a checksum lookup and does not send them', async () => {
    const sha = 'a'.repeat(64)
    const { w, searches } = await mountWith(`/packages?q=${sha}&project=acme&shared=1`)
    expect(w.find('[data-testid="search-project"]').exists()).toBe(false)
    expect(w.find('[data-testid="search-shared"]').exists()).toBe(false)

    const last = searches().at(-1)!
    expect(last.searchParams.get('checksum')).toBe(sha)
    expect(last.searchParams.has('project')).toBe(false)
    expect(last.searchParams.has('project_role')).toBe(false)
    w.unmount()
  })
})

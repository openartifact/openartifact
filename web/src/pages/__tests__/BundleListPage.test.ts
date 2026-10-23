// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import BundleListPage from '../BundleListPage.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const BUNDLES = [
  { id: '1', name: 'shop', version: '2.4.0', state: 'sealed', current_stage: 'qa', created_at: '2026-10-01T10:00:00Z', sealed_at: '2026-10-02T10:00:00Z' },
  { id: '2', name: 'shop', version: '2.5.0', state: 'draft', current_stage: null, created_at: '2026-10-03T10:00:00Z', sealed_at: null },
]

let fetchSpy: Mock<typeof fetch>

async function mountPage(bundles: unknown[] = BUNDLES) {
  setActivePinia(createPinia())
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    if (init?.method === 'POST') return json({ id: '3', name: 'new', version: '1.0.0' }, 201)
    if (url.startsWith('/api/v1/bundles')) return json({ total: bundles.length, page: 1, limit: 25, bundles })
    if (url === '/api/v1/pipelines') return json({ pipelines: [{ id: 'p1', name: 'std', stages: [] }] })
    return json({ total: 0, repositories: [] })
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/bundles', component: BundleListPage },
      { path: '/bundles/:name/:version', component: { template: '<div id="detail" />' } },
    ],
  })
  await router.push('/bundles')
  await router.isReady()
  const wrapper = mount(BundleListPage, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return { wrapper, router }
}

describe('BundleListPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })
  afterEach(() => fetchSpy?.mockRestore())

  it('lists bundles with state, stage and a link to the detail page', async () => {
    const { wrapper } = await mountPage()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('Sealed')
    expect(rows[0]!.text()).toContain('qa')
    expect(rows[0]!.find('a').attributes('href')).toBe('/bundles/shop/2.4.0')
    expect(rows[1]!.text()).toContain('Draft')
    expect(rows[1]!.text()).toContain('Not promoted')
    wrapper.unmount()
  })

  it('shows the empty state', async () => {
    const { wrapper } = await mountPage([])
    expect(wrapper.text()).toContain('No release bundles yet')
    wrapper.unmount()
  })

  it('creates a draft and opens it', async () => {
    const { wrapper, router } = await mountPage()
    const open = wrapper.findAll('button').find((b) => b.text() === 'New bundle')!
    await open.trigger('click')
    await flushPromises()
    const name = document.body.querySelector('#bundle-name') as HTMLInputElement
    const version = document.body.querySelector('#bundle-version') as HTMLInputElement
    name.value = 'new'
    name.dispatchEvent(new Event('input'))
    version.value = '1.0.0'
    version.dispatchEvent(new Event('input'))
    ;(document.body.querySelector('#bundle-create-form') as HTMLFormElement).dispatchEvent(new Event('submit'))
    await flushPromises()

    const post = fetchSpy.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'POST')!
    expect(String(post[0])).toBe('/api/v1/bundles')
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ name: 'new', version: '1.0.0' })
    expect(router.currentRoute.value.fullPath).toBe('/bundles/new/1.0.0')
    wrapper.unmount()
  })
})

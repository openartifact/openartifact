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
  { id: '1', name: 'shop', version: '2.4.0', state: 'sealed', current_stage: 'qa', gpg_key_id: 'K1', created_by: 'ann', created_at: '2026-10-01T10:00:00Z', sealed_at: '2026-10-02T10:00:00Z' },
  { id: '2', name: 'shop', version: '2.5.0', state: 'draft', current_stage: null, created_by: 'bob', created_at: '2026-10-03T10:00:00Z', sealed_at: null },
]
const PIPELINES = [{ id: 'p1', name: 'std', description: 'Standard', stages: [{ name: 'qa', repo_id: 'r1' }, { name: 'prod' }] }]
const REPOS = [{ id: 'r1', name: 'maven-qa', format: 'maven' }]

let fetchSpy: Mock<typeof fetch>
const sent = () => fetchSpy.mock.calls.map((c) => `${(c[1] as RequestInit | undefined)?.method ?? 'GET'} ${String(c[0])}`)

async function mountPage(bundles: unknown[] = BUNDLES, path = '/bundles') {
  setActivePinia(createPinia())
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    if (init?.method === 'POST' && url === '/api/v1/pipelines') return json({ ...PIPELINES[0], id: 'p2', name: 'fast' }, 201)
    if (init?.method === 'POST') return json({ id: '3', name: 'new', version: '1.0.0' }, 201)
    if (url === '/api/v1/pipelines') return json({ pipelines: PIPELINES })
    if (url.startsWith('/api/v1/bundles')) return json({ total: bundles.length, page: 1, limit: 25, bundles })
    return json({ total: REPOS.length, repositories: REPOS })
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/bundles', component: BundleListPage },
      { path: '/bundles/:name/:version', component: { template: '<div id="detail" />' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(BundleListPage, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return { wrapper, router }
}

const type = (selector: string, value: string) => {
  const el = document.body.querySelector(selector) as HTMLInputElement
  el.value = value
  el.dispatchEvent(new Event('input'))
}

describe('BundleListPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })
  afterEach(() => fetchSpy?.mockRestore())

  it('lists bundles with state, signature, stage, author and a link to the detail page', async () => {
    const { wrapper } = await mountPage()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('Sealed')
    expect(rows[0]!.text()).toContain('Signed')
    expect(rows[0]!.text()).toContain('qa')
    expect(rows[0]!.text()).toContain('by ann')
    expect(rows[0]!.find('a').attributes('href')).toBe('/bundles/shop/2.4.0')
    expect(rows[1]!.text()).toContain('Draft')
    expect(rows[1]!.text()).toContain('Not signed')
    expect(rows[1]!.text()).toContain('Not promoted')
    wrapper.unmount()
  })

  it('filters by state through the URL and the API', async () => {
    const { wrapper, router } = await mountPage()
    await wrapper.findAll('button').find((b) => b.text() === 'Sealed')!.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.state).toBe('sealed')
    expect(sent()).toContain('GET /api/v1/bundles?state=sealed&page=1&limit=25')
    wrapper.unmount()
  })

  it('restores the filters from the URL', async () => {
    const { wrapper } = await mountPage(BUNDLES, '/bundles?state=draft&q=shop')
    expect(sent()).toContain('GET /api/v1/bundles?name=shop&state=draft&page=1&limit=25')
    wrapper.unmount()
  })

  it('shows the empty state with the create action', async () => {
    const { wrapper } = await mountPage([])
    expect(wrapper.text()).toContain('No release bundles yet')
    expect(wrapper.findAll('button').some((b) => b.text() === 'New bundle')).toBe(true)
    wrapper.unmount()
  })

  it('creates a draft from the drawer and opens it', async () => {
    const { wrapper, router } = await mountPage()
    await wrapper.get('[data-testid="new-bundle"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.new).toBe('1')
    type('#bundle-name', 'new')
    type('#bundle-version', '1.0.0')
    ;(document.body.querySelector('#bundle-create-form') as HTMLFormElement).dispatchEvent(new Event('submit'))
    await flushPromises()

    const post = fetchSpy.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'POST')!
    expect(String(post[0])).toBe('/api/v1/bundles')
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ name: 'new', version: '1.0.0' })
    expect(router.currentRoute.value.fullPath).toBe('/bundles/new/1.0.0')
    wrapper.unmount()
  })

  it('opens the create drawer from ?new=1 and rejects an invalid name', async () => {
    const { wrapper } = await mountPage(BUNDLES, '/bundles?new=1')
    expect(document.body.querySelector('#bundle-create-form')).not.toBeNull()
    type('#bundle-name', 'a/b')
    type('#bundle-version', '1')
    ;(document.body.querySelector('#bundle-create-form') as HTMLFormElement).dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(document.body.textContent).toContain('Not a valid name or version.')
    expect(sent().some((r) => r.startsWith('POST'))).toBe(false)
    wrapper.unmount()
  })

  it('lists the pipelines with their stages and defines a new one', async () => {
    const { wrapper } = await mountPage()
    await wrapper.get('[data-testid="open-pipelines"]').trigger('click')
    await flushPromises()
    const list = document.body.querySelector('[data-testid="pipeline-list"]')!
    expect(list.textContent).toContain('std')
    expect(list.textContent).toContain('into maven-qa')
    expect(list.textContent).toContain('prod')

    const create = [...document.body.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'New pipeline')!
    create.click()
    await flushPromises()
    type('#pipeline-name', 'fast')
    type('#stage-name-0', 'live')
    ;(document.body.querySelector('#pipeline-create-form') as HTMLFormElement).dispatchEvent(new Event('submit'))
    await flushPromises()

    const post = fetchSpy.mock.calls.find((c) => String(c[0]) === '/api/v1/pipelines' && (c[1] as RequestInit | undefined)?.method === 'POST')!
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ name: 'fast', stages: [{ name: 'live' }] })
    wrapper.unmount()
  })
})

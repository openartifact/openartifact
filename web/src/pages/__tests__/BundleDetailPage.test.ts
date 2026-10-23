// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import BundleDetailPage from '../BundleDetailPage.vue'
import { useToastStore } from '../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const SHA = 'a'.repeat(64)
const PIPELINE = {
  id: 'p1',
  name: 'std',
  description: '',
  stages: [{ name: 'qa' }, { name: 'prod', repo_id: 'r2' }],
}
const REPOS = [
  { id: 'r1', name: 'maven-dev', format: 'maven' },
  { id: 'r2', name: 'maven-prod', format: 'maven' },
]
const ITEM = {
  id: 'i1',
  source_repo_id: 'r1',
  sha256: SHA,
  artifact_path: 'com/acme/shop-1.jar',
  format: 'maven',
  size_bytes: 2048,
}

function bundle(over: Record<string, unknown> = {}) {
  return {
    id: 'b1',
    name: 'shop',
    version: '1.0.0',
    description: 'Release',
    state: 'draft',
    pipeline_id: 'p1',
    current_stage: null,
    created_by: 'admin',
    sealed_at: null,
    sealed_by: null,
    archived_at: null,
    archived_by: null,
    created_at: '2026-10-01T10:00:00Z',
    updated_at: '2026-10-01T10:00:00Z',
    ...over,
  }
}

let state: Record<string, unknown>
let promoteStatus = 200
let promoteUrl = ''
let fetchSpy: Mock<typeof fetch>
const sent = () => fetchSpy.mock.calls.map((c) => `${(c[1] as RequestInit | undefined)?.method ?? 'GET'} ${String(c[0])}`)

function route(url: string, method: string, body?: string): Response {
  if (method === 'GET' && url === '/api/v1/bundles/shop/1.0.0') return json(state)
  if (url.endsWith('/items') && method === 'GET') return json({ items: [ITEM] })
  if (url.endsWith('/promotions')) return json({ promotions: [] })
  if (url === '/api/v1/pipelines') return json({ pipelines: [PIPELINE] })
  if (url.startsWith('/api/v1/repositories?')) return json({ total: 2, repositories: REPOS })
  if (url.startsWith('/api/v1/vuln/scans/summary')) return json({ total: 3, severity_at_least: { critical: 1, high: 2 } })
  if (url === '/api/v1/admin/gpg/keys') return json({ keys: [{ id: 'k-1', fingerprint: 'ABCDEF0123456789', key_id: 'ABCDEF0123456789', name: 'Release key' }], can_store_keys: true })
  if (url.endsWith('/seal')) {
    state = bundle({ state: 'sealed', sealed_at: '2026-10-02T10:00:00Z', sealed_by: 'admin', gpg_key_id: body ? 'ABCDEF0123456789' : null })
    return json(state)
  }
  if (url.includes('/promote')) {
    promoteUrl = url
    if (promoteStatus !== 200) return json({ error: 'conflicting content in the target', code: 'artifact_conflict' }, promoteStatus)
    state = bundle({ state: 'sealed', current_stage: 'qa' })
    return json({ promotion: {}, copied: 2, existing: 1, replaced: 0, items: [] })
  }
  return json({}, 404)
}

async function mountPage() {
  setActivePinia(createPinia())
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) =>
    route(String(input), init?.method ?? 'GET', init?.body as string | undefined),
  )
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/bundles/:name/:version', component: BundleDetailPage }, { path: '/bundles', component: { template: '<div/>' } }, { path: '/security/vulns', component: { template: '<div/>' } }],
  })
  await router.push('/bundles/shop/1.0.0')
  await router.isReady()
  const wrapper = mount(BundleDetailPage, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

const body = () => document.body
const buttonByText = (text: string) =>
  [...body().querySelectorAll('button')].find((b) => b.textContent?.trim() === text) as HTMLButtonElement | undefined

describe('BundleDetailPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    promoteStatus = 200
    promoteUrl = ''
    state = bundle()
  })
  afterEach(() => fetchSpy?.mockRestore())

  it('shows items, the stage trail and source-repository scans for a draft', async () => {
    const wrapper = await mountPage()
    expect(wrapper.text()).toContain('shop 1.0.0')
    expect(wrapper.text()).toContain('com/acme/shop-1.jar')
    expect(wrapper.text()).toContain('maven-dev')
    expect(wrapper.get('[data-testid="stage-trail"]').text()).toContain('qa')
    expect(wrapper.text()).toContain('Security scans')
    expect(wrapper.find('a[href="/security/vulns?repo=maven-dev"]').exists()).toBe(true)
    expect(sent()).toContain('GET /api/v1/vuln/scans/summary?repo=maven-dev')
    expect(wrapper.text()).toContain('Remove')
    wrapper.unmount()
  })

  it('seals with the selected GPG key and then offers promotion', async () => {
    const wrapper = await mountPage()
    buttonByText('Seal')!.click()
    await flushPromises()
    const select = body().querySelector('#seal-key') as HTMLSelectElement
    expect(select.textContent).toContain('Release key')
    select.value = 'k-1'
    select.dispatchEvent(new Event('change'))
    const confirm = [...body().querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Seal').pop()!
    confirm.click()
    await flushPromises()

    const seal = fetchSpy.mock.calls.find((c) => String(c[0]).endsWith('/seal'))!
    expect(JSON.parse((seal[1] as RequestInit).body as string)).toEqual({ gpg_key_id: 'k-1' })
    expect(wrapper.get('[data-testid="signer"]').text()).toBe('ABCDEF0123456789')
    expect(buttonByText('Promote')).toBeDefined()
    expect(wrapper.text()).not.toContain('Remove')
    wrapper.unmount()
  })

  it('promotes to the next stage with the repository the stage demands', async () => {
    state = bundle({ state: 'sealed', sealed_at: '2026-10-02T10:00:00Z' })
    const wrapper = await mountPage()
    buttonByText('Promote')!.click()
    await flushPromises()
    const stage = body().querySelector('#promote-stage') as HTMLSelectElement
    stage.value = 'prod'
    stage.dispatchEvent(new Event('change'))
    await flushPromises()
    expect((body().querySelector('#promote-repo') as HTMLSelectElement).value).toBe('maven-prod')
    ;(body().querySelector('#bundle-promote-form') as HTMLFormElement).dispatchEvent(new Event('submit'))
    await flushPromises()

    const promote = fetchSpy.mock.calls.find((c) => String(c[0]).includes('/promote'))!
    expect(JSON.parse((promote[1] as RequestInit).body as string)).toEqual({ to_stage: 'prod', target_repo: 'maven-prod' })
    expect(promoteUrl).toBe('/api/v1/bundles/shop/1.0.0/promote')
    expect(useToastStore().queue.some((x) => x.description === '2 copied, 1 already present, 0 replaced')).toBe(true)
    wrapper.unmount()
  })

  it('forces a promotion only when asked and shows a conflict message', async () => {
    state = bundle({ state: 'sealed', sealed_at: '2026-10-02T10:00:00Z' })
    promoteStatus = 409
    const wrapper = await mountPage()
    buttonByText('Promote')!.click()
    await flushPromises()
    const repo = body().querySelector('#promote-repo') as HTMLSelectElement
    repo.value = 'maven-dev'
    repo.dispatchEvent(new Event('change'))
    ;(body().querySelector('#bundle-promote-form') as HTMLFormElement).dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(promoteUrl).toBe('/api/v1/bundles/shop/1.0.0/promote')
    expect(useToastStore().queue.some((x) => x.title === 'conflicting content in the target')).toBe(true)

    promoteStatus = 200
    const force = body().querySelector('#promote-force') as HTMLInputElement
    force.checked = true
    force.dispatchEvent(new Event('change'))
    ;(body().querySelector('#bundle-promote-form') as HTMLFormElement).dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(promoteUrl).toBe('/api/v1/bundles/shop/1.0.0/promote?force=true')
    wrapper.unmount()
  })

  it('offers the manifest, and the signature only for a signed bundle', async () => {
    state = bundle({ state: 'sealed', gpg_key_id: 'AB', gpg_signature: '-----BEGIN PGP SIGNATURE-----' })
    let wrapper = await mountPage()
    expect(buttonByText('Manifest')).toBeDefined()
    expect(buttonByText('Signature')).toBeDefined()
    wrapper.unmount()

    document.body.innerHTML = ''
    fetchSpy.mockRestore()
    state = bundle({ state: 'sealed', gpg_key_id: null, gpg_signature: null })
    wrapper = await mountPage()
    expect(buttonByText('Manifest')).toBeDefined()
    expect(buttonByText('Signature')).toBeUndefined()
    expect(wrapper.text()).toContain('Not signed')
    wrapper.unmount()
  })
})

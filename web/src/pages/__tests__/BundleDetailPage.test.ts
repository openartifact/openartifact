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
const SHA2 = 'b'.repeat(64)
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
const HIT = { repository: 'maven-dev', format: 'maven', name: 'lib', version: '2', path: 'com/acme/lib-2.jar', sha256: SHA2, size: 512 }

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
const SEALED = { state: 'sealed', sealed_at: '2026-10-02T10:00:00Z', sealed_by: 'admin' }

let state: Record<string, unknown>
let items: unknown[]
let getStatus = 200
let promoteStatus = 200
const KEY = { id: 'k-1', fingerprint: 'ABCDEF0123456789', key_id: 'ABCDEF0123456789', name: 'Release key', user_id: 'rel', algorithm: 'ed25519', active_global: true }
// What the server answers for the keys the user may seal with, and whether the seal itself is refused.
let signingKeys: unknown[] = [KEY]
let signingKeysStatus = 200
let sealRefused = false
let fetchSpy: Mock<typeof fetch>
const sent = () => fetchSpy.mock.calls.map((c) => `${(c[1] as RequestInit | undefined)?.method ?? 'GET'} ${String(c[0])}`)
// The JSON body of the last request to `match` that sent one.
const bodyOf = (match: string) => {
  const call = fetchSpy.mock.calls.filter((c) => String(c[0]).includes(match) && (c[1] as RequestInit | undefined)?.body).pop()!
  return JSON.parse((call[1] as RequestInit).body as string)
}

function route(url: string, method: string, body?: string): Response {
  if (method === 'GET' && url === '/api/v1/bundles/shop/1.0.0') return getStatus === 200 ? json(state) : json({ error: 'not found' }, getStatus)
  if (url.endsWith('/items') && method === 'GET') return json({ items })
  if (url.endsWith('/items') && method === 'POST') return json({ items: [] }, 201)
  if (url.includes('/items/') && method === 'DELETE') return new Response(null, { status: 204 })
  if (url.endsWith('/promotions')) return json({ promotions: [{ id: 'm1', bundle_id: 'b1', from_stage: null, to_stage: 'qa', status: 'success', promoted_by: 'ann', notes: 'ship it', item_count: 1, started_at: '2026-10-03T10:00:00Z' }] })
  if (url === '/api/v1/pipelines') return json({ pipelines: [PIPELINE] })
  if (url.startsWith('/api/v1/repositories?')) return json({ total: 2, repositories: REPOS })
  if (url.startsWith('/api/v1/vuln/scans/summary')) return json({ total: 3, severity_at_least: { critical: 1, high: 2 } })
  if (url.startsWith('/api/v1/search?')) return json({ packages: [HIT, { ...HIT, path: 'dup.jar' }, { ...ITEM, repository: 'maven-dev', name: 'shop', version: '1', path: ITEM.artifact_path, sha256: SHA, size: 2048 }, { ...HIT, name: 'nosum', sha256: '' }] })
  if (url === '/api/v1/bundles/signing-keys') return signingKeysStatus === 200 ? json({ keys: signingKeys }) : json({ error: 'boom' }, signingKeysStatus)
  if (url.endsWith('/seal') && sealRefused) return json({ error: 'you may not sign bundles with this key', code: 'signer_forbidden' }, 403)
  if (url.endsWith('/seal')) {
    state = bundle({ ...SEALED, gpg_key_id: body ? 'k-1' : null })
    return json(state)
  }
  if (url.endsWith('/archive')) {
    state = bundle({ ...SEALED, state: 'archived', archived_at: '2026-10-09T10:00:00Z', archived_by: 'admin' })
    return json(state)
  }
  if (url.includes('/promote')) {
    if (promoteStatus !== 200) return json({ error: 'conflicting content in the target', code: 'artifact_conflict' }, promoteStatus)
    state = bundle({ ...SEALED, current_stage: 'qa' })
    return json({ promotion: {}, copied: 2, existing: 1, replaced: 0, items: [] })
  }
  if (method === 'DELETE' && url === '/api/v1/bundles/shop/1.0.0') return new Response(null, { status: 204 })
  return json({}, 404)
}

async function mountPage(path = '/bundles/shop/1.0.0') {
  setActivePinia(createPinia())
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) =>
    route(String(input), init?.method ?? 'GET', init?.body as string | undefined),
  )
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/bundles/:name/:version', component: BundleDetailPage },
      { path: '/bundles', component: { template: '<div id="list" />' } },
      { path: '/:rest(.*)*', component: { template: '<div/>' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(BundleDetailPage, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return { wrapper, router }
}

const body = () => document.body
const buttonByText = (text: string) =>
  [...body().querySelectorAll('button')].filter((b) => b.textContent?.trim() === text).pop() as HTMLButtonElement | undefined
const testid = (id: string) => body().querySelector(`[data-testid="${id}"]`) as HTMLButtonElement | null
const setValue = (selector: string, value: string, event = 'input') => {
  const el = body().querySelector(selector) as HTMLInputElement
  el.value = value
  el.dispatchEvent(new Event(event))
}
const submit = (selector: string) => (body().querySelector(selector) as HTMLFormElement).dispatchEvent(new Event('submit'))
const primaries = () => ['primary-seal', 'primary-promote', 'primary-archive'].filter((id) => testid(id))

describe('BundleDetailPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    getStatus = 200
    promoteStatus = 200
    signingKeys = [KEY]
    signingKeysStatus = 200
    sealRefused = false
    state = bundle()
    items = [ITEM]
  })
  afterEach(() => fetchSpy?.mockRestore())

  it('shows the title, the lifecycle trail, the items and one primary action for a draft', async () => {
    const { wrapper } = await mountPage()
    expect(wrapper.text()).toContain('shop 1.0.0')
    expect(wrapper.text()).toContain('Draft')
    const trail = wrapper.get('[data-testid="lifecycle"]')
    expect(trail.text()).toContain('qa')
    expect(trail.find('[aria-current="step"]').text()).toContain('Sealed')
    expect(wrapper.text()).toContain('com/acme/shop-1.jar')
    expect(wrapper.find('a[href="/repos/maven-dev"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="action-remove"]').exists()).toBe(true)
    expect(primaries()).toEqual(['primary-seal'])
    expect((testid('primary-seal') as HTMLButtonElement).disabled).toBe(false)
    wrapper.unmount()
  })

  it('blocks sealing an empty draft and says why', async () => {
    items = []
    const { wrapper } = await mountPage()
    expect((testid('primary-seal') as HTMLButtonElement).disabled).toBe(true)
    expect(wrapper.get('[data-testid="bundle-notices"]').text()).toContain('no items yet')
    wrapper.unmount()
  })

  it('seals with the preselected GPG key and then offers promotion', async () => {
    const { wrapper } = await mountPage()
    testid('primary-seal')!.click()
    await flushPromises()
    const select = body().querySelector('#seal-key') as HTMLSelectElement
    expect(select.textContent).toContain('Release key')
    // The globally active key is preselected: the drawer's own button says it signs.
    expect([...body().querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Seal and sign')).toHaveLength(2)
    buttonByText('Seal and sign')!.click()
    await flushPromises()

    expect(bodyOf('/seal')).toEqual({ gpg_key_id: 'k-1' })
    expect(useToastStore().queue.some((x) => x.title === 'Bundle sealed and signed')).toBe(true)
    expect(primaries()).toEqual(['primary-promote'])
    expect(testid('primary-promote')!.textContent).toContain('Promote to qa')
    expect(wrapper.find('[data-testid="action-remove"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('offers exactly the keys the server lists for the user, never the admin key list or `config`', async () => {
    signingKeys = [KEY, { ...KEY, id: 'k-2', name: 'Nightly key', active_global: false }]
    const { wrapper } = await mountPage()
    testid('primary-seal')!.click()
    await flushPromises()

    const options = [...(body().querySelector('#seal-key') as HTMLSelectElement).options].map((o) => o.value)
    expect(options).toEqual(['k-1', 'k-2', ''])
    expect(sent()).toContain('GET /api/v1/bundles/signing-keys')
    expect(sent().some((c) => c.includes('/admin/gpg'))).toBe(false)
    wrapper.unmount()
  })

  it('hides the signing selector when the user may sign with no key, and seals unsigned', async () => {
    signingKeys = []
    const { wrapper } = await mountPage()
    testid('primary-seal')!.click()
    await flushPromises()

    expect(body().querySelector('#seal-key')).toBeNull()
    expect(testid('seal-no-keys')!.textContent).toContain('No signing key is available to you')
    // Only the page's own action says "Seal and sign"; the drawer's confirm button says it seals unsigned.
    expect([...body().querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Seal and sign')).toHaveLength(1)
    buttonByText('Seal without signing')!.click()
    await flushPromises()

    const seal = fetchSpy.mock.calls.find((c) => String(c[0]).endsWith('/seal'))!
    expect((seal[1] as RequestInit).body).toBeUndefined()
    wrapper.unmount()
  })

  it('says the keys could not be loaded and offers no signing', async () => {
    signingKeysStatus = 500
    const { wrapper } = await mountPage()
    testid('primary-seal')!.click()
    await flushPromises()

    expect(body().querySelector('#seal-key')).toBeNull()
    expect(body().textContent).toContain('Could not load the signing keys.')
    expect(testid('seal-no-keys')).toBeNull()
    wrapper.unmount()
  })

  it('explains a 403 signer_forbidden on seal in the locale and refreshes the keys', async () => {
    sealRefused = true
    const { wrapper } = await mountPage()
    testid('primary-seal')!.click()
    await flushPromises()
    signingKeys = []
    buttonByText('Seal and sign')!.click()
    await flushPromises()

    const alert = body().querySelector('[role=alert]') as HTMLElement
    expect(alert.textContent).toBe('You may not sign bundles with this key any more. The list of keys was refreshed; choose another one or seal without signing.')
    expect(sent().filter((c) => c === 'GET /api/v1/bundles/signing-keys')).toHaveLength(2)
    expect(body().querySelector('#seal-key')).toBeNull()
    wrapper.unmount()
  })

  it('seals without a signature when told not to sign', async () => {
    const { wrapper } = await mountPage()
    testid('primary-seal')!.click()
    await flushPromises()
    setValue('#seal-key', '', 'change')
    await flushPromises()
    buttonByText('Seal without signing')!.click()
    await flushPromises()
    const seal = fetchSpy.mock.calls.find((c) => String(c[0]).endsWith('/seal'))!
    expect((seal[1] as RequestInit).body).toBeUndefined()
    wrapper.unmount()
  })

  it('promotes to the next stage and reports what was copied', async () => {
    state = bundle({ ...SEALED, gpg_key_id: 'k-1' })
    const { wrapper } = await mountPage()
    expect(primaries()).toEqual(['primary-promote'])
    testid('primary-promote')!.click()
    await flushPromises()
    expect(body().textContent).toContain('Critical findings are open')
    expect((body().querySelector('#promote-stage') as HTMLSelectElement).value).toBe('qa')
    setValue('#promote-repo', 'maven-dev', 'change')
    setValue('#promote-notes', ' go ')
    submit('#bundle-promote-form')
    await flushPromises()

    expect(sent()).toContain('POST /api/v1/bundles/shop/1.0.0/promote')
    expect(bodyOf('/promote')).toEqual({ to_stage: 'qa', target_repo: 'maven-dev', notes: 'go' })
    expect(useToastStore().queue.some((x) => x.description === '2 copied, 1 already there, 0 replaced.')).toBe(true)
    wrapper.unmount()
  })

  it('fixes the target repository of a stage that demands one', async () => {
    state = bundle({ ...SEALED, current_stage: 'qa', gpg_key_id: 'k-1' })
    const { wrapper } = await mountPage()
    testid('primary-promote')!.click()
    await flushPromises()
    const repo = body().querySelector('#promote-repo') as HTMLSelectElement
    expect(repo.value).toBe('maven-prod')
    expect(repo.disabled).toBe(true)
    wrapper.unmount()
  })

  it('keeps the drawer open on a conflict and forces only when asked', async () => {
    state = bundle({ ...SEALED, gpg_key_id: 'k-1' })
    promoteStatus = 409
    const { wrapper } = await mountPage()
    testid('primary-promote')!.click()
    await flushPromises()
    setValue('#promote-repo', 'maven-dev', 'change')
    submit('#bundle-promote-form')
    await flushPromises()
    expect(sent()).toContain('POST /api/v1/bundles/shop/1.0.0/promote')
    expect(testid('promote-error')!.textContent).toBe('conflicting content in the target')

    promoteStatus = 200
    const force = body().querySelector('#promote-force') as HTMLInputElement
    force.checked = true
    force.dispatchEvent(new Event('change'))
    submit('#bundle-promote-form')
    await flushPromises()
    expect(sent()).toContain('POST /api/v1/bundles/shop/1.0.0/promote?force=true')
    wrapper.unmount()
  })

  it('archives a bundle that reached its last stage, after confirming', async () => {
    state = bundle({ ...SEALED, current_stage: 'prod', gpg_key_id: 'k-1' })
    const { wrapper } = await mountPage()
    expect(primaries()).toEqual(['primary-archive'])
    expect(wrapper.get('[data-testid="bundle-notices"]').text()).toContain('last stage')
    testid('primary-archive')!.click()
    await flushPromises()
    expect(sent().some((r) => r.endsWith('/archive'))).toBe(false)
    expect(body().textContent).toContain('Archive this bundle?')
    const confirm = [...body().querySelectorAll('[role="dialog"] button')].find((b) => b.textContent?.trim() === 'Archive') as HTMLButtonElement
    confirm.click()
    await flushPromises()

    expect(sent()).toContain('POST /api/v1/bundles/shop/1.0.0/archive')
    expect(primaries()).toEqual([])
    expect(wrapper.get('[data-testid="bundle-notices"]').text()).toContain('read only')
    wrapper.unmount()
  })

  it('archives from the menu while promotion is still the primary action', async () => {
    state = bundle({ ...SEALED, gpg_key_id: 'k-1' })
    const { wrapper } = await mountPage()
    testid('bundle-more')!.click()
    await flushPromises()
    testid('bundle-action-archive')!.click()
    await flushPromises()
    expect(body().textContent).toContain('Archive this bundle?')
    wrapper.unmount()
  })

  it('deletes a draft from the menu after confirming and returns to the list', async () => {
    const { wrapper, router } = await mountPage()
    testid('bundle-more')!.click()
    await flushPromises()
    testid('bundle-action-delete')!.click()
    await flushPromises()
    expect(sent().some((r) => r.startsWith('DELETE /api/v1/bundles/shop'))).toBe(false)
    const confirm = [...body().querySelectorAll('[role="dialog"] button')].find((b) => b.textContent?.trim() === 'Delete') as HTMLButtonElement
    confirm.click()
    await flushPromises()
    expect(sent()).toContain('DELETE /api/v1/bundles/shop/1.0.0')
    expect(router.currentRoute.value.fullPath).toBe('/bundles')
    wrapper.unmount()
  })

  it('removes an item at once and offers to put it back', async () => {
    const { wrapper } = await mountPage()
    await wrapper.get('[data-testid="action-remove"]').trigger('click')
    await flushPromises()
    expect(sent()).toContain(`DELETE /api/v1/bundles/shop/1.0.0/items/${SHA}`)
    const toast = useToastStore().queue.find((x) => x.title === 'Item removed')!
    expect(toast.action?.label).toBe('Undo')

    toast.action!.run()
    await flushPromises()
    expect(bodyOf('/items')).toEqual({
      items: [{ source_repo_id: 'r1', sha256: SHA, artifact_path: 'com/acme/shop-1.jar', format: 'maven', size_bytes: 2048 }],
    })
    wrapper.unmount()
  })

  it('adds items picked from a search, skipping what is already in the bundle or cannot be pinned', async () => {
    const { wrapper } = await mountPage()
    testid('add-items')!.click()
    await flushPromises()
    setValue('#bundle-item-search', 'lib')
    body().querySelector('#bundle-item-search')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()

    expect(sent().some((r) => r.startsWith('GET /api/v1/search?q=lib'))).toBe(true)
    const rows = [...body().querySelectorAll('[data-testid="item-candidates"] li')]
    // The first lib hit, and the hit without a checksum (disabled). The duplicate digest and the bundled file are dropped.
    expect(rows).toHaveLength(2)
    expect(rows[0]!.textContent).toContain('com/acme/lib-2.jar')
    const boxes = rows.map((r) => r.querySelector('input') as HTMLInputElement)
    expect(boxes[1]!.disabled).toBe(true)
    expect(rows[1]!.textContent).toContain('No checksum recorded')

    boxes[0]!.click()
    await flushPromises()
    buttonByText('Add selected (1)')!.click()
    await flushPromises()
    expect(bodyOf('/items')).toEqual({
      items: [{ source_repo_id: 'r1', sha256: SHA2, artifact_path: 'com/acme/lib-2.jar', format: 'maven', size_bytes: 512 }],
    })
    wrapper.unmount()
  })

  it('shows the source repositories scans and the critical count on the security tab', async () => {
    const { wrapper } = await mountPage('/bundles/shop/1.0.0?tab=security')
    expect(sent()).toContain('GET /api/v1/vuln/scans/summary?repo=maven-dev')
    expect(wrapper.text()).toContain('1 critical')
    expect(wrapper.find('a[href="/security?type=vuln&repo=maven-dev"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('lists the promotion history', async () => {
    state = bundle({ ...SEALED, current_stage: 'qa', gpg_key_id: 'k-1' })
    const { wrapper } = await mountPage('/bundles/shop/1.0.0?tab=promotions')
    const row = wrapper.findAll('tbody tr').find((r) => r.text().includes('ship it'))!
    expect(row.text()).toContain('qa')
    expect(row.text()).toContain('Succeeded')
    expect(row.text()).toContain('ann')
    wrapper.unmount()
  })

  it('offers the manifest, and the signature only for a signed bundle', async () => {
    state = bundle({ ...SEALED, gpg_key_id: 'ABCD', gpg_signature: '-----BEGIN PGP SIGNATURE-----' })
    let { wrapper } = await mountPage('/bundles/shop/1.0.0?tab=verification')
    expect(testid('download-manifest')).not.toBeNull()
    expect(testid('download-signature')).not.toBeNull()
    expect(wrapper.get('[data-testid="verification"]').text()).toContain('ABCD')
    wrapper.unmount()

    document.body.innerHTML = ''
    fetchSpy.mockRestore()
    state = bundle({ ...SEALED, gpg_key_id: null, gpg_signature: null })
    ;({ wrapper } = await mountPage('/bundles/shop/1.0.0?tab=verification'))
    expect(testid('download-manifest')).not.toBeNull()
    expect(testid('download-signature')).toBeNull()
    expect(wrapper.get('[data-testid="verification"]').text()).toContain('Not signed')
    expect(wrapper.get('[data-testid="bundle-notices"]').text()).toContain('without a signature')
    wrapper.unmount()
  })

  it('says so when the bundle does not exist', async () => {
    getStatus = 404
    const { wrapper } = await mountPage()
    expect(wrapper.text()).toContain('Bundle not found')
    expect(wrapper.find('a[href="/bundles"]').exists()).toBe(true)
    expect(primaries()).toEqual([])
    wrapper.unmount()
  })

  it('offers a retry when the bundle cannot be loaded', async () => {
    getStatus = 500
    const { wrapper } = await mountPage()
    expect(wrapper.find('[role="alert"], [data-variant="error"]').exists() || wrapper.text().includes('Retry')).toBe(true)
    wrapper.unmount()
  })
})

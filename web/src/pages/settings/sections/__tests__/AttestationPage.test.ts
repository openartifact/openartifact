// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import AttestationPage from '../AttestationPage.vue'
import SettingsSectionPage from '../../SettingsSectionPage.vue'
import { useSessionStore, type User } from '../../../../stores/session'

const KEY = {
  key_id: 'abc123',
  algorithm: 'ecdsa-p256',
  source: 'generated',
  public_key_pem: '-----BEGIN PUBLIC KEY-----\nMFk\n-----END PUBLIC KEY-----',
  public_key_url: '/api/v1/attestation/key.pem',
}

interface Call { method: string; path: string; body?: string }
let spy: Mock<typeof fetch> | undefined
let calls: Call[] = []

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** Answers each request from `routes` ("METHOD /path"); anything else is a 404. */
function serve(routes: Record<string, () => Response>): void {
  calls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const method = init?.method ?? 'GET'
    calls.push({ method, path: url.pathname + url.search, body: init?.body as string | undefined })
    return (routes[`${method} ${url.pathname}`] ?? (() => json({ error: 'not found' }, 404)))()
  }) as never)
}

const repos = (names: string[]) => () => json({ repositories: names.map((name) => ({ name })) })

function router() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/repos/:repo', name: 'repo-detail', component: { template: '<div />' } },
      { path: '/:rest(.*)*', component: { template: '<div />' } },
    ],
  })
}

async function mountPage() {
  const r = router()
  await r.push('/settings/attestation')
  const w = mount(AttestationPage, { attachTo: document.body, global: { plugins: [r] } })
  await flushPromises()
  return w
}

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('AttestationPage', () => {
  it('shows a loading state before the key arrives', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation((() => new Promise(() => {})) as never)
    const r = router()
    await r.push('/settings/attestation')
    const w = mount(AttestationPage, { global: { plugins: [r] } })
    expect(w.find('[role="status"]').text()).toContain('Loading')
    w.unmount()
  })

  it('shows the key, its source and the verify commands against the key URL', async () => {
    serve({ 'GET /api/v1/admin/attestation': () => json(KEY), 'GET /api/v1/repositories': repos([]) })
    const w = await mountPage()
    expect(w.get('[data-testid="key-id"]').text()).toBe('abc123')
    expect(w.get('[data-testid="key-pem"]').text()).toContain('BEGIN PUBLIC KEY')
    expect(w.get('[data-testid="key-source"]').text()).toContain('Generated')
    expect(w.get('[data-testid="key-url"]').text()).toBe(`${window.location.origin}/api/v1/attestation/key.pem`)
    expect(w.get('[data-testid="verify-snippet"]').text()).toContain(`curl -sS ${window.location.origin}/api/v1/attestation/key.pem`)
    w.unmount()
  })

  it('explains that attestations are off on a 501', async () => {
    serve({ 'GET /api/v1/admin/attestation': () => json({ error: 'not implemented', code: 'not_implemented' }, 501) })
    const w = await mountPage()
    expect(w.get('[data-testid="attestation-off"]').text()).toContain('Attestations are not enabled')
    expect(w.find('[data-testid="attestation-key"]').exists()).toBe(false)
    w.unmount()
  })

  it('asks for the secret key on a 409 secret_key_required', async () => {
    serve({ 'GET /api/v1/admin/attestation': () => json({ error: 'need secret', code: 'secret_key_required' }, 409) })
    const w = await mountPage()
    const block = w.get('[data-testid="attestation-secret-required"]')
    expect(block.text()).toContain('A secret key is needed')
    expect(block.text()).toContain('OPENARTIFACT_SECRET_KEY=<random secret>')
    w.unmount()
  })

  it('shows the server message and a retry on another failure', async () => {
    serve({ 'GET /api/v1/admin/attestation': () => json({ error: 'database down' }, 500) })
    const w = await mountPage()
    const block = w.get('[data-testid="attestation-error"]')
    expect(block.text()).toContain('database down')
    expect(block.text()).toContain('Retry')
    w.unmount()
  })

  it('lists docker repositories with their state and sends the toggle as a PUT', async () => {
    let enabled = false
    serve({
      'GET /api/v1/admin/attestation': () => json(KEY),
      'GET /api/v1/repositories': repos(['images']),
      'GET /api/v1/repositories/images/attestation-attach': () => json({ enabled }),
      'PUT /api/v1/repositories/images/attestation-attach': () => {
        enabled = true
        return json({ enabled })
      },
    })
    const w = await mountPage()
    expect(w.find('tbody tr').text()).toContain('images')
    expect(w.find('tbody tr').text()).toContain('Off')
    await w.get('[role="switch"], input[type="checkbox"]').trigger('click')
    await flushPromises()
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.path).toBe('/api/v1/repositories/images/attestation-attach')
    expect(JSON.parse(put!.body!)).toEqual({ enabled: true })
    expect(w.find('tbody tr').text()).toContain('Attaching')
    w.unmount()
  })

  it('shows an empty state when there is no hosted docker repository', async () => {
    serve({ 'GET /api/v1/admin/attestation': () => json(KEY), 'GET /api/v1/repositories': repos([]) })
    const w = await mountPage()
    expect(w.text()).toContain('No hosted docker repositories')
    w.unmount()
  })

  it('reports a failed repository list without hiding the key', async () => {
    serve({ 'GET /api/v1/admin/attestation': () => json(KEY), 'GET /api/v1/repositories': () => json({ error: 'list broke' }, 500) })
    const w = await mountPage()
    expect(w.get('[data-testid="attach-error"]').text()).toContain('list broke')
    expect(w.find('[data-testid="attestation-key"]').exists()).toBe(true)
    w.unmount()
  })

  it('is refused for a non-administrator', async () => {
    serve({})
    setActivePinia(createPinia())
    useSessionStore().user = { id: 'u', username: 'dev', is_admin: false } as User
    const r = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/settings', component: { render: () => null } },
        { path: '/settings/:section', name: 'settings-section', component: SettingsSectionPage },
      ],
    })
    await r.push('/settings/attestation')
    const w = mount(SettingsSectionPage, { attachTo: document.body, global: { plugins: [r] } })
    await flushPromises()
    expect(w.find('[data-testid="section-forbidden"]').exists()).toBe(true)
    expect(calls.some((c) => c.path.includes('/admin/attestation'))).toBe(false)
    w.unmount()
  })
})

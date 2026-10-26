// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import TokensSection from '../TokensSection.vue'
import { useSessionStore } from '../../../../stores/session'
import { useToastStore } from '../../../../stores/toast'

const FUTURE = new Date(Date.now() + 40 * 86_400_000).toISOString()

const TOKENS = [
  { id: 't1', type: 'personal', name: 'laptop', owner: 'alice', created_at: '2026-01-01T00:00:00Z', expires_at: FUTURE, expired: false },
  { id: 't2', type: 'project', name: 'ci', owner: 'bob', project: 'team', role: 'developer', created_at: '2026-01-01T00:00:00Z', expired: false },
  { id: 't3', type: 'personal', name: 'bobs', owner: 'bob', created_at: '2026-01-01T00:00:00Z', expired: false },
]

interface Call {
  method: string
  url: URL
}

let spy: Mock<typeof fetch> | undefined
let calls: Call[] = []

function serve(): void {
  calls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const method = init?.method ?? 'GET'
    calls.push({ method, url })
    const json = (body: unknown): Response => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (method === 'DELETE') return new Response(null, { status: 204 })
    if (url.pathname === '/api/v1/tokens') return json({ tokens: TOKENS, total: 3, page: 1, limit: 25 })
    return json({ items: [], projects: [], repositories: [], total: 0 })
  }) as never)
}

async function mountPage(path = '/settings/tokens') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const w = mount(TokensSection, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return Object.assign(w, { router })
}

const listCalls = () => calls.filter((c) => c.url.pathname === '/api/v1/tokens')

beforeEach(() => {
  setActivePinia(createPinia())
  useSessionStore().user = { id: 'u1', username: 'alice' } as never
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('TokensSection', () => {
  it('lists every type in one table', async () => {
    serve()
    const w = await mountPage()
    const rows = w.findAll('tbody tr').map((r) => r.text())
    expect(rows).toHaveLength(3)
    expect(rows[1]).toContain('ci')
    expect(rows[1]).toContain('team')
    w.unmount()
  })

  it('sends the chosen type and keeps it in the URL', async () => {
    serve()
    const w = await mountPage()
    await w.findAll('[data-testid="type-filter"] button').find((b) => b.text() === 'Project')!.trigger('click')
    await flushPromises()
    expect(w.router.currentRoute.value.query.type).toBe('project')
    expect(listCalls().at(-1)!.url.searchParams.get('type')).toBe('project')
    w.unmount()
  })

  it('opens on the personal tokens from the profile link', async () => {
    serve()
    const w = await mountPage('/settings/tokens?type=personal')
    expect(listCalls()[0].url.searchParams.get('type')).toBe('personal')
    w.unmount()
  })

  it('asks for tokens expiring soon and says so, with a way back', async () => {
    serve()
    const w = await mountPage()
    await w.find('[data-testid="filter-expiring-7"]').trigger('click')
    await flushPromises()
    expect(listCalls().at(-1)!.url.searchParams.get('expires_before')).toBeTruthy()
    expect(w.find('[data-testid="token-notice"]').text()).toContain('within 7 days')

    await w.find('[data-testid="token-notice"] button').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="token-notice"]').exists()).toBe(false)
    w.unmount()
  })

  it('revokes through the endpoint of the token type after confirmation', async () => {
    serve()
    const w = await mountPage()
    await w.find('[data-testid="revoke-t1"]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)

    const confirm = [...document.body.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent?.trim() === 'Revoke')!
    confirm.click()
    await flushPromises()
    const del = calls.filter((c) => c.method === 'DELETE')
    expect(del.map((c) => c.url.pathname)).toEqual(['/api/v1/auth/tokens/t1'])
    expect(useToastStore().queue.some((x) => x.level === 'success' && x.title.includes('laptop'))).toBe(true)
    w.unmount()
  })

  it("offers no revoke on someone else's personal token for a non-admin", async () => {
    serve()
    const w = await mountPage()
    expect(w.find('[data-testid="revoke-t1"]').exists()).toBe(true)
    expect(w.find('[data-testid="revoke-t3"]').exists()).toBe(false)
    w.unmount()
  })

  it('opens the create drawer from ?new=1', async () => {
    serve()
    const w = await mountPage('/settings/tokens?new=1')
    expect(document.body.textContent).toContain('Create a token')
    w.unmount()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ServiceAccountsPage from '../ServiceAccountsPage.vue'

const ACCOUNTS = [
  { id: 'sa1', name: 'ci-bot', description: 'Publishes', disabled: false, created_at: '2026-01-01T00:00:00Z' },
  { id: 'sa2', name: 'legacy', description: '', disabled: true, created_at: '2026-01-02T00:00:00Z' },
]

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let fetchSpy: Mock<typeof fetch>
const writes = () => fetchSpy.mock.calls.filter(([, init]) => init?.method && init.method !== 'GET').map(([url, init]) => ({
  url: String(url),
  method: init!.method,
  body: init!.body ? JSON.parse(String(init!.body)) : undefined,
}))

beforeEach(() => {
  setActivePinia(createPinia())
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'DELETE') return new Response(null, { status: 204 })
    if (method === 'POST' && url === '/api/v1/service-accounts') return json({ id: 'sa3', name: 'new-bot' }, 201)
    if (method !== 'GET') return json(ACCOUNTS[0])
    if (url.startsWith('/api/v1/oidc-trust-policies')) return json({ data: [] })
    return json({ data: ACCOUNTS, total: 2, page: 1, per_page: 200 })
  }) as never)
})
afterEach(() => {
  fetchSpy.mockRestore()
  document.body.innerHTML = ''
})

async function mountPage(path = '/settings/service-accounts') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const wrapper = mount(ServiceAccountsPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return Object.assign(wrapper, { router })
}

describe('ServiceAccountsPage', () => {
  it('lists the accounts with their status and links each to its page', async () => {
    const w = await mountPage()
    const rows = w.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.find('a').attributes('href')).toBe('/settings/service-accounts/sa1')
    expect(rows[0]!.text()).toContain('Active')
    expect(rows[1]!.text()).toContain('Disabled')
    w.unmount()
  })

  it('filters by status through the URL', async () => {
    const w = await mountPage()
    await w.find('select').setValue('disabled')
    await flushPromises()
    expect(w.router.currentRoute.value.query.status).toBe('disabled')
    expect(w.findAll('tbody tr')).toHaveLength(1)
    expect(w.find('tbody tr').text()).toContain('legacy')
    w.unmount()
  })

  it('Disable PATCHes disabled=true without deleting', async () => {
    const w = await mountPage()
    await w.find('button[aria-label="Disable: ci-bot"]').trigger('click')
    await flushPromises()
    expect(writes()).toEqual([{ url: '/api/v1/service-accounts/sa1', method: 'PATCH', body: { disabled: true } }])
    w.unmount()
  })

  it('Delete asks first, then DELETEs', async () => {
    const w = await mountPage()
    await w.find('button[aria-label="Delete: ci-bot"]').trigger('click')
    await flushPromises()
    expect(writes()).toEqual([])
    const confirm = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Delete' && !b.hasAttribute('aria-label'))!
    confirm.click()
    await flushPromises()
    expect(writes()).toEqual([{ url: '/api/v1/service-accounts/sa1', method: 'DELETE', body: undefined }])
    w.unmount()
  })

  it('opens the create drawer from ?new=1, creates, and goes to the new account', async () => {
    const w = await mountPage('/settings/service-accounts?new=1')
    const name = document.querySelector<HTMLInputElement>('input[type=text]')!
    name.value = 'new-bot'
    name.dispatchEvent(new Event('input'))
    await flushPromises()
    document.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await flushPromises()
    expect(writes()[0]).toMatchObject({ url: '/api/v1/service-accounts', method: 'POST', body: { name: 'new-bot' } })
    expect(w.router.currentRoute.value.path).toBe('/settings/service-accounts/sa3')
    w.unmount()
  })

  it('shows the policies tab with its own empty state', async () => {
    const w = await mountPage('/settings/service-accounts?tab=policies')
    await flushPromises()
    expect(document.body.textContent).toContain('No trust policies yet')
    w.unmount()
  })
})

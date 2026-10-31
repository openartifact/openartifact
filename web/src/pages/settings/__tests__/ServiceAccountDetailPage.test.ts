// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ServiceAccountDetailPage from '../ServiceAccountDetailPage.vue'

const ACCOUNT = { id: 'sa1', name: 'ci-bot', description: 'Publishes', disabled: false, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let fetchSpy: Mock<typeof fetch>
let missing = false
const writes = () => fetchSpy.mock.calls.filter(([, init]) => init?.method && init.method !== 'GET').map(([url, init]) => ({
  url: String(url),
  method: init!.method,
  body: init!.body ? JSON.parse(String(init!.body)) : undefined,
}))

beforeEach(() => {
  missing = false
  setActivePinia(createPinia())
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'DELETE') return new Response(null, { status: 204 })
    if (method === 'PATCH') return json({ ...ACCOUNT, ...JSON.parse(String(init!.body)) })
    if (url === '/api/v1/service-accounts/sa1') return missing ? json({ error: 'not found' }, 404) : json(ACCOUNT)
    if (url.startsWith('/api/v1/users/sa1/tokens')) return json({ data: [{ id: 't1', name: 'deploy-key', kind: 'deploy', created_at: '2026-01-01T00:00:00Z' }] })
    if (url.startsWith('/api/v1/users/sa1/groups')) return json({ data: [{ id: 'g1', name: 'publishers' }], total: 1 })
    if (url.startsWith('/api/v1/groups')) return json({ data: [{ id: 'g1', name: 'publishers' }, { id: 'g2', name: 'readers' }], total: 2 })
    if (url.startsWith('/api/v1/permission-targets/p1/assignments')) return json({ data: [{ principal_type: 'user', principal_id: 'sa1', principal_name: 'ci-bot' }] })
    if (url.startsWith('/api/v1/permission-targets/p2/assignments')) return json({ data: [{ principal_type: 'user', principal_id: 'other' }] })
    if (url.startsWith('/api/v1/permission-targets')) return json({ data: [{ id: 'p1', name: 'releases' }, { id: 'p2', name: 'readonly' }], total: 2 })
    if (url.startsWith('/api/v1/projects/shop/members')) return json({ members: [{ user_id: 'sa1', username: 'ci-bot', role: 'developer' }] })
    if (url.startsWith('/api/v1/projects/')) return json({ members: [] })
    if (url.startsWith('/api/v1/projects')) return json({ projects: [{ name: 'shop' }, { name: 'tools' }] })
    if (url.startsWith('/api/v1/oidc-trust-policies')) return json({ data: [] })
    return json({})
  }) as never)
})
afterEach(() => {
  fetchSpy.mockRestore()
  document.body.innerHTML = ''
})

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/settings/service-accounts/:id', component: { template: '<div />' } },
      { path: '/:rest(.*)*', component: { template: '<div />' } },
    ],
  })
  await router.push('/settings/service-accounts/sa1')
  const wrapper = mount(ServiceAccountDetailPage, {
    attachTo: document.body,
    global: { plugins: [router] },
  })
  await flushPromises()
  return Object.assign(wrapper, { router })
}

describe('ServiceAccountDetailPage', () => {
  it('shows the account with its tokens, roles, groups, projects and policies', async () => {
    const w = await mountPage()
    expect(w.text()).toContain('ci-bot')
    expect(w.text()).toContain('deploy-key')
    expect(w.find('[data-testid=access-row-p1]').text()).toContain('releases')
    expect(w.find('[data-testid=access-row-p2]').exists()).toBe(false)
    expect(w.find('[data-testid=access-row-g1]').text()).toContain('publishers')
    expect(w.find('[data-testid=access-row-shop]').text()).toContain('shop')
    expect(w.text()).toContain('No trust policies yet')
    w.unmount()
  })

  it('saves the description with PATCH', async () => {
    const w = await mountPage()
    await w.find('#sa-detail-description').setValue('Publishes releases')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(writes()).toEqual([{ url: '/api/v1/service-accounts/sa1', method: 'PATCH', body: { description: 'Publishes releases' } }])
    w.unmount()
  })

  it('disables the account and says so', async () => {
    const w = await mountPage()
    await w.find('[data-testid=account-toggle]').trigger('click')
    await flushPromises()
    expect(writes()).toEqual([{ url: '/api/v1/service-accounts/sa1', method: 'PATCH', body: { disabled: true } }])
    expect(w.find('[data-testid=account-disabled-note]').exists()).toBe(true)
    w.unmount()
  })

  it('deletes only after confirming, then returns to the list', async () => {
    const w = await mountPage()
    await w.find('[data-testid=account-delete]').trigger('click')
    await flushPromises()
    expect(writes()).toEqual([])
    const confirm = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Delete' && b.dataset.testid !== 'account-delete')!
    confirm.click()
    await flushPromises()
    expect(writes()).toEqual([{ url: '/api/v1/service-accounts/sa1', method: 'DELETE', body: undefined }])
    expect(w.router.currentRoute.value.path).toBe('/settings/service-accounts')
    w.unmount()
  })

  it('says so when the account does not exist', async () => {
    missing = true
    const w = await mountPage()
    expect(w.text()).toContain('Service account not found')
    expect(w.find('[data-testid=account-toggle]').exists()).toBe(false)
    w.unmount()
  })
})

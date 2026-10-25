// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import UsersPage from '../UsersPage.vue'

const USERS = [
  { id: 'u1', username: 'alice', email: 'alice@example.com', disabled: false, created_at: '2026-01-01T00:00:00Z' },
  { id: 'u2', username: 'bob', email: 'bob@example.com', disabled: true, created_at: '2026-01-02T00:00:00Z' },
]

interface Call {
  method: string
  url: string
  body?: unknown
}

function mockApi(): Call[] {
  const calls: Call[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET') {
      return new Response(JSON.stringify({ data: USERS, total: USERS.length, page: 1, per_page: 200 }), { status: 200 })
    }
    calls.push({ method, url: String(input), body: init?.body ? JSON.parse(String(init.body)) : undefined })
    if (method === 'DELETE') return new Response(null, { status: 204 })
    return new Response(JSON.stringify(USERS[0]), { status: 200 })
  }) as never)
  return calls
}

const mountPage = async (path = '/settings/users') => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const wrapper = mount(UsersPage, { attachTo: document.body, global: { plugins: [router] } })
  return Object.assign(wrapper, { router })
}

describe('UsersPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.body.innerHTML = ''
  })

  it('shows a Disabled badge for disabled users and filters by status', async () => {
    mockApi()
    const wrapper = await mountPage()
    await flushPromises()

    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    expect(wrapper.findAll('tbody tr')[1]!.text()).toContain('Disabled')

    await wrapper.find('select').setValue('disabled')
    await flushPromises()
    expect(wrapper.router.currentRoute.value.query.status).toBe('disabled')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.find('tbody tr').text()).toContain('bob')

    await wrapper.find('select').setValue('active')
    await flushPromises()
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.find('tbody tr').text()).toContain('alice')
    wrapper.unmount()
  })

  it('Disable PATCHes disabled=true and never deletes', async () => {
    const calls = mockApi()
    const wrapper = await mountPage()
    await flushPromises()

    await wrapper.find('button[aria-label="Disable alice"]').trigger('click')
    await flushPromises()

    expect(calls).toEqual([{ method: 'PATCH', url: '/api/v1/users/u1', body: { disabled: true } }])
    wrapper.unmount()
  })

  it('Enable PATCHes disabled=false on a disabled user', async () => {
    const calls = mockApi()
    const wrapper = await mountPage()
    await flushPromises()

    await wrapper.find('button[aria-label="Enable bob"]').trigger('click')
    await flushPromises()

    expect(calls).toEqual([{ method: 'PATCH', url: '/api/v1/users/u2', body: { disabled: false } }])
    wrapper.unmount()
  })

  it('Delete asks for a destructive confirmation before calling DELETE, also for disabled users', async () => {
    const calls = mockApi()
    const wrapper = await mountPage()
    await flushPromises()

    const del = wrapper.find('button[aria-label="Delete user: bob"]')
    expect(del.attributes('disabled')).toBeUndefined()
    await del.trigger('click')
    await flushPromises()

    expect(calls).toHaveLength(0)
    const dialog = document.body.textContent ?? ''
    expect(dialog).toContain('API tokens, sessions, project memberships, group links and SSO link are removed')

    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Delete')
    confirm!.click()
    await flushPromises()

    expect(calls).toEqual([{ method: 'DELETE', url: '/api/v1/users/u2', body: undefined }])
    wrapper.unmount()
  })

  it('restores the search from ?q= (palette links) and opens the create drawer from ?new=1', async () => {
    mockApi()
    const wrapper = await mountPage('/settings/users?q=bob&new=1')
    await flushPromises()
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.find('tbody tr').text()).toContain('bob')
    expect((wrapper.find('input[type="search"]').element as HTMLInputElement).value).toBe('bob')
    expect(document.body.querySelector('#user-form')).not.toBeNull()
    wrapper.unmount()
  })

  it('counts users from the whole list, not the filtered page', async () => {
    mockApi()
    const wrapper = await mountPage('/settings/users?status=disabled')
    await flushPromises()
    expect(wrapper.get('[data-testid="user-counts"]').text()).toBe('2 users, 1 active, 1 disabled')
    wrapper.unmount()
  })
})

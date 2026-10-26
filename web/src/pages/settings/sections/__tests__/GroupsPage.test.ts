// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import GroupsPage from '../GroupsPage.vue'
import { useToastStore } from '../../../../stores/toast'

interface Call {
  method: string
  url: string
  body?: unknown
}

interface Server {
  calls: Call[]
  groups: { id: string; name: string; created_at: string }[]
  members: Record<string, string[]>
  /** Fails the next mutating request whose URL matches with this status and message. */
  failNext?: { match: RegExp; status: number; error: string }
}

const USERS = [
  { id: 'u1', username: 'alice', full_name: 'Alice Liddell', disabled: false },
  { id: 'u2', username: 'bob', disabled: false },
  { id: 'u3', username: 'bobby-disabled', disabled: true },
]

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

function startServer(init: Partial<Pick<Server, 'groups' | 'members'>> = {}): Server {
  const server: Server = {
    calls: [],
    groups: init.groups ?? [
      { id: 'g1', name: 'devs', created_at: '2026-01-02T00:00:00Z' },
      { id: 'g2', name: 'admins', created_at: '2026-01-01T00:00:00Z' },
    ],
    members: init.members ?? { g1: ['u1'], g2: [] },
  }
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (method !== 'GET') {
      server.calls.push({ method, url, body })
      const fail = server.failNext
      if (fail && fail.match.test(url)) {
        server.failNext = undefined
        return json({ error: fail.error }, fail.status)
      }
    }
    const members = /^\/api\/v1\/groups\/([^/]+)\/members(?:\/([^/]+))?$/.exec(url)
    if (members) {
      const [, gid, uid] = members
      const list = (server.members[gid!] ??= [])
      if (method === 'POST') list.push(uid!)
      else if (method === 'DELETE') server.members[gid!] = list.filter((m) => m !== uid)
      else return json({ data: list.map((id) => USERS.find((u) => u.id === id)) })
      return new Response(null, { status: 204 })
    }
    if (url === '/api/v1/groups') {
      if (method === 'POST') {
        const g = { id: `g${server.groups.length + 1}`, name: (body as { name: string }).name, created_at: '2026-02-01T00:00:00Z' }
        server.groups.push(g)
        return json(g, 201)
      }
      return json({ data: server.groups })
    }
    const del = /^\/api\/v1\/groups\/([^/]+)$/.exec(url)
    if (del && method === 'DELETE') {
      server.groups = server.groups.filter((g) => g.id !== del[1])
      return new Response(null, { status: 204 })
    }
    if (url.startsWith('/api/v1/users')) return json({ data: USERS, total: USERS.length })
    return json({ error: `unexpected ${method} ${url}` }, 500)
  }) as never)
  return server
}

async function mountPage(path = '/settings/groups') {
  const pinia = createPinia()
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const wrapper = mount(GroupsPage, { attachTo: document.body, global: { plugins: [pinia, router] } })
  await flushPromises()
  return Object.assign(wrapper, { router, toasts: useToastStore(pinia) })
}

const byId = (id: string) => document.getElementById(id) as HTMLInputElement
const rowNames = (w: { findAll: (s: string) => { text: () => string }[] }) => w.findAll('tbody tr').map((r) => r.text())
const buttonByLabel = (label: string) => document.querySelector(`button[aria-label="${label}"]`) as HTMLButtonElement

async function typeInto(el: HTMLInputElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('input'))
  await flushPromises()
}

// The name filter is debounced: poll until the table reflects it instead of sleeping.
async function waitFor(cond: () => boolean, timeoutMs = 3000) {
  const deadline = Date.now() + timeoutMs
  while (!cond() && Date.now() < deadline) {
    await new Promise((r) => queueMicrotask(() => r(undefined)))
    await flushPromises()
  }
  expect(cond()).toBe(true)
}

async function submitCreate(name: string) {
  await typeInto(byId('group-name'), name)
  ;(document.getElementById('group-form') as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }))
  await flushPromises()
}

describe('GroupsPage list', () => {
  it('shows the empty state and no counts when there are no groups', async () => {
    startServer({ groups: [], members: {} })
    const wrapper = await mountPage()

    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
    expect(wrapper.find('[data-testid="group-counts"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('lists groups sorted by name with their member counts and total memberships', async () => {
    startServer({ members: { g1: ['u1', 'u2'], g2: [] } })
    const wrapper = await mountPage()

    const rows = rowNames(wrapper)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toContain('admins')
    expect(rows[1]).toContain('devs')
    // The members column is the count of the group's own membership list.
    expect(wrapper.find('button[aria-label$=": admins"]').text()).toBe('0')
    expect(wrapper.findAll('tbody tr')[1]!.find('button').text()).toBe('2')
    expect(wrapper.find('[data-testid="group-counts"]').text()).toContain('2')
    wrapper.unmount()
  })

  it('filters by name case-insensitively', async () => {
    startServer()
    const wrapper = await mountPage()

    const input = wrapper.find('input[type=search]')
    await input.setValue('  DEV ')
    await waitFor(() => wrapper.findAll('tbody tr').length === 1)

    const rows = rowNames(wrapper)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toContain('devs')
    wrapper.unmount()
  })
})

describe('GroupsPage create', () => {
  it('opens the drawer from ?new=1 and POSTs the trimmed name, then lists the new group', async () => {
    const server = startServer()
    const wrapper = await mountPage('/settings/groups?new=1')

    await submitCreate('  qa-team  ')

    expect(server.calls).toEqual([{ method: 'POST', url: '/api/v1/groups', body: { name: 'qa-team' } }])
    expect(wrapper.router.currentRoute.value.query.new).toBeUndefined()
    expect(rowNames(wrapper).some((r) => r.includes('qa-team'))).toBe(true)
    expect(wrapper.toasts.visible.some((x) => x.level === 'success')).toBe(true)
    wrapper.unmount()
  })

  it('rejects a blank name without calling the server', async () => {
    const server = startServer()
    const wrapper = await mountPage('/settings/groups?new=1')

    await submitCreate('   ')

    expect(server.calls).toEqual([])
    expect(wrapper.toasts.visible.some((x) => x.level === 'error')).toBe(true)
    // The drawer stays open so the admin can correct the name.
    expect(byId('group-name')).not.toBeNull()
    wrapper.unmount()
  })

  it('surfaces the server error for a duplicate name and keeps the drawer and typed name', async () => {
    const server = startServer()
    server.failNext = { match: /\/groups$/, status: 409, error: 'group "devs" already exists' }
    const wrapper = await mountPage('/settings/groups?new=1')

    await submitCreate('devs')

    const toast = wrapper.toasts.visible.find((x) => x.level === 'error')
    expect(toast?.title).toBe('group "devs" already exists')
    expect(byId('group-name').value).toBe('devs')
    expect(wrapper.router.currentRoute.value.query.new).toBe('1')
    expect(wrapper.toasts.visible.some((x) => x.level === 'success')).toBe(false)
    wrapper.unmount()
  })

  it('clears a previously typed name when the drawer is cancelled and reopened', async () => {
    startServer()
    const wrapper = await mountPage('/settings/groups?new=1')
    await typeInto(byId('group-name'), 'half-typed')

    await wrapper.router.replace({ query: {} })
    await flushPromises()
    await wrapper.router.replace({ query: { new: '1' } })
    await flushPromises()

    expect(byId('group-name').value).toBe('')
    wrapper.unmount()
  })
})

describe('GroupsPage members', () => {
  it('lists current members (full name preferred) and offers only enabled non-members in search', async () => {
    startServer()
    const wrapper = await mountPage()

    await wrapper.find('button[aria-label$=": devs"]').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('Alice Liddell')

    await typeInto(byId('group-member-search'), 'bob')
    const offered = Array.from(document.querySelectorAll('button')).map((b) => b.textContent?.trim())
    expect(offered).toContain('bob')
    // Disabled accounts cannot be added.
    expect(offered).not.toContain('bobby-disabled')

    // An existing member is not offered again.
    await typeInto(byId('group-member-search'), 'alice')
    expect(document.querySelectorAll('.max-h-40 button')).toHaveLength(0)
    wrapper.unmount()
  })

  it('adds a member via the single-member endpoint and updates the count', async () => {
    const server = startServer()
    const wrapper = await mountPage()

    await wrapper.find('button[aria-label$=": admins"]').trigger('click')
    await flushPromises()
    await typeInto(byId('group-member-search'), 'bob')
    const pick = Array.from(document.querySelectorAll('.max-h-40 button')).find((b) => b.textContent?.trim() === 'bob') as HTMLButtonElement
    pick.click()
    await flushPromises()

    expect(server.calls).toEqual([{ method: 'POST', url: '/api/v1/groups/g2/members/u2', body: undefined }])
    expect(byId('group-member-search').value).toBe('')
    expect(buttonByLabel('Remove member: bob')).not.toBeNull()
    expect(wrapper.find('button[aria-label$=": admins"]').text()).toBe('1')
    wrapper.unmount()
  })

  it('removes a member and reports a failed removal without changing the list', async () => {
    const server = startServer()
    const wrapper = await mountPage()
    await wrapper.find('button[aria-label$=": devs"]').trigger('click')
    await flushPromises()

    server.failNext = { match: /members\/u1$/, status: 403, error: 'forbidden' }
    buttonByLabel('Remove member: alice').click()
    await flushPromises()

    expect(wrapper.toasts.visible.find((x) => x.level === 'error')?.title).toBe('forbidden')
    expect(buttonByLabel('Remove member: alice')).not.toBeNull()
    expect(server.members.g1).toEqual(['u1'])

    buttonByLabel('Remove member: alice').click()
    await flushPromises()

    expect(server.calls.at(-1)).toEqual({ method: 'DELETE', url: '/api/v1/groups/g1/members/u1', body: undefined })
    expect(buttonByLabel('Remove member: alice')).toBeNull()
    expect(wrapper.findAll('tbody tr')[1]!.find('button').text()).toBe('0')
    wrapper.unmount()
  })

  it('reports a failed add and leaves the group unchanged', async () => {
    const server = startServer()
    const wrapper = await mountPage()
    await wrapper.find('button[aria-label$=": admins"]').trigger('click')
    await flushPromises()
    server.failNext = { match: /members\/u2$/, status: 404, error: 'user not found' }

    await typeInto(byId('group-member-search'), 'bob')
    ;(Array.from(document.querySelectorAll('.max-h-40 button')).find((b) => b.textContent?.trim() === 'bob') as HTMLButtonElement).click()
    await flushPromises()

    expect(wrapper.toasts.visible.find((x) => x.level === 'error')?.title).toBe('user not found')
    expect(server.members.g2).toEqual([])
    expect(wrapper.find('button[aria-label$=": admins"]').text()).toBe('0')
    wrapper.unmount()
  })
})

describe('GroupsPage delete', () => {
  it('asks for confirmation naming the group and deletes nothing until confirmed', async () => {
    const server = startServer()
    const wrapper = await mountPage()

    await wrapper.find('button[aria-label="Delete: devs"]').trigger('click')
    await flushPromises()

    expect(document.body.textContent).toContain('devs')
    expect(server.calls).toEqual([])
    wrapper.unmount()
  })

  it('DELETEs the chosen group (not its neighbour) and removes it from the list', async () => {
    const server = startServer()
    const wrapper = await mountPage()

    await wrapper.find('button[aria-label="Delete: devs"]').trigger('click')
    await flushPromises()
    const confirm = Array.from(document.querySelectorAll('[role=dialog] button')).find((b) => b.textContent?.trim() === 'Delete') as HTMLButtonElement
    confirm.click()
    await flushPromises()

    expect(server.calls).toEqual([{ method: 'DELETE', url: '/api/v1/groups/g1', body: undefined }])
    const rows = rowNames(wrapper)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toContain('admins')
    wrapper.unmount()
  })

  it('keeps the group listed and shows the server error when deletion is refused', async () => {
    const server = startServer()
    server.failNext = { match: /groups\/g1$/, status: 403, error: 'group is referenced by a permission target' }
    const wrapper = await mountPage()

    await wrapper.find('button[aria-label="Delete: devs"]').trigger('click')
    await flushPromises()
    const confirm = Array.from(document.querySelectorAll('[role=dialog] button')).find((b) => b.textContent?.trim() === 'Delete') as HTMLButtonElement
    confirm.click()
    await flushPromises()

    expect(wrapper.toasts.visible.find((x) => x.level === 'error')?.title).toBe('group is referenced by a permission target')
    expect(rowNames(wrapper)).toHaveLength(2)
    wrapper.unmount()
  })
})

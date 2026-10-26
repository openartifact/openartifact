// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import TokenCreateDrawer from '../TokenCreateDrawer.vue'
import { useSessionStore, type User } from '../../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const PROJECTS = [
  { name: 'web', my_role: 'owner', token_max_ttl_days: 45 },
  { name: 'ops', my_role: 'viewer' },
]

let fetchSpy: Mock<typeof fetch>

function serve(): void {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === 'POST') return json({ token: 'oa_secret', name: 'x' }, 201)
    if (url.startsWith('/api/v1/projects')) return json({ projects: PROJECTS, can_create: true })
    if (url.startsWith('/api/v1/repositories')) {
      return json({ total: 2, repositories: [{ name: 'libs', repo_type: 'hosted', federated: true }, { name: 'img', repo_type: 'hosted' }] })
    }
    if (url.startsWith('/api/v1/permission-targets')) return json({ items: [{ id: 'p1', name: 'ci-read' }], total: 1 })
    return json({})
  }) as never)
}

function login(isAdmin: boolean): void {
  setActivePinia(createPinia())
  useSessionStore().user = { id: 'u', username: 'ada', is_admin: isAdmin } as User
}

async function open(props: Record<string, unknown> = {}) {
  const wrapper = mount(TokenCreateDrawer, { props: { open: true, ...props }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

const $ = <T extends Element>(sel: string): T => document.querySelector<T>(sel)!
async function setValue(sel: string, value: string): Promise<void> {
  const el = $<HTMLInputElement | HTMLSelectElement>(sel)
  el.value = value
  el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input'))
  await flushPromises()
}
const posts = () => fetchSpy.mock.calls.filter(([, init]) => init?.method === 'POST')
const body = (i = 0) => JSON.parse(String(posts()[i]![1]!.body))
const typeIds = () => [...document.querySelectorAll<HTMLInputElement>('[data-testid^=type-]')].map((e) => e.value)

beforeEach(() => serve())
afterEach(() => {
  fetchSpy.mockRestore()
  document.body.innerHTML = ''
})

describe('TokenCreateDrawer', () => {
  it('offers federation to administrators only', async () => {
    login(false)
    const w = await open()
    expect(typeIds()).toEqual(['personal', 'project'])
    w.unmount()
    login(true)
    const w2 = await open()
    expect(typeIds()).toEqual(['personal', 'project', 'federation'])
    w2.unmount()
  })

  it('shows the fields of the chosen type', async () => {
    login(true)
    await open()
    expect(document.querySelector('#token-kind')).not.toBeNull()
    expect(document.querySelector('#token-project')).toBeNull()
    $<HTMLInputElement>('[data-testid=type-project]').click()
    await flushPromises()
    expect(document.querySelector('#token-kind')).toBeNull()
    expect(document.querySelector('#token-project')).not.toBeNull()
    expect(document.querySelector('#token-role')).not.toBeNull()
    $<HTMLInputElement>('[data-testid=type-federation]').click()
    await flushPromises()
    expect(document.querySelector('#token-repo')).not.toBeNull()
    expect(document.querySelector('#token-expiry')).toBeNull()
    // only federated hosted repositories
    expect([...document.querySelectorAll('#token-repo option')].map((o) => o.textContent)).toEqual(['libs'])
  })

  it('creates a personal token with scopes and an expiry', async () => {
    login(true)
    const w = await open()
    await setValue('#token-name', 'laptop')
    $<HTMLInputElement>('[data-testid=token-scopes] input').click()
    await flushPromises()
    $<HTMLFormElement>('#token-create-form').dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(posts()[0]![0]).toBe('/api/v1/auth/tokens')
    const b = body()
    expect(b).toMatchObject({ name: 'laptop', kind: 'access', scopes: ['p1'] })
    expect(new Date(b.expires_at).getTime()).toBeGreaterThan(Date.now())
    expect(w.emitted('created')![0]).toEqual([{ secret: 'oa_secret', name: 'laptop', type: 'personal' }])
  })

  it('preselects the deep-linked project and caps the lifetime and role', async () => {
    login(false)
    const w = await open({ initialType: 'project', initialProject: 'ops' })
    expect($<HTMLSelectElement>('#token-project').value).toBe('ops')
    expect([...document.querySelectorAll('#token-role option')].map((o) => (o as HTMLOptionElement).value)).toEqual(['viewer'])
    await setValue('#token-project', 'web')
    expect([...document.querySelectorAll('#token-expiry option')].map((o) => (o as HTMLOptionElement).value)).toEqual(['30', '45'])
    expect([...document.querySelectorAll('#token-role option')].map((o) => (o as HTMLOptionElement).value)).toEqual(['viewer', 'developer'])
    await setValue('#token-role', 'developer')
    await setValue('#token-name', 'deploy')
    $<HTMLFormElement>('#token-create-form').dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(posts()[0]![0]).toBe('/api/v1/projects/web/tokens')
    expect(body()).toMatchObject({ name: 'deploy', role: 'developer' })
    w.unmount()
  })

  it('creates a federation token for a repository, without expiry', async () => {
    login(true)
    await open({ initialType: 'federation' })
    await setValue('#token-name', 'peer-1')
    $<HTMLFormElement>('#token-create-form').dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(posts()[0]![0]).toBe('/api/v1/repositories/libs/federation/tokens')
    expect(body()).toEqual({ name: 'peer-1' })
  })

  it('ignores a deep-linked type the caller cannot create', async () => {
    login(false)
    await open({ initialType: 'federation' })
    expect($<HTMLInputElement>('[data-testid=type-personal]').checked).toBe(true)
  })

  it('blocks submission without a valid name', async () => {
    login(false)
    await open()
    $<HTMLFormElement>('#token-create-form').dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(posts()).toHaveLength(0)
    expect(document.body.textContent).toContain('Enter a name.')
  })
})

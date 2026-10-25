// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ProjectListPage from '../ProjectListPage.vue'
import { useSessionStore, type User } from '../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const MINE = {
  projects: [
    { id: '1', name: 'alpha', display_name: 'Alpha Team', my_role: 'owner', repository_count: 3, member_count: 4, usage: { size_bytes: 2147483648 }, quota: { limit_bytes: 4294967296 } },
    { id: '2', name: 'beta', display_name: 'beta', my_role: 'viewer', repository_count: 0, member_count: 1, usage: { size_bytes: 0 } },
  ],
  can_create: false,
}

let fetchSpy: Mock<typeof fetch>
// The shared system info (docs base behind the empty-state link) is not part of what these tests assert.
const requests = () =>
  fetchSpy.mock.calls
    .map((c) => `${(c[1] as RequestInit | undefined)?.method ?? 'GET'} ${String(c[0])}`)
    .filter((r) => r !== 'GET /api/v1/system/info')

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/projects', component: ProjectListPage },
      { path: '/projects/:project', component: { template: '<div id="detail" />' } },
    ],
  })
}

async function mountPage(list: unknown = MINE, user: Partial<User> = { id: 'u', username: 'ann' }, path = '/projects') {
  setActivePinia(createPinia())
  const session = useSessionStore()
  session.user = user as User
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const method = init?.method ?? 'GET'
    if (method === 'POST') return json({ name: 'new-one' }, 201)
    return json(list)
  })
  const router = makeRouter()
  await router.push(path)
  await router.isReady()
  const wrapper = mount(ProjectListPage, { global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, router }
}

describe('ProjectListPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('lists name, display name, role, members, repositories, quota usage and creation', async () => {
    const { wrapper } = await mountPage()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('alpha')
    expect(rows[0]!.text()).toContain('Alpha Team')
    expect(rows[0]!.text()).toContain('Owner')
    expect(rows[0]!.text()).toContain('4')
    expect(rows[0]!.text()).toContain('2.0 GB')
    expect(rows[0]!.find('[role="progressbar"]').attributes('aria-valuenow')).toBe('50')
    expect(rows[0]!.find('a').attributes('href')).toBe('/projects/alpha')
    expect(rows[1]!.text()).toContain('Viewer')
    expect(rows[1]!.text()).toContain('no quota')
    expect(rows[1]!.find('[role="progressbar"]').exists()).toBe(false)
    // A display name equal to the name is not repeated.
    expect(rows[1]!.text().match(/beta/g)).toHaveLength(1)
    expect(requests()).toEqual(['GET /api/v1/projects'])
  })

  it('explains why creation is off instead of hiding the button', async () => {
    const { wrapper } = await mountPage({ ...MINE, can_create: false })
    const button = wrapper.get('[data-testid="new-project"]')
    expect(button.attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="create-blocked"]').text()).toContain('limited to administrators')
  })

  it('enables New project when can_create is true', async () => {
    const { wrapper } = await mountPage({ ...MINE, can_create: true })
    expect(wrapper.get('[data-testid="new-project"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-testid="create-blocked"]').exists()).toBe(false)
  })

  it('does not offer the scope control to non-admins and never asks for all projects', async () => {
    const { wrapper } = await mountPage(MINE, { id: 'u', username: 'ann' }, '/projects?scope=all')
    expect(wrapper.find('[role="group"]').exists()).toBe(false)
    expect(requests()).toEqual(['GET /api/v1/projects'])
  })

  it('opens admins on All and refetches only Mine when it is picked', async () => {
    const { wrapper } = await mountPage({ ...MINE, can_create: true }, { id: 'a', username: 'root', is_admin: true })
    const buttons = wrapper.findAll('[role="group"] button')
    expect(buttons.map((b) => [b.text(), b.attributes('aria-pressed')])).toEqual([['Mine', 'false'], ['All', 'true']])
    await buttons[0]!.trigger('click')
    await flushPromises()
    expect(requests()).toEqual(['GET /api/v1/projects?all=true', 'GET /api/v1/projects'])
  })

  it('searches in the URL without another request', async () => {
    const { wrapper, router } = await mountPage()
    const search = wrapper.get('input[type="search"]')
    await search.setValue('alp')
    await new Promise((r) => setTimeout(r, 400))
    await flushPromises()
    expect(router.currentRoute.value.query.q).toBe('alp')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(requests()).toEqual(['GET /api/v1/projects'])
  })

  it('shows an empty state without a project', async () => {
    const { wrapper } = await mountPage({ projects: [], can_create: true })
    expect(wrapper.text()).toContain('You are not a member of any project')
    expect(wrapper.text()).toContain('Create a project')
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('opens the create drawer from ?new=1', async () => {
    await mountPage({ ...MINE, can_create: true }, { id: 'u', username: 'ann' }, '/projects?new=1')
    expect(document.body.querySelector('#new-project-name')).not.toBeNull()
  })

  it('drops ?new=1 without a drawer when the caller may not create', async () => {
    const { router } = await mountPage({ ...MINE, can_create: false }, { id: 'u', username: 'ann' }, '/projects?new=1')
    expect(document.body.querySelector('#new-project-name')).toBeNull()
    expect(router.currentRoute.value.query.new).toBeUndefined()
  })

  it('creates a project with only name, display name and description, then opens it', async () => {
    const { wrapper, router } = await mountPage({ ...MINE, can_create: true })
    await wrapper.get('[data-testid="new-project"]').trigger('click')
    await flushPromises()

    const name = document.body.querySelector('#new-project-name') as HTMLInputElement
    const display = document.body.querySelector('#new-project-display-name') as HTMLInputElement
    const description = document.body.querySelector('#new-project-description') as HTMLTextAreaElement
    expect(name).not.toBeNull()
    name.value = 'new-one'
    name.dispatchEvent(new Event('input'))
    display.value = 'New One'
    display.dispatchEvent(new Event('input'))
    description.value = 'for tests'
    description.dispatchEvent(new Event('input'))
    await flushPromises()

    const form = document.body.querySelector('#project-create-form') as HTMLFormElement
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await flushPromises()

    const post = fetchSpy.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'POST')!
    expect(String(post[0])).toBe('/api/v1/projects')
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({
      name: 'new-one',
      display_name: 'New One',
      description: 'for tests',
    })
    expect(router.currentRoute.value.path).toBe('/projects/new-one')
  })
})

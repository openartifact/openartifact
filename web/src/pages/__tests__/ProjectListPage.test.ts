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
    { id: '1', name: 'alpha', display_name: 'Alpha Team', my_role: 'owner', repository_count: 3 },
    { id: '2', name: 'beta', display_name: 'beta', my_role: 'viewer', repository_count: 0 },
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

async function mountPage(list: unknown = MINE, user: Partial<User> = { id: 'u', username: 'ann' }) {
  setActivePinia(createPinia())
  const session = useSessionStore()
  session.user = user as User
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const method = init?.method ?? 'GET'
    if (method === 'POST') return json({ name: 'new-one' }, 201)
    return json(list)
  })
  const router = makeRouter()
  await router.push('/projects')
  await router.isReady()
  const wrapper = mount(ProjectListPage, { global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, router }
}

describe('ProjectListPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('lists name, display name, role badge and repository count', async () => {
    const { wrapper } = await mountPage()
    const rows = wrapper.findAll('[data-testid="project-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('alpha')
    expect(rows[0]!.text()).toContain('Alpha Team')
    expect(rows[0]!.find('[data-testid="role-badge"]').text()).toBe('Owner')
    expect(rows[0]!.text()).toContain('3')
    expect(rows[0]!.find('a').attributes('href')).toBe('/projects/alpha')
    expect(rows[1]!.find('[data-testid="role-badge"]').text()).toBe('Viewer')
    // A display name equal to the name is not repeated.
    expect(rows[1]!.findAll('td')[1]!.text()).toBe('')
    expect(requests()).toEqual(['GET /api/v1/projects'])
  })

  it('hides New project when the API says the caller cannot create', async () => {
    const { wrapper } = await mountPage({ ...MINE, can_create: false })
    expect(wrapper.find('[data-testid="new-project"]').exists()).toBe(false)
  })

  it('shows New project when can_create is true', async () => {
    const { wrapper } = await mountPage({ ...MINE, can_create: true })
    expect(wrapper.find('[data-testid="new-project"]').exists()).toBe(true)
  })

  it('does not offer the All projects toggle to non-admins', async () => {
    const { wrapper } = await mountPage()
    expect(wrapper.find('[role="switch"]').exists()).toBe(false)
  })

  it('offers the All projects toggle to admins and reloads with ?all=true', async () => {
    const admin = await mountPage({ ...MINE, can_create: true }, { id: 'a', username: 'root', is_admin: true })
    const toggle = admin.wrapper.find('[role="switch"]')
    expect(toggle.exists()).toBe(true)
    await toggle.trigger('click')
    await flushPromises()
    expect(requests()).toEqual(['GET /api/v1/projects', 'GET /api/v1/projects?all=true'])
  })

  it('shows an empty state without a project', async () => {
    const { wrapper } = await mountPage({ projects: [], can_create: true })
    expect(wrapper.text()).toContain('No projects yet')
    expect(wrapper.text()).toContain('Create a project')
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('creates a project with only name, display name and description, then opens it', async () => {
    const { wrapper, router } = await mountPage({ ...MINE, can_create: true })
    await wrapper.find('[data-testid="new-project"]').trigger('click')
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

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ProjectHomePage from '../ProjectHomePage.vue'
import { useSessionStore, type User } from '../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const REPOS = {
  repositories: [
    { id: 'r1', name: 'web-deb', format: 'deb', repo_type: 'hosted', public: false, created_at: '2026-01-02T00:00:00Z' },
    { id: 'r2', name: 'web-npm', format: 'npm', repo_type: 'proxy', public: true, created_at: '2026-01-01T00:00:00Z' },
  ],
  total: 2,
  page: 1,
  limit: 20,
}
const MEMBERS = {
  members: [
    { id: 'm1', user_id: 'u1', username: 'ann', role: 'owner', created_at: '2026-01-01T00:00:00Z' },
    { id: 'm2', user_id: 'u2', username: 'bob', role: 'developer', created_at: '2026-01-01T00:00:00Z' },
  ],
}
const GROUPS = { groups: [{ group_id: 'g1', group_name: 'devs', role: 'developer', created_at: '2026-01-01T00:00:00Z' }] }

function project(my_role: string, extra: Record<string, unknown> = {}) {
  return {
    id: 'p1',
    name: 'alpha',
    display_name: 'Alpha Team',
    description: 'The alpha project',
    owner_id: 'u1',
    my_role,
    repository_count: 2,
    ...extra,
  }
}

let fetchSpy: Mock<typeof fetch>

/** Answers the project endpoints; `status` makes the project fetch itself fail. */
async function mountPage(opts: { role?: string; status?: number; query?: string; user?: Partial<User> } = {}) {
  setActivePinia(createPinia())
  useSessionStore().user = (opts.user ?? { id: 'u', username: 'ann' }) as User
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const path = new URL(String(input), 'http://localhost').pathname
    if (path === '/api/v1/projects/alpha') {
      return opts.status ? json({ error: 'nope' }, opts.status) : json(project(opts.role ?? 'viewer'))
    }
    if (path.endsWith('/repositories')) return json(REPOS)
    if (path.endsWith('/members')) return json(MEMBERS)
    if (path.endsWith('/groups')) return json(GROUPS)
    return json({}, 404)
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/projects', component: { template: '<div />' } },
      { path: '/projects/:project', component: ProjectHomePage },
      { path: '/repos/:name', component: { template: '<div />' } },
      { path: '/admin/projects', component: { template: '<div />' } },
    ],
  })
  await router.push(`/projects/alpha${opts.query ?? ''}`)
  await router.isReady()
  const wrapper = mount(ProjectHomePage, { global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, router }
}

const tabLabels = (w: VueWrapper) =>
  w.findAll('nav[aria-label="Project sections"] a').map((a) => a.text().replace(/\s*\d+$/, ''))

describe('ProjectHomePage', () => {
  it('shows the overview: facts, counts and recent repositories', async () => {
    const { wrapper } = await mountPage({ role: 'viewer' })
    expect(wrapper.find('h1').text()).toBe('Alpha Team')
    const facts = wrapper.find('[data-testid="project-facts"]')
    expect(facts.text()).toContain('alpha')
    expect(facts.find('[data-testid="fact-repositories"]').text()).toBe('2')
    expect(facts.find('[data-testid="fact-members"]').text()).toBe('2')
    expect(wrapper.text()).toContain('web-deb')
    expect(wrapper.text()).toContain('web-npm')
  })

  it('lists every section for a viewer, with the tab kept in ?tab=', async () => {
    const { wrapper } = await mountPage({ role: 'viewer' })
    expect(tabLabels(wrapper)).toEqual(['Overview', 'Repositories', 'Members', 'Tokens', 'Settings'])
  })

  it('shows the repositories tab as a paginated table linking to /repos/:name', async () => {
    const { wrapper } = await mountPage({ role: 'viewer', query: '?tab=repositories' })
    const rows = wrapper.findAll('[data-testid="repo-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.find('a').attributes('href')).toBe('/repos/web-deb')
    expect(rows[0]!.text()).toContain('Deb')
    expect(rows[1]!.text()).toContain('Public')
    expect(wrapper.text()).toContain('Showing 1–2 of 2')
    expect(fetchSpy.mock.calls.map((c) => String(c[0]))).toContain('/api/v1/projects/alpha/repositories?page=1&limit=20')
  })

  it('falls back to the overview for an unknown ?tab=', async () => {
    const { wrapper } = await mountPage({ role: 'viewer', query: '?tab=bogus' })
    expect(wrapper.find('[data-testid="project-facts"]').exists()).toBe(true)
  })

  describe('members tab by role', () => {
    it('is read-only for a viewer', async () => {
      const { wrapper } = await mountPage({ role: 'viewer', query: '?tab=members' })
      expect(wrapper.findAll('[data-testid="member-row"]')).toHaveLength(2)
      expect(wrapper.find('[data-testid="member-row"]').text()).toContain('ann')
      expect(wrapper.find('[data-testid="member-add"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="group-attach"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="members-readonly"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="member-row"] select').exists()).toBe(false)
    })

    it('is read-only for a developer', async () => {
      const { wrapper } = await mountPage({ role: 'developer', query: '?tab=members' })
      expect(wrapper.find('[data-testid="member-add"]').exists()).toBe(false)
    })

    it('offers add, attach and role changes to a maintainer', async () => {
      const { wrapper } = await mountPage({ role: 'maintainer', query: '?tab=members' })
      expect(wrapper.find('[data-testid="member-add"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="group-attach"]').exists()).toBe(true)
      expect(wrapper.findAll('[data-testid="member-row"] select')).toHaveLength(2)
    })
  })

  describe('settings tab by role', () => {
    it('is read-only for a maintainer and shows the policy as managed by a global admin', async () => {
      const { wrapper } = await mountPage({ role: 'maintainer', query: '?tab=settings' })
      expect(wrapper.find('[data-testid="project-edit"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="project-readonly"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="project-danger"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="project-policy"]').text()).toContain('managed by a global admin')
      expect(wrapper.find('[data-testid="project-policy"] a').exists()).toBe(false)
    })

    it('lets an owner edit the details with PATCH and delete the project', async () => {
      const { wrapper } = await mountPage({ role: 'owner', query: '?tab=settings' })
      expect(wrapper.find('[data-testid="project-edit"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="project-danger"]').exists()).toBe(true)

      const input = wrapper.find('#project-display-name')
      await input.setValue('Alpha Renamed')
      await wrapper.find('[data-testid="project-edit"]').trigger('submit')
      await flushPromises()
      const patch = fetchSpy.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PATCH')!
      expect(String(patch[0])).toBe('/api/v1/projects/alpha')
      expect(JSON.parse((patch[1] as RequestInit).body as string)).toEqual({
        display_name: 'Alpha Renamed',
        description: 'The alpha project',
      })
    })

    it('points admins at the administration page for the policy', async () => {
      const { wrapper } = await mountPage({
        role: 'owner',
        query: '?tab=settings',
        user: { id: 'a', username: 'root', is_admin: true },
      })
      expect(wrapper.find('[data-testid="project-policy"] a').attributes('href')).toBe('/admin/projects')
    })
  })

  it('shows the not-found state for 404', async () => {
    const { wrapper } = await mountPage({ status: 404 })
    expect(wrapper.text()).toContain('Project not found')
    expect(wrapper.find('nav[aria-label="Project sections"]').exists()).toBe(false)
  })

  it('shows the forbidden state for 403', async () => {
    const { wrapper } = await mountPage({ status: 403 })
    expect(wrapper.text()).toContain('Access denied')
  })

  it('offers a retry for other failures', async () => {
    const { wrapper } = await mountPage({ status: 500 })
    expect(wrapper.text()).toContain('Failed to load the project')
    expect(wrapper.text()).toContain('Retry')
  })
})

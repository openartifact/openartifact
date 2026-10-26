// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import RepoDetailPage from '../RepoDetailPage.vue'
import { useSessionStore, type User } from '../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

async function mountPage(repo: Record<string, unknown> | number, user: Partial<User>, path = '/repos/libs') {
  setActivePinia(createPinia())
  useSessionStore().user = user as User
  spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
    const url = String(input)
    if (/\/repository\/libs$/.test(url)) return typeof repo === 'number' ? json({ error: 'not found' }, repo) : json(repo)
    if (url.includes('/packages')) return json({ total: 0, packages: [] })
    if (url.includes('/members')) return json({ members: [] })
    if (url.includes('/security/summary')) return json({ scanned_at: null })
    return json({})
  }) as never)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/repos/:repo', component: RepoDetailPage }, { path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  await router.isReady()
  const w = mount(RepoDetailPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

const tabNames = (w: ReturnType<typeof mount>) => w.findAll('[role="tab"]').map((t) => t.text())

describe('RepoDetailPage', () => {
  const repo = { name: 'libs', format: 'npm', repo_type: 'hosted', public: false }

  it('shows the connect block and every maintainer tab to an administrator', async () => {
    const w = await mountPage({ ...repo, permissions: { admin: true, delete: true } }, { id: 'a', username: 'root', is_admin: true })
    expect(w.text()).toContain('Connect a client')
    const names = tabNames(w).join('|')
    for (const n of ['Packages', 'Activity', 'Security', 'Trash', 'Settings']) expect(names).toContain(n)
    w.unmount()
  })

  it('hides the maintainer tabs from a plain reader but keeps the connect block', async () => {
    const w = await mountPage({ ...repo, permissions: {} }, { id: 'u', username: 'ann' })
    const names = tabNames(w).join('|')
    expect(names).toContain('Security')
    expect(names).not.toContain('Settings')
    expect(names).not.toContain('Activity')
    expect(w.text()).toContain('Connect a client')
    w.unmount()
  })

  it('shows members instead of packages for a group to people who administer it', async () => {
    const w = await mountPage({ ...repo, repo_type: 'group', permissions: { admin: true } }, { id: 'u', username: 'ann' })
    const names = tabNames(w)
    expect(names.some((n) => n.startsWith('Members'))).toBe(true)
    expect(names.some((n) => n.startsWith('Packages'))).toBe(false)
    expect(w.get('[data-testid="edit-members"]').text()).toBe('Edit members')
    // a group cannot be published to
    expect(w.find('[data-testid="connect-read-only"]').exists()).toBe(true)
    w.unmount()
  })

  it('offers no publish command for a proxy, but does for a hosted repository', async () => {
    const proxy = await mountPage({ ...repo, repo_type: 'proxy', permissions: {} }, { id: 'u', username: 'ann' })
    expect(proxy.find('[data-testid="connect-read-only"]').exists()).toBe(true)
    proxy.unmount()
    const hosted = await mountPage({ ...repo, permissions: {} }, { id: 'u', username: 'ann' })
    expect(hosted.find('[data-testid="connect-read-only"]').exists()).toBe(false)
    hosted.unmount()
  })

  it('does not request the member list of a group for a plain reader, whom the server refuses', async () => {
    const w = await mountPage({ ...repo, repo_type: 'group', permissions: {} }, { id: 'u', username: 'ann' })
    expect(tabNames(w).some((n) => n.startsWith('Members'))).toBe(false)
    expect((globalThis.fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls.some((c) => String(c[0]).includes('/members'))).toBe(false)
    w.unmount()
  })

  it('says so when the repository does not exist', async () => {
    const w = await mountPage(404, { id: 'a', username: 'root', is_admin: true })
    expect(w.text()).toContain('Repository not found')
    w.unmount()
  })
})

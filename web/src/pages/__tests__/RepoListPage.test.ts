// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import RepoListPage from '../RepoListPage.vue'
import { useSessionStore, type User } from '../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const ROW = {
  name: 'libs', format: 'npm', repo_type: 'hosted', public: false, description: 'shared libs',
  stats: {
    package_count: 7, size_bytes: 2048, last_published_at: '2026-01-01T00:00:00Z',
    security: { critical: 1, high: 0, medium: 0, low: 0, secrets: 0, license_violations: 0, scanned_at: '2026-01-01T00:00:00Z' },
    quota: { used_bytes: 2048 },
  },
}

async function mountPage(body: unknown, path = '/repos', user: Partial<User> = { id: 'a', username: 'root', is_admin: true }) {
  setActivePinia(createPinia())
  useSessionStore().user = user as User
  const spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
    const url = String(input)
    return url.includes('/repositories') ? json(body) : json({})
  }) as never)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: RepoListPage }] })
  await router.push(path)
  await router.isReady()
  const w = mount(RepoListPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { w, router, spy }
}

const repoCalls = (spy: ReturnType<typeof spyOn>) =>
  spy.mock.calls.map((c) => String(c[0])).filter((u) => u.includes('/api/v1/repositories'))

describe('RepoListPage', () => {
  it('shows stats from the API for each repository', async () => {
    const { w } = await mountPage({ repositories: [ROW], total: 1 })
    const text = w.text()
    expect(text).toContain('libs')
    expect(text).toContain('7')
    expect(text).toContain('2.0 KB')
    expect(text.toLowerCase()).toContain('critical')
    w.unmount()
  })

  it('sends the chip filter to the server and keeps it in the URL', async () => {
    const { w, router, spy } = await mountPage({ repositories: [ROW], total: 1 })
    await w.get('[data-testid="chip-flagged"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.flagged).toBeDefined()
    expect(repoCalls(spy).at(-1)).toContain('flagged=true')
    w.unmount()
  })

  it('offers creation in the empty state to those who can create', async () => {
    const { w } = await mountPage({ repositories: [], total: 0 })
    expect(w.text()).toContain('No repositories')
    expect(w.text()).toContain('New repository')
    w.unmount()
  })

  it('does not open the create drawer for a user who cannot create', async () => {
    const { w, router } = await mountPage({ repositories: [], total: 0 }, '/repos?new=1', { id: 'u', username: 'ann' })
    expect(w.text()).not.toContain('New repository')
    expect(router.currentRoute.value.query.new).toBeUndefined()
    w.unmount()
  })
})

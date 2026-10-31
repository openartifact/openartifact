// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import TrashList from '../TrashList.vue'
import { useSessionStore } from '../../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const item = (id: string, kind = 'npm_version') => ({
  id,
  kind,
  repository_id: 'r1',
  repository: 'web',
  format: 'npm',
  name: `pkg-${id}`,
  version: '1.0.0',
  size_bytes: 2048,
  deleted_at: '2026-01-02T03:04:05Z',
  deleted_by: 'alice',
  deleted_by_name: 'alice',
  expires_at: '2999-01-01T00:00:00Z',
})

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

async function mountList(props: Record<string, unknown>, admin: boolean, respond: (url: string, init?: RequestInit) => Response) {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => respond(String(input), init))
  const pinia = createPinia()
  setActivePinia(pinia)
  useSessionStore().user = { id: '1', username: 'u', is_admin: admin } as never
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
  await router.push('/')
  const wrapper = mount(TrashList as never, { props, global: { plugins: [pinia, router] } } as never)
  await flushPromises()
  return wrapper
}

describe('TrashList', () => {
  it('lists the trash of one repository without a repository column and lets a delete-permission user restore', async () => {
    const w = await mountList({ repo: 'web', permissions: { delete: true } }, false, () => json({ items: [item('a')], total: 1, page: 1, limit: 25 }))
    expect(String(spy.mock.calls[0]![0])).toContain('/api/v1/repositories/web/trash?')
    const headers = w.findAll('thead th').map((th) => th.text())
    expect(headers).not.toContain('Repository')
    expect(w.text()).toContain('pkg-a')
    const buttons = w.findAll('tbody button').map((b) => b.attributes('aria-label'))
    expect(buttons).toContain('Restore: pkg-a')
    expect(buttons).not.toContain('Purge: pkg-a')
  })

  it('shows the repository column and purge action in the global admin view', async () => {
    const w = await mountList({}, true, () => json({ items: [item('a')], total: 1, page: 1, limit: 25 }))
    expect(String(spy.mock.calls[0]![0])).toContain('/api/v1/trash?')
    expect(w.findAll('thead th').map((th) => th.text())).toContain('Repository')
    expect(w.findAll('tbody button').map((b) => b.attributes('aria-label'))).toContain('Purge: pkg-a')
  })

  it('offers emptying only with the manage permission and shows an empty state', async () => {
    const empty = await mountList({ repo: 'web', permissions: { delete: true } }, false, () => json({ items: [], total: 0 }))
    expect(empty.text()).toContain('The trash is empty')
    const full = await mountList({ repo: 'web', permissions: { delete: true, manage: true } }, false, () => json({ items: [item('a')], total: 1 }))
    expect(full.text()).toContain('Empty trash')
    const noManage = await mountList({ repo: 'web', permissions: { delete: true } }, false, () => json({ items: [item('a')], total: 1 }))
    expect(noManage.text()).not.toContain('Empty trash')
  })

  it('says so when the caller may not view the trash', async () => {
    const w = await mountList({ repo: 'web' }, false, () => json({ error: 'forbidden' }, 403))
    expect(w.find('[role=alert]').text()).toContain('permission')
  })
})

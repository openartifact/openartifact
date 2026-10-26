// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import AuditLogPage from '../AuditLogPage.vue'

interface Entry {
  id: string
  action: string
  timestamp?: string
  actor?: string
  actor_name?: string
  ip?: string
  subject?: { kind: string; name: string | null; version?: string; repository?: string } | null
}

type Page = { data: Entry[]; has_more: boolean; next_cursor?: string }

let spy: Mock<typeof fetch> | undefined
let urls: URL[] = []

function serve(pages: Record<string, Page>): void {
  urls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost')
    urls.push(url)
    return new Response(JSON.stringify(pages[url.searchParams.get('cursor') ?? ''] ?? { data: [], has_more: false }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as never)
}

async function mountPage(path = '/settings/audit') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const wrapper = mount(AuditLogPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return Object.assign(wrapper, { router })
}

const rowsText = (w: VueWrapper): string[] => w.findAll('tbody tr').map((r) => r.text())

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('AuditLogPage', () => {
  it('shows the actor name, the subject as a link and the time in UTC', async () => {
    serve({
      '': {
        data: [
          {
            id: 'e1',
            action: 'repository.created',
            timestamp: '2026-03-05T23:30:00+02:00',
            actor: 'u-1',
            actor_name: 'Alice Admin',
            subject: { kind: 'repository', name: 'npm-hosted' },
          },
        ],
        has_more: false,
      },
    })
    const w = await mountPage()

    const row = w.find('tbody tr')
    expect(row.text()).toContain('Alice Admin')
    expect(row.text()).not.toContain('u-1')
    expect(row.find('[data-testid="audit-time"]').text()).toBe('2026-03-05 21:30:00')
    expect(w.find('thead').text()).toContain('Time (UTC)')
    const link = row.find('a')
    expect(link.text()).toBe('npm-hosted')
    expect(link.attributes('href')).toBe('/repos/npm-hosted')
    expect(row.text()).toContain('Repository')
    w.unmount()
  })

  it('marks a subject that no longer exists', async () => {
    serve({ '': { data: [{ id: 'e1', action: 'repository.deleted', subject: { kind: 'repository', name: null } }], has_more: false } })
    const w = await mountPage()
    expect(rowsText(w)[0]).toContain('(removed)')
    expect(w.find('tbody a').exists()).toBe(false)
    w.unmount()
  })

  it('hides HTTP request events by default, says how many, and shows them on request', async () => {
    serve({
      '': {
        data: [
          { id: 'h1', action: 'http.request' },
          { id: 'a1', action: 'auth.login' },
          { id: 'h2', action: 'http.request' },
        ],
        has_more: false,
      },
    })
    const w = await mountPage()
    expect(rowsText(w)).toHaveLength(1)
    expect(rowsText(w)[0]).toContain('auth.login')
    expect(w.find('[data-testid="audit-hidden"]').text()).toContain('2 HTTP request events hidden')
    expect(w.find('[data-testid="audit-http-toggle"]').attributes('aria-pressed')).toBe('true')

    await w.find('[data-testid="audit-hidden"] button').trigger('click')
    await flushPromises()
    expect(w.router.currentRoute.value.query.http).toBe('1')
    expect(rowsText(w)).toHaveLength(3)
    expect(w.find('[data-testid="audit-http-toggle"]').attributes('aria-pressed')).toBe('false')
    expect(w.find('[data-testid="audit-hidden"]').exists()).toBe(false)
    w.unmount()
  })

  it('keeps reading chunks until something visible turns up', async () => {
    serve({
      '': { data: [{ id: 'h1', action: 'http.request' }], has_more: true, next_cursor: 'c2' },
      c2: { data: [{ id: 'a1', action: 'auth.login' }], has_more: false },
    })
    const w = await mountPage()
    expect(rowsText(w)).toHaveLength(1)
    expect(urls.map((u) => u.searchParams.get('cursor'))).toEqual(['', 'c2'])
    w.unmount()
  })

  it('loads more with the cursor of the previous chunk and stops on the last one', async () => {
    const page = (n: number, more: boolean): Page => ({
      data: Array.from({ length: 30 }, (_, i) => ({ id: `${n}-${i}`, action: 'auth.login' })),
      has_more: more,
      next_cursor: more ? `c${n + 1}` : undefined,
    })
    serve({ '': page(1, true), c2: page(2, false) })
    const w = await mountPage()
    expect(rowsText(w)).toHaveLength(30)

    const more = w.findAll('button').find((b) => b.text() === 'Load more')!
    await more.trigger('click')
    await flushPromises()
    expect(rowsText(w)).toHaveLength(60)
    expect(w.findAll('button').some((b) => b.text() === 'Load more')).toBe(false)
    expect(urls.map((u) => u.searchParams.get('cursor'))).toEqual(['', 'c2'])
    w.unmount()
  })

  it('opens an event in a drawer and can show every event of its actor', async () => {
    serve({ '': { data: [{ id: 'e1', action: 'auth.login', actor: 'u-1', actor_name: 'Alice', ip: '10.0.0.7' }], has_more: false } })
    const w = await mountPage()

    await w.find('tbody button').trigger('click')
    await flushPromises()
    const detail = document.body.querySelector('[data-testid="audit-detail"]')!
    expect(detail.textContent).toContain('10.0.0.7')
    expect(detail.textContent).toContain('Alice')

    ;(document.body.querySelector('[data-testid="audit-by-actor"]') as HTMLButtonElement).click()
    await new Promise((r) => setTimeout(r, 450))
    await flushPromises()
    expect((w.find('input[type="search"]').element as HTMLInputElement).value).toBe('u-1')
    expect(w.router.currentRoute.value.query.actor).toBe('u-1')
    expect(urls.at(-1)!.searchParams.get('actor')).toBe('u-1')
    w.unmount()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import CleanupSection from '../CleanupSection.vue'
import { useToastStore } from '../../../../stores/toast'

const REPOS = {
  repositories: [
    { id: '1', name: 'npm-a', format: 'npm', repo_type: 'hosted' },
    { id: '2', name: 'npm-proxy', format: 'npm', repo_type: 'proxy' },
    { id: '3', name: 'locked', format: 'raw', repo_type: 'hosted' },
    { id: '4', name: 'deb-b', format: 'deb', repo_type: 'hosted' },
  ],
  total: 4,
}

const POLICIES: Record<string, unknown[]> = {
  'npm-a': [
    { id: 'p1', name: 'keep five', enabled: true, keep_last_n: 5, last_run_at: null, created_at: '2026-01-01T00:00:00Z' },
    { id: 'p2', name: 'old stuff', enabled: false, keep_since_days: 30, created_at: '2026-01-01T00:00:00Z' },
  ],
  'deb-b': [{ id: 'p3', name: 'debs', enabled: true, keep_last_n: 3, created_at: '2026-01-01T00:00:00Z' }],
}

interface Call {
  method: string
  path: string
}

let spy: Mock<typeof fetch> | undefined
let calls: Call[] = []

function serve(): void {
  calls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const method = init?.method ?? 'GET'
    calls.push({ method, path: url.pathname })
    const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
    if (url.pathname === '/api/v1/repositories') return json(REPOS)
    if (url.pathname.endsWith('/dry-run')) return json({ repository: 'npm-a', format: 'npm', dry_run: true, rows: 2, items: [{ id: 'i1', name: 'left-pad', version: '1.0.0' }], truncated: false })
    if (url.pathname.endsWith('/run')) return new Response(null, { status: 202 })
    const repo = url.pathname.split('/')[4]
    if (repo === 'locked') return json({ error: 'forbidden' }, 403)
    return json({ policies: POLICIES[repo] ?? [] })
  }) as never)
}

async function mountPage(path = '/settings/cleanup') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const w = mount(CleanupSection, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return Object.assign(w, { router })
}

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('CleanupSection', () => {
  it('gathers the policies of every hosted repository and skips proxies', async () => {
    serve()
    const w = await mountPage()
    expect(w.findAll('tbody tr')).toHaveLength(3)
    expect(calls.some((c) => c.path.includes('npm-proxy'))).toBe(false)
    const keep = w.findAll('tbody tr').find((r) => r.text().includes('keep five'))!
    expect(keep.text()).toContain('npm-a')
    expect(keep.text()).toContain('Enabled')
    expect(keep.text()).toContain('Never')
    expect(keep.find('a').attributes('href')).toBe('/repos/npm-a?tab=cleanup')
    expect(w.findAll('tbody tr').find((r) => r.text().includes('old stuff'))!.text()).toContain('Disabled')
    w.unmount()
  })

  it('names the repositories whose policies could not be read', async () => {
    serve()
    const w = await mountPage()
    expect(w.find('[data-testid="cleanup-partial"]').text()).toContain('locked')
    w.unmount()
  })

  it('filters by status and by text', async () => {
    serve()
    const w = await mountPage()
    await w.findAll('button').find((b) => b.text() === 'Disabled')!.trigger('click')
    await flushPromises()
    expect(w.findAll('tbody tr')).toHaveLength(1)
    expect(w.router.currentRoute.value.query.status).toBe('disabled')

    await w.findAll('button').find((b) => b.text() === 'All')!.trigger('click')
    await w.find('input[type="search"]').setValue('deb')
    await new Promise((r) => setTimeout(r, 450))
    await flushPromises()
    expect(w.findAll('tbody tr')).toHaveLength(1)
    expect(w.find('tbody tr').text()).toContain('debs')
    w.unmount()
  })

  it('previews what a run would delete without running anything', async () => {
    serve()
    const w = await mountPage()
    await w.find('button[aria-label="Preview keep five"]').trigger('click')
    await flushPromises()
    expect(calls.at(-1)).toEqual({ method: 'POST', path: '/api/v1/repositories/npm-a/cleanup-policies/p1/dry-run' })
    expect(document.body.querySelector('[data-testid="dryrun-total"]')!.textContent).toContain('2')
    expect(calls.some((c) => c.path.endsWith('/run'))).toBe(false)
    w.unmount()
  })

  it('runs a policy now and reports it', async () => {
    serve()
    const w = await mountPage()
    await w.find('button[aria-label="Run keep five now"]').trigger('click')
    await flushPromises()
    expect(calls.filter((c) => c.method === 'POST').map((c) => c.path)).toEqual(['/api/v1/repositories/npm-a/cleanup-policies/p1/run'])
    expect(useToastStore().queue.some((x) => x.level === 'success' && x.title.includes('keep five'))).toBe(true)
    w.unmount()
  })

  it('links edit to the repository Cleanup tab', async () => {
    serve()
    const w = await mountPage()
    expect(w.find('a[aria-label="Edit debs in repository deb-b"]').attributes('href')).toBe('/repos/deb-b?tab=cleanup')
    w.unmount()
  })
})

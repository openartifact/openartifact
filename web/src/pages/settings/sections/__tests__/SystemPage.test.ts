// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import SystemPage from '../SystemPage.vue'

const STAMPED = {
  version: '0.6.0',
  commit: 'd6fdb07abcdef0123456789abcdef0123456789a',
  build_time: '2026-03-05T10:00:00Z',
  go_version: 'go1.25',
  os: 'linux',
  arch: 'amd64',
  uptime: '3h 2m',
  db_status: 'ok',
  storage_kind: 'filesystem',
  storage_status: 'ok',
}

const HEALTH = {
  scanners: [
    { name: 'osv', ready: true },
    { name: 'trivy', ready: false, message: 'binary not found', docs_url: 'https://docs.example.test/trivy' },
  ],
  last_scan_at: '2026-03-05T09:00:00Z',
  scans_pending: 2,
  scans_failed_24h: 1,
  dependency_track: { enabled: false },
  ai: { enabled: true },
  unscanned_repositories: 3,
}

let spy: Mock<typeof fetch> | undefined

function serve(info: unknown, health: unknown): void {
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    const url = String(input)
    const body = url.includes('/admin/system/info') ? info : health
    if (body === null) return new Response(JSON.stringify({ error: 'down' }), { status: 500 })
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as never)
}

async function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/health')
  const w = mount(SystemPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('SystemPage', () => {
  it('lists every component with a status in words, including scanners, Dependency-Track and AI', async () => {
    serve(STAMPED, HEALTH)
    const w = await mountPage()
    const rows = w.findAll('tbody tr').map((r) => r.text())

    expect(rows.find((r) => r.includes('Database'))).toContain('Healthy')
    expect(rows.find((r) => r.includes('OSV-Scanner'))).toContain('Healthy')
    const trivy = rows.find((r) => r.includes('Trivy'))!
    expect(trivy).toContain('Warning')
    expect(trivy).toContain('binary not found')
    expect(w.find('a[href="https://docs.example.test/trivy"]').exists()).toBe(true)
    expect(rows.find((r) => r.includes('Dependency-Track'))).toContain('Not configured')
    expect(rows.find((r) => r.includes('AI analysis'))).toContain('Enabled')
    expect(rows.find((r) => r.includes('Scan jobs'))).toContain('2 pending or running, 1 failed in 24 hours')
    expect(rows.find((r) => r.includes('Repositories never scanned'))).toContain('no completed scan: 3')
    expect(w.find('[data-testid="health-summary"]').text()).toContain('3 warnings')
    w.unmount()
  })

  it('shows the short commit sha with its full value on hover and no ldflags notice for a stamped build', async () => {
    serve(STAMPED, HEALTH)
    const w = await mountPage()
    const commit = w.find('[data-testid="build-commit"]')
    expect(commit.text()).toContain('d6fdb07')
    expect(commit.text()).not.toContain('d6fdb07a')
    expect(commit.find('span[title]').attributes('title')).toBe(STAMPED.commit)
    expect(w.find('[data-testid="build-version"]').text()).toBe('v0.6.0')
    expect(w.find('[data-testid="unstamped"]').exists()).toBe(false)
    w.unmount()
  })

  it('says so, with the ldflags fix, when the build carries no version metadata', async () => {
    serve({ ...STAMPED, version: 'dev', commit: 'unknown', build_time: 'unknown' }, HEALTH)
    const w = await mountPage()
    const notice = w.find('[data-testid="unstamped"]')
    expect(notice.text()).toContain('compiled without version metadata')
    expect(notice.find('[data-testid="ldflags"]').text()).toContain('-X main.version=')
    expect(w.find('[data-testid="build-version"]').text()).toBe('Not recorded')
    expect(w.find('[data-testid="build-commit"]').text()).toBe('Not recorded')
    w.unmount()
  })

  it('marks an unavailable database', async () => {
    serve({ ...STAMPED, db_status: 'error' }, HEALTH)
    const w = await mountPage()
    expect(w.findAll('tbody tr').find((r) => r.text().includes('Database'))!.text()).toContain('Unavailable')
    expect(w.find('[data-testid="health-summary"]').text()).toContain('1 component unavailable')
    w.unmount()
  })

  it('still shows the server components when the scanner health cannot be read', async () => {
    serve(STAMPED, null)
    const w = await mountPage()
    const rows = w.findAll('tbody tr').map((r) => r.text())
    expect(rows.find((r) => r.includes('Database'))).toContain('Healthy')
    expect(rows.find((r) => r.includes('Scanning'))).toContain('Unavailable')
    w.unmount()
  })

  it('offers a retry when the system info cannot be read', async () => {
    serve(null, HEALTH)
    const w = await mountPage()
    expect(w.find('[data-testid="health-error"]').exists()).toBe(true)
    expect(w.text()).toContain('Retry')
    w.unmount()
  })
})

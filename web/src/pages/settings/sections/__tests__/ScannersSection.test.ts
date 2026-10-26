// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ScannersSection from '../ScannersSection.vue'

const HEALTH = {
  scanners: [
    { name: 'osv', ready: true },
    { name: 'trivy', ready: false, message: 'binary not found', docs_url: 'https://docs.example.test/trivy' },
    { name: 'grype', ready: false, docs_url: 'javascript:alert(1)' },
  ],
  last_scan_at: null,
  scans_pending: 4,
  scans_failed_24h: 0,
  dependency_track: { enabled: false },
  ai: { enabled: false },
  unscanned_repositories: 2,
}

let spy: Mock<typeof fetch> | undefined

function serve(body: unknown, status = 200): void {
  spy = spyOn(globalThis, 'fetch').mockImplementation((async () =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })) as never)
}

async function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/scanners')
  const w = mount(ScannersSection, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('ScannersSection', () => {
  it('shows each scanner as ready or not ready, in words, with its message and guide', async () => {
    serve(HEALTH)
    const w = await mountPage()
    const rows = w.findAll('tbody tr').map((r) => r.text())
    expect(rows[0]).toContain('OSV-Scanner')
    expect(rows[0]).toContain('Ready')
    expect(rows[1]).toContain('Not ready')
    expect(rows[1]).toContain('binary not found')
    expect(w.find('a[href="https://docs.example.test/trivy"]').exists()).toBe(true)
    expect(w.find('a[href^="javascript"]').exists()).toBe(false)
    expect(w.find('[data-testid="scanner-summary"]').text()).toBe('1 of 3 scanners ready')
    w.unmount()
  })

  it('reports scan activity from the server', async () => {
    serve(HEALTH)
    const w = await mountPage()
    const activity = w.find('[data-testid="scan-activity"]').text()
    expect(activity).toContain('Never')
    expect(activity).toContain('4')
    expect(activity).toContain('Repositories never scanned')
    w.unmount()
  })

  it('warns when no scanner is ready', async () => {
    serve({ ...HEALTH, scanners: [{ name: 'osv', ready: false }] })
    const w = await mountPage()
    expect(w.find('[data-testid="scanner-summary"]').text()).toContain('No scanner is ready')
    w.unmount()
  })

  it('explains that configuration lives in the server config and links to security', async () => {
    serve(HEALTH)
    const w = await mountPage()
    const cfg = w.find('[data-testid="scanner-config"]')
    expect(cfg.text()).toContain('server configuration')
    expect(cfg.find('a').attributes('href')).toBe('/security')
    w.unmount()
  })

  it('offers a retry when the status cannot be read', async () => {
    serve({ error: 'boom' }, 500)
    const w = await mountPage()
    expect(w.text()).toContain('Retry')
    w.unmount()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ScanGatePage from '../ScanGatePage.vue'
import SettingsSectionPage from '../../SettingsSectionPage.vue'
import { useSessionStore, type User } from '../../../../stores/session'

const FEEDS = { enabled: true, epss_threshold: 0.5, feeds: [] }

const finding = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  type: 'vuln',
  repository: 'npm-hosted',
  package_name: `pkg-${id}`,
  package_version: '1.0.0',
  kev_count: 0,
  max_epss: null,
  priority: 'high',
  ...extra,
})

interface Call { method: string; path: string; query: URLSearchParams; body?: string }
let spy: Mock<typeof fetch> | undefined
let calls: Call[] = []
type Handler = (c: Call) => Response

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function serve(handlers: Record<string, Handler>): void {
  calls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const call = { method: init?.method ?? 'GET', path: url.pathname, query: url.searchParams, body: init?.body as string | undefined }
    calls.push(call)
    return (handlers[`${call.method} ${call.path}`] ?? (() => json({ error: 'not found' }, 404)))(call)
  }) as never)
}

/** Findings by status: open ones (candidates) and quarantined ones (blocked). */
function findings(open: unknown[], blocked: unknown[] = [], hasMore = false): Handler {
  return (c) => json({ findings: c.query.get('status') === 'quarantined' ? blocked : open, has_more: hasMore })
}

async function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/scan-gate')
  const w = mount(ScanGatePage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

const dialogButton = (label: string): HTMLButtonElement | undefined =>
  Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')).find((b) => b.textContent?.trim() === label)

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('ScanGatePage', () => {
  it('searches the KEV candidates on load and lists them with their signals', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json(FEEDS),
      'GET /api/v1/security/findings': findings([finding('s1', { kev_count: 2, max_epss: 0.9, priority: 'kev' })]),
    })
    const w = await mountPage()
    const search = calls.find((c) => c.path === '/api/v1/security/findings' && c.query.get('status') === 'open')
    expect(search?.query.get('kev')).toBe('true')
    expect(search?.query.get('type')).toBe('vuln')
    const row = w.get('[data-testid="action-check"]').element.closest('tr')!
    expect(row.textContent).toContain('pkg-s1@1.0.0')
    expect(row.textContent).toContain('npm-hosted')
    w.unmount()
  })

  it('searches by EPSS minimum as a fraction when a percentage is typed', async () => {
    serve({ 'GET /api/v1/security/exploit-feeds': () => json(FEEDS), 'GET /api/v1/security/findings': findings([]) })
    const w = await mountPage()
    await w.get('[data-testid="gate-kev"]').setValue(false)
    await w.get('[data-testid="gate-epss"]').setValue('25')
    calls = []
    await w.get('form').trigger('submit')
    await flushPromises()
    const search = calls.find((c) => c.query.get('status') === 'open')
    expect(search?.query.get('epss_min')).toBe('0.25')
    expect(search?.query.has('kev')).toBe(false)
    w.unmount()
  })

  it('asks for a condition when none is chosen and does not search', async () => {
    serve({ 'GET /api/v1/security/exploit-feeds': () => json(FEEDS), 'GET /api/v1/security/findings': findings([]) })
    const w = await mountPage()
    await w.get('[data-testid="gate-kev"]').setValue(false)
    calls = []
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('[data-testid="gate-nocriteria"]').text()).toContain('Choose at least one condition')
    expect(calls.some((c) => c.query.get('status') === 'open')).toBe(false)
    w.unmount()
  })

  it('rejects an invalid EPSS percentage and disables the search', async () => {
    serve({ 'GET /api/v1/security/exploit-feeds': () => json(FEEDS), 'GET /api/v1/security/findings': findings([]) })
    const w = await mountPage()
    await w.get('[data-testid="gate-epss"]').setValue('150')
    expect(w.get('[data-testid="gate-search"]').attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('shows empty states for no candidates and nothing blocked', async () => {
    serve({ 'GET /api/v1/security/exploit-feeds': () => json(FEEDS), 'GET /api/v1/security/findings': findings([]) })
    const w = await mountPage()
    expect(w.text()).toContain('No open scan meets the gate')
    expect(w.text()).toContain('Nothing is blocked')
    w.unmount()
  })

  it('says so when more scans meet the gate than are listed', async () => {
    serve({ 'GET /api/v1/security/exploit-feeds': () => json(FEEDS), 'GET /api/v1/security/findings': findings([finding('s1')], [], true) })
    const w = await mountPage()
    expect(w.get('[data-testid="gate-truncated"]').text()).toContain('More scans meet the gate')
    w.unmount()
  })

  it('offers a retry when the search fails', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json(FEEDS),
      'GET /api/v1/security/findings': (c) => (c.query.get('status') === 'open' ? json({ error: 'search broke' }, 500) : json({ findings: [] })),
    })
    const w = await mountPage()
    expect(w.text()).toContain('search broke')
    expect(w.text()).toContain('Retry')
    w.unmount()
  })

  it('shows the gate verdict of a passing scan', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json(FEEDS),
      'GET /api/v1/security/findings': findings([finding('s1')]),
      'GET /api/v1/repositories/npm-hosted/vuln/scans/s1/gate': () => json({ pass: true, checked: 3, failed: [], criteria: { kev: true, epss_min: null, min_priority: '' } }),
    })
    const w = await mountPage()
    await w.get('[data-testid="action-check"]').trigger('click')
    await flushPromises()
    const gate = calls.find((c) => c.path.endsWith('/gate'))
    expect(gate?.query.get('kev')).toBe('true')
    expect(w.get('[data-testid="verdict"]').text()).toContain('Pass')
    w.unmount()
  })

  it('shows a failing verdict with the reason of each failed finding', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json(FEEDS),
      'GET /api/v1/security/findings': findings([finding('s1')]),
      'GET /api/v1/repositories/npm-hosted/vuln/scans/s1/gate': () =>
        json({ pass: false, checked: 3, failed: [{ id: 'f1', cve: 'CVE-2024-1', reasons: ['kev'] }], criteria: { kev: true, epss_min: null, min_priority: '' } }),
    })
    const w = await mountPage()
    await w.get('[data-testid="action-check"]').trigger('click')
    await flushPromises()
    const verdict = w.get('[data-testid="verdict"]').text()
    expect(verdict).toContain('Fail')
    expect(verdict).toContain('CVE-2024-1')
    expect(verdict).toContain('in KEV')
    w.unmount()
  })

  it('shows a failed gate check inline', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json(FEEDS),
      'GET /api/v1/security/findings': findings([finding('s1')]),
      'GET /api/v1/repositories/npm-hosted/vuln/scans/s1/gate': () => json({ error: 'gate unavailable' }, 500),
    })
    const w = await mountPage()
    await w.get('[data-testid="action-check"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid="verdict"]').text()).toContain('gate unavailable')
    w.unmount()
  })

  it('blocks a scan only after confirmation, by quarantining it', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json(FEEDS),
      'GET /api/v1/security/findings': findings([finding('s1')]),
      'POST /api/v1/repositories/npm-hosted/vuln/scans/s1/quarantine': () => json({}),
    })
    const w = await mountPage()
    await w.get('[data-testid="action-block"]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain('pkg-s1@1.0.0')
    dialogButton('Block downloads')!.click()
    await flushPromises()
    const put = calls.find((c) => c.method === 'POST')
    expect(put?.path).toBe('/api/v1/repositories/npm-hosted/vuln/scans/s1/quarantine')
    expect(JSON.parse(put!.body!)).toEqual({ quarantined: true })
    w.unmount()
  })

  it('does not block when the confirmation is cancelled', async () => {
    serve({ 'GET /api/v1/security/exploit-feeds': () => json(FEEDS), 'GET /api/v1/security/findings': findings([finding('s1')]) })
    const w = await mountPage()
    await w.get('[data-testid="action-block"]').trigger('click')
    await flushPromises()
    dialogButton('Cancel')!.click()
    await flushPromises()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    w.unmount()
  })

  it('releases a blocked scan after confirmation', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json(FEEDS),
      'GET /api/v1/security/findings': findings([], [finding('q1', { package_name: 'evil' })]),
      'POST /api/v1/repositories/npm-hosted/vuln/scans/q1/quarantine': () => json({}),
    })
    const w = await mountPage()
    expect(w.text()).toContain('evil@1.0.0')
    await w.get('[data-testid="action-release"]').trigger('click')
    await flushPromises()
    dialogButton('Release')!.click()
    await flushPromises()
    const put = calls.find((c) => c.method === 'POST')
    expect(put?.path).toBe('/api/v1/repositories/npm-hosted/vuln/scans/q1/quarantine')
    expect(JSON.parse(put!.body!)).toEqual({ quarantined: false })
    w.unmount()
  })

  it('explains that the exploit feeds are off and offers no criteria', async () => {
    serve({
      'GET /api/v1/security/exploit-feeds': () => json({ ...FEEDS, enabled: false }),
      'GET /api/v1/security/findings': findings([], [finding('q1')]),
    })
    const w = await mountPage()
    expect(w.get('[data-testid="gate-feeds-off"]').text()).toContain('exploit.enabled: true')
    expect(w.find('[data-testid="gate-kev"]').exists()).toBe(false)
    // blocked scans can still be released
    expect(w.find('[data-testid="action-release"]').exists()).toBe(true)
    w.unmount()
  })

  it('is refused for a non-administrator', async () => {
    serve({})
    setActivePinia(createPinia())
    useSessionStore().user = { id: 'u', username: 'dev', is_admin: false } as User
    const r = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/settings', component: { render: () => null } },
        { path: '/settings/:section', name: 'settings-section', component: SettingsSectionPage },
      ],
    })
    await r.push('/settings/scan-gate')
    const w = mount(SettingsSectionPage, { attachTo: document.body, global: { plugins: [r] } })
    await flushPromises()
    expect(w.find('[data-testid="section-forbidden"]').exists()).toBe(true)
    expect(calls.some((c) => c.path.includes('/security/findings'))).toBe(false)
    w.unmount()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import SecurityPage from '../SecurityPage.vue'
import { useSessionStore, type User } from '../../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const zero = { total: 0, critical: 0, high: 0, medium: 0, low: 0, quarantined: 0 }
const summary = (over: Record<string, unknown> = {}, scanned = 3) => ({
  by_type: { vuln: { ...zero }, license: { ...zero }, secret: { ...zero }, ai: { ...zero }, ...over },
  scanned_repositories: scanned,
  total_repositories: 3,
})

const FINDING = {
  id: 'f1', type: 'vuln', repository: 'libs', format: 'npm', package_name: 'left-pad', package_version: '1.0.0',
  artifact_id: 'a1', severity: 'critical', counts: { critical: 1, high: 2, medium: 0, low: 0 },
  title: 'CVE-2026-0001', scanner: 'osv', status: 'open', quarantined: false, scanned_at: '2026-01-01T00:00:00Z',
  kev_count: 0, max_epss: null, priority: 'critical',
}

const EXPLOITED = { ...FINDING, id: 'f2', title: 'CVE-2026-0002', kev_count: 2, max_epss: 0.976, priority: 'kev' }

interface Api { summary?: unknown; findings?: unknown; findingsStatus?: number; health?: unknown; feeds?: unknown }

function mockApi(api: Api = {}) {
  return spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname === '/api/v1/security/findings/summary') return json(api.summary ?? summary({ vuln: { ...zero, total: 1, critical: 1 } }))
    if (url.pathname === '/api/v1/security/findings') {
      if (api.findingsStatus) return json({ error: 'forbidden', code: 'forbidden' }, api.findingsStatus)
      return json(api.findings ?? { findings: [FINDING], has_more: false })
    }
    if (url.pathname === '/api/v1/security/exploit-feeds') return api.feeds ? json(api.feeds) : json({ error: 'nope', code: 'forbidden' }, 403)
    if (url.pathname === '/api/v1/security/health') return json(api.health ?? { scanners: [], ai: { enabled: true }, scans_pending: 0, scans_failed_24h: 0, unscanned_repositories: 0, dependency_track: { enabled: false } })
    if (url.pathname === '/api/v1/repositories') return json({ total: 1, repositories: [{ name: 'libs' }] })
    if (/\/vuln\/scans\/[^/]+$/.test(url.pathname)) return json({ id: 'f1', artifact_id: 'a1', status: 'completed', scanner: 'osv', package_name: 'left-pad', package_version: '1.0.0', results: [] })
    return json({})
  }) as never)
}

async function mountPage(path = '/security', user: Partial<User> = { id: 'a', username: 'root', is_admin: true }) {
  setActivePinia(createPinia())
  useSessionStore().user = user as User
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: SecurityPage }] })
  await router.push(path)
  await router.isReady()
  const w = mount(SecurityPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { w, router }
}

const calls = (spy: ReturnType<typeof spyOn>, path: string) =>
  spy.mock.calls.map((c) => new URL(String(c[0]), 'http://localhost')).filter((u) => u.pathname === path)

afterEach(() => {
  document.body.innerHTML = ''
})

describe('SecurityPage', () => {
  it('lists findings from every repository by default', async () => {
    const spy = mockApi()
    const { w } = await mountPage()
    expect(w.text()).toContain('CVE-2026-0001')
    expect(w.text()).toContain('left-pad@1.0.0')
    expect(calls(spy, '/api/v1/security/findings').at(-1)!.search).toBe('?limit=25')
    w.unmount()
  })

  it('sends the type, severity, status and repository from the URL to the server', async () => {
    const spy = mockApi()
    const { w } = await mountPage('/security?type=secret&severity=high&status=open&repo=libs')
    const q = calls(spy, '/api/v1/security/findings').at(-1)!.searchParams
    expect(Object.fromEntries(q)).toEqual({ type: 'secret', severity: 'high', status: 'open', repo: 'libs', limit: '25' })
    expect(Object.fromEntries(calls(spy, '/api/v1/security/findings/summary').at(-1)!.searchParams)).toEqual({ repo: 'libs' })
    w.unmount()
  })

  it('narrows the list from a select and keeps the filter in the URL', async () => {
    const spy = mockApi()
    const { w, router } = await mountPage()
    const sel = w.get('[data-testid="filter-severity"]')
    ;(sel.element as HTMLSelectElement).value = 'critical'
    await sel.trigger('change')
    await flushPromises()
    expect(router.currentRoute.value.query.severity).toBe('critical')
    expect(calls(spy, '/api/v1/security/findings').at(-1)!.searchParams.get('severity')).toBe('critical')
    w.unmount()
  })

  it('shows the all-clear state when the scope has nothing to triage', async () => {
    mockApi({ findings: { findings: [], has_more: false }, summary: summary() })
    const { w } = await mountPage()
    expect(w.find('[data-testid="all-clear"]').exists()).toBe(true)
    w.unmount()
  })

  it('shows a filtered-empty state, not all clear, when filters match nothing', async () => {
    mockApi({ findings: { findings: [], has_more: false }, summary: summary({ vuln: { ...zero, total: 4, low: 4 } }) })
    const { w } = await mountPage('/security?severity=critical')
    expect(w.find('[data-testid="all-clear"]').exists()).toBe(false)
    expect(w.find('[data-state="filtered"]').exists()).toBe(true)
    w.unmount()
  })

  it('teaches how to scan when nothing was ever scanned', async () => {
    mockApi({ findings: { findings: [], has_more: false }, summary: summary({}, 0) })
    const { w } = await mountPage()
    expect(w.find('[data-testid="all-clear"]').exists()).toBe(false)
    expect(w.find('[data-testid="security-state"]').text()).toContain('scan')
    w.unmount()
  })

  it('shows an error state with retry when the list fails', async () => {
    mockApi({ findingsStatus: 500 })
    const { w } = await mountPage()
    expect(w.find('[data-state="error"]').exists()).toBe(true)
    expect(w.find('[data-testid="all-clear"]').exists()).toBe(false)
    w.unmount()
  })

  it('explains a 403 for readers and points to the package pages', async () => {
    mockApi({ findingsStatus: 403 })
    const { w } = await mountPage('/security', { id: 'u', username: 'ann' })
    expect(w.find('[data-testid="security-forbidden"]').exists()).toBe(true)
    expect(w.get('[data-testid="forbidden-packages"]').attributes('href')).toBe('/packages')
    expect(w.find('[data-testid="finding-f1"]').exists()).toBe(false)
    w.unmount()
  })

  it('tells maintainers they see their own scope and hides admin actions', async () => {
    const spy = mockApi()
    const { w } = await mountPage('/security', { id: 'm', username: 'mia' })
    expect(w.find('[data-testid="scope-note"]').exists()).toBe(true)
    expect(w.find('[data-testid="run-scan"]').exists()).toBe(false)
    expect(calls(spy, '/api/v1/security/health')).toHaveLength(0)
    w.unmount()
  })

  it('explains why AI is off and links to its settings', async () => {
    mockApi({
      findings: { findings: [], has_more: false },
      summary: summary({}, 3),
      health: { scanners: [], ai: { enabled: false }, scans_pending: 0, scans_failed_24h: 0, unscanned_repositories: 0, dependency_track: { enabled: false } },
    })
    const { w } = await mountPage('/security?type=ai')
    const off = w.get('[data-testid="ai-off"]')
    expect(off.find('a').attributes('href')).toBe('/settings/ai')
    w.unmount()
  })

  it('opens the quick look for a finding', async () => {
    mockApi()
    const { w } = await mountPage()
    await w.get('[data-testid="finding-f1"]').trigger('click')
    await flushPromises()
    expect(document.body.querySelector('[data-testid="finding-drawer"]')).not.toBeNull()
    expect(document.body.querySelector('[data-testid="finding-open-artifact"]')?.getAttribute('href')).toContain('?tab=security')
    w.unmount()
  })

  it('exports with the active filters', async () => {
    mockApi()
    const { w } = await mountPage('/security?type=vuln&severity=high')
    expect(w.text()).toMatch(/CSV/)
    expect(w.text()).toMatch(/VEX/)
    w.unmount()
  })
  describe('exploitation signals', () => {
    const lastQuery = (spy: ReturnType<typeof spyOn>) => Object.fromEntries(calls(spy, '/api/v1/security/findings').at(-1)!.searchParams)

    it('shows KEV and EPSS badges on the finding rows and a dash when there are none', async () => {
      mockApi({ findings: { findings: [EXPLOITED, { ...FINDING, kev_count: 0, max_epss: null, priority: 'critical' }], has_more: false } })
      const { w } = await mountPage()
      const cells = w.findAll('td').filter((td) => td.find('[data-testid="kev-badge"], [data-testid="epss-badge"]').exists())
      expect(cells).toHaveLength(1)
      expect(cells[0].get('[data-testid="kev-badge"]').text()).toBe('KEV ×2')
      expect(cells[0].get('[data-testid="epss-badge"]').text()).toBe('EPSS 97.6%')
      w.unmount()
    })

    it('hides the exploitation column for types that cannot be exploited', async () => {
      mockApi()
      const { w } = await mountPage('/security?type=license')
      expect(w.text()).not.toContain('Exploitation')
      w.unmount()
    })

    it('sorts by priority from the control and keeps the order in the URL', async () => {
      const spy = mockApi()
      const { w, router } = await mountPage()
      const sort = w.get('[data-testid="sort-by"]')
      const [severity, priority] = sort.findAll('button')
      expect(severity.attributes('aria-pressed')).toBe('true')
      await priority.trigger('click')
      await flushPromises()
      expect(router.currentRoute.value.query.sort).toBe('priority')
      expect(lastQuery(spy).sort).toBe('priority')
      expect(w.get('[data-testid="sort-by"]').findAll('button')[1].attributes('aria-pressed')).toBe('true')
      await w.get('[data-testid="sort-by"]').findAll('button')[0].trigger('click')
      await flushPromises()
      expect(router.currentRoute.value.query.sort).toBeUndefined()
      expect(lastQuery(spy).sort).toBeUndefined()
      w.unmount()
    })

    it('drops the cursor when the sort changes, so a cursor never meets another order', async () => {
      const spy = mockApi({ findings: { findings: [FINDING], has_more: true, next_cursor: 'c2' } })
      const { w } = await mountPage()
      await w.get('[data-testid="sort-by"]').findAll('button')[1].trigger('click')
      await flushPromises()
      const q = calls(spy, '/api/v1/security/findings').at(-1)!.searchParams
      expect(q.get('sort')).toBe('priority')
      expect(q.has('cursor')).toBe(false)
      w.unmount()
    })

    it('sends the KEV, EPSS and priority filters to the server and the URL', async () => {
      const spy = mockApi()
      const { w, router } = await mountPage()
      const kev = w.get('[data-testid="filter-kev"]')
      ;(kev.element as HTMLInputElement).checked = true
      await kev.trigger('change')
      await flushPromises()
      expect(lastQuery(spy).kev).toBe('true')
      expect(router.currentRoute.value.query.kev).toBe('1')

      const epss = w.get('[data-testid="filter-epss"]')
      ;(epss.element as HTMLSelectElement).value = '0.1'
      await epss.trigger('change')
      await flushPromises()
      expect(lastQuery(spy).epss_min).toBe('0.1')
      expect(router.currentRoute.value.query.epss_min).toBe('0.1')

      const priority = w.get('[data-testid="filter-priority"]')
      ;(priority.element as HTMLSelectElement).value = 'epss'
      await priority.trigger('change')
      await flushPromises()
      expect(lastQuery(spy)).toMatchObject({ kev: 'true', epss_min: '0.1', priority: 'epss', limit: '25' })
      w.unmount()
    })

    it('offers 1%, 10% and 50% as EPSS thresholds', async () => {
      mockApi()
      const { w } = await mountPage()
      const options = w.get('[data-testid="filter-epss"]').findAll('option').map((o) => [o.attributes('value'), o.text()])
      expect(options).toEqual([['', 'Any EPSS'], ['0.01', 'EPSS at least 1%'], ['0.1', 'EPSS at least 10%'], ['0.5', 'EPSS at least 50%']])
      w.unmount()
    })

    it('restores sort and filters from the URL and clears the filters but keeps the order', async () => {
      const spy = mockApi()
      const { w, router } = await mountPage('/security?sort=priority&kev=1&epss_min=0.5&priority=kev')
      expect(lastQuery(spy)).toEqual({ sort: 'priority', kev: 'true', epss_min: '0.5', priority: 'kev', limit: '25' })
      expect((w.get('[data-testid="filter-kev"]').element as HTMLInputElement).checked).toBe(true)
      expect((w.get('[data-testid="filter-epss"]').element as HTMLSelectElement).value).toBe('0.5')
      await w.get('[role="search"] button').trigger('click')
      await flushPromises()
      expect(router.currentRoute.value.query).toEqual({ sort: 'priority' })
      expect(lastQuery(spy)).toEqual({ sort: 'priority', limit: '25' })
      w.unmount()
    })

    it('exports with the exploitation filters', async () => {
      const spy = mockApi()
      const { w } = await mountPage('/security?type=vuln&sort=priority&kev=1')
      await w.get('[data-testid="export-csv"]').trigger('click')
      await flushPromises()
      expect(Object.fromEntries(calls(spy, '/api/v1/security/findings/export').at(-1)!.searchParams)).toEqual({
        type: 'vuln', sort: 'priority', kev: 'true', format: 'csv',
      })
      w.unmount()
    })

    it('shows how fresh the exploitation data is, and nothing when the endpoint refuses', async () => {
      const day = new Date(Date.now() - 86_400_000).toISOString()
      const f = (name: string) => ({ name, source_url: '', version: '1', published_at: day, entry_count: 1, imported_at: day, checked_at: day, last_error: '' })
      mockApi({ feeds: { feeds: [f('kev'), f('epss')], epss_threshold: 0.1, enabled: true } })
      const first = await mountPage()
      expect(first.w.get('[data-testid="exploit-feeds"]').text()).toContain('Exploitation data: KEV')
      first.w.unmount()
      mockApi()
      const second = await mountPage()
      expect(second.w.find('[data-testid="exploit-feeds"]').exists()).toBe(false)
      second.w.unmount()
    })

    it('shows priority and exploitation signals in the finding drawer', async () => {
      mockApi({ findings: { findings: [EXPLOITED], has_more: false } })
      const { w } = await mountPage()
      await w.get('[data-testid="finding-f2"]').trigger('click')
      await flushPromises()
      const drawer = document.body.querySelector('[data-testid="finding-drawer"]')!
      expect(drawer.querySelector('[data-testid="finding-priority"] [data-priority="kev"]')).not.toBeNull()
      expect(drawer.querySelector('[data-testid="finding-exploit"] [data-testid="kev-badge"]')).not.toBeNull()
      expect(drawer.querySelector('[data-testid="finding-exploit"] [data-testid="epss-badge"]')?.textContent?.trim()).toBe('EPSS 97.6%')
      w.unmount()
    })
  })
})

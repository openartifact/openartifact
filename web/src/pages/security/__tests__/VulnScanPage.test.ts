// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import VulnScanPage from '../VulnScanPage.vue'
import { REPO, makeRouter, type RecordedCall } from './scanPageHarness'

const base = {
  repo_id: 'r1',
  repository_name: REPO,
  repository_format: 'npm',
  artifact_id: 'art-1',
  package_version: '1.0.0',
  ecosystem: 'npm',
  quarantined: false,
  created_at: '2026-10-20T10:00:00Z',
  updated_at: '2026-10-20T10:00:00Z',
  scanned_at: '2026-10-20T10:00:00Z',
  vuln_count: 0,
  critical_count: 0,
  high_count: 0,
  medium_count: 0,
  low_count: 0,
  scan_error: null,
  failed_scanners: [],
}

const FAILED = {
  ...base,
  id: 'failed-scan',
  package_name: 'pkg-failed',
  scanner: 'osv,trivy',
  scan_status: 'failed',
  scan_error: 'osv: scan timed out; trivy: scanner is not available on the server',
  failed_scanners: ['osv', 'trivy'],
}
const PARTIAL = {
  ...base,
  id: 'partial-scan',
  package_name: 'pkg-partial',
  scanner: 'osv,trivy',
  scan_status: 'partial',
  scan_error: 'trivy: scanner is not available on the server',
  failed_scanners: ['trivy'],
  vuln_count: 2,
  critical_count: 1,
  high_count: 1,
}
const CLEAN = { ...base, id: 'clean-scan', package_name: 'pkg-clean', scanner: 'osv', scan_status: 'completed' }
const OTHER_REPO = {
  ...base,
  id: 'other-scan',
  repository_name: 'other',
  repository_format: 'pypi',
  package_name: 'pkg-other',
  scanner: 'grype',
  scan_status: 'completed',
  quarantined: true,
  vuln_count: 1,
  medium_count: 1,
}

const SUMMARY = {
  total: 4,
  completed: 2,
  partial: 1,
  failed: 1,
  scanning: 0,
  quarantined: 1,
  severity_at_least: { critical: 1, high: 1, medium: 2, low: 2 },
}

type Row = typeof CLEAN
type Page = { scans: Row[]; has_more: boolean; next_cursor?: string }

/** Plays the server's filter semantics over a fixed row set. */
function serverFilter(rows: Row[], q: URLSearchParams): Row[] {
  const sev: Record<string, (r: Row) => boolean> = {
    critical: (r) => r.critical_count > 0,
    high: (r) => r.critical_count + r.high_count > 0,
    medium: (r) => r.critical_count + r.high_count + r.medium_count > 0,
    low: (r) => r.vuln_count > 0,
  }
  return rows.filter(
    (r) =>
      (!q.get('repo') || r.repository_name === q.get('repo')) &&
      (!q.get('status') || r.scan_status === q.get('status')) &&
      (!q.get('severity') || sev[q.get('severity')!]!(r)) &&
      (!q.get('scanner') || r.scanner.split(',').includes(q.get('scanner')!)) &&
      (!q.get('quarantined') || String(r.quarantined) === q.get('quarantined')),
  )
}

interface ApiOptions {
  rows?: Row[]
  /** Answers GET /api/v1/vuln/scans; defaults to the server filter over `rows`. */
  list?: (call: RecordedCall) => Page | Promise<Page>
  /** Answers any other call that is not routed below. */
  other?: (call: RecordedCall) => unknown | Promise<unknown>
  scanners?: unknown
}

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

function mockApi(opts: ApiOptions = {}) {
  const rows = opts.rows ?? [FAILED, PARTIAL, CLEAN, OTHER_REPO]
  const calls: RecordedCall[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: string, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const call: RecordedCall = {
      method: (init?.method ?? 'GET').toUpperCase(),
      path: url.pathname,
      search: url.searchParams,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    }
    calls.push(call)
    const { path, method } = call
    if (path === '/api/v1/repositories') {
      return json({ total: 2, repositories: [{ name: REPO }, { name: 'other' }] })
    }
    if (path === '/api/v1/vuln/scans/summary') return json(SUMMARY)
    if (path === '/api/v1/vuln/scans' && method === 'GET') {
      return json(await (opts.list ? opts.list(call) : { scans: serverFilter(rows, call.search), has_more: false }))
    }
    const pkgs = /^\/api\/v1\/repositories\/([^/]+)\/packages$/.exec(path)
    if (pkgs) {
      const repo = pkgs[1]
      return json({
        total: 1,
        packages: [
          repo === REPO
            ? { id: 'art-1', name: 'left-pad', version: '1.0.0', format: 'npm' }
            : { id: 'art-9', name: 'requests', version: '2.0.0', format: 'pypi' },
        ],
      })
    }
    if (path.endsWith('/vuln/scanners')) return json({ scanners: opts.scanners })
    return json(opts.other ? await opts.other(call) : {})
  }) as never)
  return calls
}

const listCalls = (calls: RecordedCall[]) => calls.filter((c) => c.path === '/api/v1/vuln/scans' && c.method === 'GET')
const summaryCalls = (calls: RecordedCall[]) => calls.filter((c) => c.path === '/api/v1/vuln/scans/summary')

// PageHeader reads the router; the page tests only need its action slot.
const PageHeaderStub = { template: '<div><slot name="actions" /></div>' }

async function mountPage(path = '/security/vulns', opts: ApiOptions = {}) {
  const calls = mockApi(opts)
  const router = await makeRouter(path)
  const wrapper = mount(VulnScanPage, {
    global: { plugins: [createPinia(), router], stubs: { RouterLink: true, PageHeader: PageHeaderStub } },
    attachTo: document.body,
  })
  await flushPromises()
  return { wrapper, calls, router }
}

type Wrapper = Awaited<ReturnType<typeof mountPage>>['wrapper']

const rowOf = (wrapper: Wrapper, pkg: string) => wrapper.findAll('tbody tr').find((r) => r.text().includes(pkg))!
const packagesShown = (wrapper: Wrapper) =>
  wrapper.findAll('tbody tr').map((r) => /pkg-[a-z0-9]+/.exec(r.text())?.[0])
const select = (wrapper: Wrapper, name: string) => wrapper.get(`[data-testid="filter-${name}"]`)
const card = (wrapper: Wrapper, name: string) => wrapper.get(`[data-testid="summary-${name}"]`)
const body = () => document.body
const button = (label: string) =>
  Array.from(body().querySelectorAll('button')).find((b) => b.textContent?.trim() === label) as HTMLButtonElement

describe('VulnScanPage overview', () => {
  it('lists scans across every repository as returned, with repository, package@version, scanners and quarantine', async () => {
    const { wrapper, calls } = await mountPage()
    expect(listCalls(calls)).toHaveLength(1)
    expect(listCalls(calls)[0]!.search.has('repo')).toBe(false)
    expect(packagesShown(wrapper)).toEqual(['pkg-failed', 'pkg-partial', 'pkg-clean', 'pkg-other'])

    const other = rowOf(wrapper, 'pkg-other')
    expect(other.get('[data-testid="scan-repo"]').text()).toBe('other')
    expect(other.text()).toContain('PyPI')
    expect(other.text()).toContain('pkg-other@1.0.0')
    expect(other.text()).toContain('grype')
    expect(other.find('[data-testid="scan-quarantined"]').exists()).toBe(true)
    expect(rowOf(wrapper, 'pkg-clean').find('[data-testid="scan-quarantined"]').exists()).toBe(false)
    expect(rowOf(wrapper, 'pkg-failed').text()).toContain('osv')
    expect(rowOf(wrapper, 'pkg-failed').text()).toContain('trivy')
  })

  it('shows the summary counts and keeps the scan button usable without a repository', async () => {
    const { wrapper } = await mountPage()
    expect(wrapper.get('[data-testid="summary-total"]').text()).toContain('4')
    expect(card(wrapper, 'critical').text()).toContain('1')
    expect(card(wrapper, 'low').text()).toContain('2')
    expect(card(wrapper, 'quarantined').text()).toContain('1')
    const scan = wrapper.findAll('button').find((b) => b.text().includes('Scan package'))!
    expect(scan.attributes('disabled')).toBeUndefined()
  })
})

describe('VulnScanPage scan rows', () => {
  it('shows a failed scan as Failed with its error and failed scanners, never as clean or zero counts', async () => {
    const { wrapper } = await mountPage()
    const row = rowOf(wrapper, 'pkg-failed')
    expect(row.get('[data-testid="scan-status"]').text()).toBe('Failed')
    expect(row.get('[data-testid="scan-status"]').classes()).toContain('text-danger')
    expect(row.get('[data-testid="scan-error"]').text()).toBe(FAILED.scan_error)
    expect(row.get('[data-testid="failed-scanners"]').text()).toContain('osv, trivy')
    expect(row.get('[data-counts]').attributes('data-counts')).toBe('notScanned')
    expect(row.text()).toContain('Not scanned')
    expect(row.text()).not.toContain('Clean')
  })

  it('shows a partial scan as an amber Partial badge with its findings and the failed scanner', async () => {
    const { wrapper } = await mountPage()
    const row = rowOf(wrapper, 'pkg-partial')
    expect(row.get('[data-testid="scan-status"]').text()).toBe('Partial')
    expect(row.get('[data-testid="scan-status"]').classes()).toContain('text-warning')
    expect(row.get('[data-testid="failed-scanners"]').text()).toContain('trivy')
    expect(row.text()).toContain('1C')
    expect(row.text()).toContain('1H')
    expect(row.text()).not.toContain('Not scanned')
  })

  it('still reports a completed scan without findings as Clean', async () => {
    const { wrapper } = await mountPage()
    const row = rowOf(wrapper, 'pkg-clean')
    expect(row.get('[data-testid="scan-status"]').text()).toBe('Completed')
    expect(row.text()).toContain('Clean')
    expect(row.find('[data-testid="scan-error"]').exists()).toBe(false)
  })
})

describe('VulnScanPage URL filters', () => {
  it('applies the initial query and ignores invalid values', async () => {
    const { wrapper, calls } = await mountPage(
      '/security/vulns?repo=main&severity=high&status=bogus&scanner=nessus&quarantined=maybe',
    )
    const list = listCalls(calls)
    expect(list).toHaveLength(1)
    expect(Object.fromEntries(list[0]!.search)).toEqual({ repo: 'main', severity: 'high', limit: '25' })
    expect((select(wrapper, 'repo').element as HTMLSelectElement).value).toBe('main')
    expect((select(wrapper, 'severity').element as HTMLSelectElement).value).toBe('high')
    expect((select(wrapper, 'status').element as HTMLSelectElement).value).toBe('')
    expect((select(wrapper, 'scanner').element as HTMLSelectElement).value).toBe('')
    expect((select(wrapper, 'quarantined').element as HTMLSelectElement).value).toBe('')
  })

  it('drops a repository filter that does not exist once the repositories are known', async () => {
    const { router, calls } = await mountPage('/security/vulns?repo=ghost')
    expect(router.currentRoute.value.query.repo).toBeUndefined()
    expect(listCalls(calls).at(-1)!.search.has('repo')).toBe(false)
  })

  it('writes a changed filter to the URL with replace, keeping other parameters and reloading', async () => {
    const { wrapper, calls, router } = await mountPage('/security/vulns?keep=1')
    const position = router.options.history.state.position
    await select(wrapper, 'severity').setValue('critical')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({ keep: '1', severity: 'critical' })
    // replace rewrites the current history entry instead of adding one.
    expect(router.options.history.state.position).toBe(position)
    expect(listCalls(calls).at(-1)!.search.get('severity')).toBe('critical')
    expect(packagesShown(wrapper)).toEqual(['pkg-partial'])

    await select(wrapper, 'severity').setValue('')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ keep: '1' })
  })

  it('re-applies the filters when the route changes (back/forward or a manual edit)', async () => {
    const { wrapper, calls, router } = await mountPage()
    await router.replace({ query: { status: 'failed', quarantined: 'false' } })
    await flushPromises()

    const last = listCalls(calls).at(-1)!
    expect(last.search.get('status')).toBe('failed')
    expect(last.search.get('quarantined')).toBe('false')
    expect((select(wrapper, 'status').element as HTMLSelectElement).value).toBe('failed')
    expect(packagesShown(wrapper)).toEqual(['pkg-failed'])
  })

  it('resets to the first page when a filter changes', async () => {
    const pages: Record<string, Page> = {
      '': { scans: [FAILED, PARTIAL], has_more: true, next_cursor: 'c1' },
      c1: { scans: [CLEAN], has_more: false },
    }
    const { wrapper, calls } = await mountPage('/security/vulns', {
      list: (call) => (call.search.get('status') === 'completed' ? { scans: [OTHER_REPO], has_more: false } : pages[call.search.get('cursor') ?? '']!),
    })
    await wrapper.get('[data-testid="load-more"]').trigger('click')
    await flushPromises()
    expect(packagesShown(wrapper)).toEqual(['pkg-failed', 'pkg-partial', 'pkg-clean'])

    await select(wrapper, 'status').setValue('completed')
    await flushPromises()
    const last = listCalls(calls).at(-1)!
    expect(last.search.has('cursor')).toBe(false)
    expect(packagesShown(wrapper)).toEqual(['pkg-other'])
  })
})

describe('VulnScanPage load more', () => {
  it('appends the next page with next_cursor until has_more is false', async () => {
    const { wrapper, calls } = await mountPage('/security/vulns', {
      list: (call) =>
        call.search.get('cursor') === 'cur-1'
          ? { scans: [CLEAN], has_more: false }
          : { scans: [FAILED, PARTIAL], has_more: true, next_cursor: 'cur-1' },
    })
    expect(packagesShown(wrapper)).toEqual(['pkg-failed', 'pkg-partial'])

    await wrapper.get('[data-testid="load-more"]').trigger('click')
    await flushPromises()

    expect(listCalls(calls).at(-1)!.search.get('cursor')).toBe('cur-1')
    expect(packagesShown(wrapper)).toEqual(['pkg-failed', 'pkg-partial', 'pkg-clean'])
    expect(wrapper.find('[data-testid="load-more"]').exists()).toBe(false)
  })
})

describe('VulnScanPage stale responses', () => {
  it('discards a slow older list response that arrives after a newer one', async () => {
    let releaseOld: (page: Page) => void = () => {}
    const old = new Promise<Page>((resolve) => (releaseOld = resolve))
    const { wrapper } = await mountPage('/security/vulns?status=failed', {
      list: (call) => (call.search.get('status') === 'failed' ? old : { scans: [CLEAN], has_more: false }),
    })
    await select(wrapper, 'status').setValue('completed')
    await flushPromises()
    expect(packagesShown(wrapper)).toEqual(['pkg-clean'])

    releaseOld({ scans: [FAILED], has_more: false })
    await flushPromises()
    expect(packagesShown(wrapper)).toEqual(['pkg-clean'])
  })

  it('discards a slow summary for a previous repository filter', async () => {
    let releaseOld: (r: Response) => void = () => {}
    const old = new Promise<Response>((resolve) => (releaseOld = resolve))
    const { wrapper } = await mountPage()
    const real = globalThis.fetch
    spyOn(globalThis, 'fetch').mockImplementation((async (input: string, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/api/v1/vuln/scans/summary' && url.searchParams.get('repo') === 'main') return old
      if (url.pathname === '/api/v1/vuln/scans/summary') return json({ ...SUMMARY, total: 77 })
      return real(input, init)
    }) as never)
    await select(wrapper, 'repo').setValue('main')
    await flushPromises()
    await select(wrapper, 'repo').setValue('other')
    await flushPromises()
    expect(wrapper.get('[data-testid="summary-total"]').text()).toContain('77')

    releaseOld(json({ ...SUMMARY, total: 5 }))
    await flushPromises()
    expect(wrapper.get('[data-testid="summary-total"]').text()).toContain('77')
  })

  it('discards a slow older scan detail when another scan was opened', async () => {
    let releaseOld: (detail: unknown) => void = () => {}
    const old = new Promise((resolve) => (releaseOld = resolve))
    const detail = (id: string, vuln: string) => ({
      ...base,
      id,
      package_name: id,
      scanner: 'osv',
      scan_status: 'completed',
      vulnerabilities: [{ id: vuln, severity: 'HIGH', summary: 's' }],
    })
    const { wrapper } = await mountPage('/security/vulns', {
      other: (c) => (c.path.endsWith('/vuln/scans/failed-scan') ? old : detail('clean-scan', 'CVE-NEW')),
    })

    await rowOf(wrapper, 'pkg-failed').get('[data-testid="scan-details"]').trigger('click')
    await rowOf(wrapper, 'pkg-clean').get('[data-testid="scan-details"]').trigger('click')
    await flushPromises()
    expect(body().textContent).toContain('CVE-NEW')

    releaseOld(detail('failed-scan', 'CVE-OLD'))
    await flushPromises()
    expect(body().textContent).toContain('CVE-NEW')
    expect(body().textContent).not.toContain('CVE-OLD')
  })
})

describe('VulnScanPage summary cards', () => {
  it('applies the matching filter on click and removes it on the second click', async () => {
    const { wrapper, calls, router } = await mountPage()
    expect(card(wrapper, 'failed').attributes('aria-pressed')).toBe('false')

    await card(wrapper, 'failed').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.status).toBe('failed')
    expect(card(wrapper, 'failed').attributes('aria-pressed')).toBe('true')
    expect(listCalls(calls).at(-1)!.search.get('status')).toBe('failed')
    expect(packagesShown(wrapper)).toEqual(['pkg-failed'])

    await card(wrapper, 'failed').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.status).toBeUndefined()
    expect(card(wrapper, 'failed').attributes('aria-pressed')).toBe('false')
    expect(listCalls(calls).at(-1)!.search.has('status')).toBe(false)

    await card(wrapper, 'high').trigger('click')
    await card(wrapper, 'quarantined').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ severity: 'high', quarantined: 'true' })
  })

  it('requests the summary for the repository filter only', async () => {
    const { wrapper, calls } = await mountPage()
    await select(wrapper, 'repo').setValue('other')
    await flushPromises()
    await card(wrapper, 'critical').trigger('click')
    await flushPromises()

    const summaries = summaryCalls(calls)
    expect(summaries.at(-1)!.search.get('repo')).toBe('other')
    expect(Array.from(summaries.at(-1)!.search.keys())).toEqual(['repo'])
    // The severity filter changed the list, not the summary.
    expect(summaries).toHaveLength(2)
  })
})

describe('VulnScanPage empty and error states', () => {
  it('says there are no scans yet when nothing is filtered', async () => {
    const { wrapper } = await mountPage('/security/vulns', { rows: [] })
    expect(wrapper.get('[data-testid="scans-empty"]').text()).toContain('No scans yet')
    expect(wrapper.find('[data-testid="empty-clear-filters"]').exists()).toBe(false)
  })

  it('distinguishes a filter without matches and clears the filters from the empty state', async () => {
    const { wrapper, router } = await mountPage('/security/vulns?status=scanning')
    const empty = wrapper.get('[data-testid="scans-empty"]')
    expect(empty.text()).toContain('No scans match these filters')
    expect(empty.text()).not.toContain('No scans yet')

    await wrapper.get('[data-testid="empty-clear-filters"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
    expect(packagesShown(wrapper)).toHaveLength(4)
  })

  it('never shows the empty state while the first page is still loading', async () => {
    let release: (page: Page) => void = () => {}
    const pending = new Promise<Page>((resolve) => (release = resolve))
    const { wrapper } = await mountPage('/security/vulns', { list: () => pending })
    expect(wrapper.find('[data-testid="scans-empty"]').exists()).toBe(false)
    expect(wrapper.find('[role="status"]').exists()).toBe(true)
    release({ scans: [CLEAN], has_more: false })
    await flushPromises()
    expect(packagesShown(wrapper)).toEqual(['pkg-clean'])
  })

  it('shows the error with a retry that reloads the list', async () => {
    let fail = true
    const { wrapper } = await mountPage('/security/vulns', {
      list: () => {
        if (fail) throw new Error('boom')
        return { scans: [CLEAN], has_more: false }
      },
    })
    expect(wrapper.get('[role="alert"]').text()).toContain('boom')
    fail = false
    await wrapper.findAll('button').find((b) => b.text() === 'Retry')!.trigger('click')
    await flushPromises()
    expect(packagesShown(wrapper)).toEqual(['pkg-clean'])
  })
})

describe('VulnScanPage row actions', () => {
  it('quarantines through the repository of the row', async () => {
    const { wrapper, calls } = await mountPage('/security/vulns', {
      other: (c) => (c.method === 'POST' ? { ...OTHER_REPO, quarantined: false } : {}),
    })
    await rowOf(wrapper, 'pkg-other').findAll('button').find((b) => b.text() === 'Release')!.trigger('click')
    await flushPromises()

    const post = calls.find((c) => c.method === 'POST')!
    expect(post.path).toBe('/api/v1/repositories/other/vuln/scans/other-scan/quarantine')
    expect(post.body).toEqual({ quarantined: false })
    expect(rowOf(wrapper, 'pkg-other').find('[data-testid="scan-quarantined"]').exists()).toBe(false)
  })

  it('loads the full findings from the scan repository and deletes through it', async () => {
    const { wrapper, calls } = await mountPage('/security/vulns', {
      other: (c) =>
        c.method === 'GET' && c.path === '/api/v1/repositories/other/vuln/scans/other-scan'
          ? { ...OTHER_REPO, vulnerabilities: [{ id: 'GHSA-1', severity: 'MEDIUM', cvss: 5.5, fixed_in: '2.1.0', summary: 'bad things' }] }
          : {},
    })
    await rowOf(wrapper, 'pkg-other').get('[data-testid="scan-details"]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.method === 'GET' && c.path === '/api/v1/repositories/other/vuln/scans/other-scan')).toBe(true)
    const detail = body().querySelector('[data-testid="scan-detail"]')!
    expect(detail.textContent).toContain('GHSA-1')
    expect(detail.textContent).toContain('2.1.0')
    button('Cancel')?.click()

    await rowOf(wrapper, 'pkg-other').get('[data-testid="scan-delete"]').trigger('click')
    await flushPromises()
    const confirm = Array.from(body().querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Delete').at(-1)!
    confirm.click()
    await flushPromises()

    expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/v1/repositories/other/vuln/scans/other-scan')).toBe(true)
    expect(packagesShown(wrapper)).not.toContain('pkg-other')
  })
})

describe('VulnScanPage scan form', () => {
  const openDrawer = async (wrapper: Wrapper) => {
    await wrapper.findAll('button').find((b) => b.text().includes('Scan package'))!.trigger('click')
    await flushPromises()
  }
  const box = (name: string) => body().querySelector(`[data-testid="scanner-${name}"]`) as HTMLInputElement
  const pick = async (id: string, value: string) => {
    const el = body().querySelector(id) as HTMLSelectElement
    el.value = value
    el.dispatchEvent(new Event('change', { bubbles: true }))
    await flushPromises()
  }

  it('scans from the overview through the repository picked in the drawer, then refreshes list and summary', async () => {
    const { wrapper, calls } = await mountPage()
    const listsBefore = listCalls(calls).length
    const summariesBefore = summaryCalls(calls).length
    await openDrawer(wrapper)

    await pick('#vuln-repo', 'other')
    await pick('#vuln-pkg', 'art-9')
    const start = button('Start scan')
    expect(start.disabled).toBe(false)
    start.click()
    await flushPromises()

    const post = calls.find((c) => c.method === 'POST')!
    expect(post.path).toBe('/api/v1/repositories/other/vuln/scan')
    expect(post.body).toEqual({
      artifact_id: 'art-9',
      package_name: 'requests',
      package_version: '2.0.0',
      ecosystem: 'pypi',
      scanners: ['osv'],
    })
    expect(listCalls(calls).length).toBe(listsBefore + 1)
    expect(summaryCalls(calls).length).toBe(summariesBefore + 1)
  })

  it('prefills the drawer repository from the repository filter and sends the selected scanners', async () => {
    const { wrapper, calls } = await mountPage('/security/vulns?repo=main')
    await openDrawer(wrapper)
    expect((body().querySelector('#vuln-repo') as HTMLSelectElement).value).toBe('main')
    expect(box('osv').checked).toBe(true)
    expect(box('trivy').checked).toBe(false)

    // v-model on a checkbox array reads the rendered value: re-render between clicks.
    box('trivy').click()
    await flushPromises()
    box('grype').click()
    await flushPromises()
    await pick('#vuln-pkg', 'art-1')
    button('Start scan').click()
    await flushPromises()

    const post = calls.find((c) => c.method === 'POST' && c.path === `/api/v1/repositories/${REPO}/vuln/scan`)!
    expect(post.body).toEqual({
      artifact_id: 'art-1',
      package_name: 'left-pad',
      package_version: '1.0.0',
      ecosystem: 'npm',
      scanners: ['osv', 'trivy', 'grype'],
    })
  })

  it('keeps every scanner selectable until a repository is chosen in the drawer', async () => {
    const { wrapper, calls } = await mountPage()
    await openDrawer(wrapper)
    expect(calls.some((c) => c.path.endsWith('/vuln/scanners'))).toBe(false)
    expect(box('trivy').disabled).toBe(false)
    expect(button('Start scan').disabled).toBe(true)
  })
})

describe('VulnScanPage scanner readiness', () => {
  const SCANNERS = [
    { name: 'osv', available: true, ready: true, db_state: null, db_updated_at: null, code: null, message: null, docs_url: null },
    {
      name: 'trivy',
      available: false,
      ready: false,
      db_state: null,
      db_updated_at: null,
      code: 'scanner_unavailable',
      message: "trivy was not found on the server's PATH: install it or set OPENARTIFACT_TRIVY_PATH.",
      docs_url: 'https://example.com/docs/scanning',
    },
    {
      name: 'grype',
      available: true,
      ready: false,
      db_state: 'updating',
      db_updated_at: null,
      code: 'scanner_db_unavailable',
      message: 'The vulnerability database is being downloaded: retry in a few minutes.',
      docs_url: 'javascript:alert(1)',
    },
  ]

  async function openForm(scanners: unknown) {
    const ctx = await mountPage(`/security/vulns?repo=${REPO}`, { scanners })
    await ctx.wrapper.findAll('button').find((b) => b.text().includes('Scan package'))!.trigger('click')
    await flushPromises()
    return ctx
  }

  const box = (name: string) => document.body.querySelector(`[data-testid="scanner-${name}"]`) as HTMLInputElement

  it('disables scanners that are not ready and explains why, without following a non-https link', async () => {
    const { calls } = await openForm(SCANNERS)

    expect(calls.some((c) => c.method === 'GET' && c.path === `/api/v1/repositories/${REPO}/vuln/scanners`)).toBe(true)
    expect(box('osv').disabled).toBe(false)
    expect(box('trivy').disabled).toBe(true)
    expect(box('grype').disabled).toBe(true)

    const trivyNote = document.body.querySelector('[data-testid="scanner-note-trivy"]')!
    expect(trivyNote.textContent).toContain('OPENARTIFACT_TRIVY_PATH')
    expect(trivyNote.querySelector('a')!.getAttribute('href')).toBe('https://example.com/docs/scanning')

    const grypeNote = document.body.querySelector('[data-testid="scanner-note-grype"]')!
    expect(grypeNote.textContent).toContain('being downloaded')
    expect(grypeNote.querySelector('a')).toBeNull()
  })

  it('says whether a scanner is installing or its installation failed', async () => {
    await openForm([
      { name: 'osv', available: true, ready: true, db_state: null, db_updated_at: null, install_state: null, code: null, message: null, docs_url: null },
      {
        name: 'trivy',
        available: false,
        ready: false,
        db_state: null,
        db_updated_at: null,
        install_state: 'installing',
        code: 'scanner_installing',
        message: 'The pinned trivy release is being downloaded: retry in a minute or two.',
        docs_url: null,
      },
      {
        name: 'grype',
        available: false,
        ready: false,
        db_state: null,
        db_updated_at: null,
        install_state: 'failed',
        code: 'scanner_install_noexec',
        message: 'The downloaded grype binary cannot be executed.',
        docs_url: null,
      },
    ])

    expect(box('trivy').disabled).toBe(true)
    expect(box('grype').disabled).toBe(true)
    expect(document.body.querySelector('[data-testid="scanner-install-trivy"]')!.textContent).toContain('Installing')
    expect(document.body.querySelector('[data-testid="scanner-install-grype"]')!.textContent).toContain('Installation failed')
    expect(document.body.querySelector('[data-testid="scanner-note-grype"]')!.textContent).toContain('cannot be executed')
  })

  it('keeps every scanner selectable when the readiness list is unavailable', async () => {
    await openForm(undefined)

    expect(box('trivy').disabled).toBe(false)
    expect(box('grype').disabled).toBe(false)
    expect(document.body.querySelector('[data-testid="scanner-unavailable"]')).toBeNull()
  })
})

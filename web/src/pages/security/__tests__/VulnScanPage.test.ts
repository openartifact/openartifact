// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import VulnScanPage from '../VulnScanPage.vue'
import { REPO, mockSecurityApi, selectRepo } from './scanPageHarness'

const base = {
  repo_id: 'r1',
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

function rowOf(wrapper: ReturnType<typeof mount>, pkg: string) {
  return wrapper.findAll('tbody tr').find((r) => r.text().includes(pkg))!
}

// PageHeader reads the router; the page tests only need its action slot.
const PageHeaderStub = { template: '<div><slot name="actions" /></div>' }

async function mountPage() {
  const calls = mockSecurityApi(`/api/v1/repositories/${REPO}/vuln/scans`, [FAILED, PARTIAL, CLEAN])
  const wrapper = mount(VulnScanPage, {
    global: { plugins: [createPinia()], stubs: { RouterLink: true, PageHeader: PageHeaderStub } },
    attachTo: document.body,
  })
  await selectRepo(wrapper)
  return { wrapper, calls }
}

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

  it('filters by the partial status', async () => {
    const { wrapper } = await mountPage()
    await wrapper.findAll('button').find((b) => b.text() === 'Partial')!.trigger('click')
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('pkg-partial')
  })
})

describe('VulnScanPage scan form', () => {
  it('sends the selected scanners (default osv) in the scanners array', async () => {
    const { wrapper, calls } = await mountPage()
    await wrapper.findAll('button').find((b) => b.text().includes('Scan package'))!.trigger('click')
    await flushPromises()

    const dialog = document.body
    const box = (name: string) => dialog.querySelector(`[data-testid="scanner-${name}"]`) as HTMLInputElement
    expect(box('osv').checked).toBe(true)
    expect(box('trivy').checked).toBe(false)
    expect(box('grype').checked).toBe(false)

    // v-model on a checkbox array reads the rendered value: re-render between clicks.
    box('trivy').click()
    await flushPromises()
    box('grype').click()
    await flushPromises()

    const pkg = dialog.querySelector('#vuln-pkg') as HTMLSelectElement
    pkg.value = 'art-1'
    pkg.dispatchEvent(new Event('change', { bubbles: true }))
    await flushPromises()

    const start = Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Start scan')!
    start.click()
    await flushPromises()

    const post = calls.find((c) => c.method === 'POST' && c.path === `/api/v1/repositories/${REPO}/vuln/scan`)!
    expect(post.body).toEqual({
      artifact_id: 'art-1',
      package_name: 'left-pad',
      package_version: '1.0.0',
      ecosystem: 'npm',
      scanners: ['osv', 'trivy', 'grype'],
    })
    wrapper.unmount()
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
    const calls = mockSecurityApi(`/api/v1/repositories/${REPO}/vuln/scans`, [CLEAN], (call) =>
      call.path === `/api/v1/repositories/${REPO}/vuln/scanners` ? { scanners } : {},
    )
    const wrapper = mount(VulnScanPage, {
      global: { plugins: [createPinia()], stubs: { RouterLink: true, PageHeader: PageHeaderStub } },
      attachTo: document.body,
    })
    await selectRepo(wrapper)
    await wrapper.findAll('button').find((b) => b.text().includes('Scan package'))!.trigger('click')
    await flushPromises()
    return { wrapper, calls }
  }

  const box = (name: string) => document.body.querySelector(`[data-testid="scanner-${name}"]`) as HTMLInputElement

  it('disables scanners that are not ready and explains why, without following a non-https link', async () => {
    const { wrapper, calls } = await openForm(SCANNERS)

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
    wrapper.unmount()
  })

  it('says whether a scanner is installing or its installation failed', async () => {
    const { wrapper } = await openForm([
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
    wrapper.unmount()
  })

  it('keeps every scanner selectable when the readiness list is unavailable', async () => {
    const { wrapper } = await openForm(undefined)

    expect(box('trivy').disabled).toBe(false)
    expect(box('grype').disabled).toBe(false)
    expect(document.body.querySelector('[data-testid="scanner-unavailable"]')).toBeNull()
    wrapper.unmount()
  })
})

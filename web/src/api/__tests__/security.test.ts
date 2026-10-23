// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import {
  listAllVulnScans,
  getVulnScanSummary,
  getVulnScan,
  deleteVulnScan,
  listVulnScanners,
  startVulnScan,
  triggerLicenseScan,
  triggerSecretScan,
  setVulnQuarantine,
  setLicenseQuarantine,
  VULN_ECOSYSTEMS,
  vulnScansExportUrl,
  secretScansExportUrl,
  downloadScanExport,
} from '../security'
import { triggerAIAnalysis } from '../ai'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

let spy: ReturnType<typeof spyOn<typeof globalThis, 'fetch'>>
afterEach(() => spy?.mockRestore())

function lastBody(): unknown {
  return JSON.parse((spy.mock.calls.at(-1)![1] as RequestInit).body as string)
}
function lastUrl(): string {
  return String(spy.mock.calls.at(-1)![0])
}

describe('security api', () => {
  it('lists scanner readiness from the repository-scoped endpoint', async () => {
    const osv = { name: 'osv', available: true, ready: true }
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ scanners: [osv] }))
    expect(await listVulnScanners('my repo')).toEqual([osv as never])
    expect(lastUrl()).toBe('/api/v1/repositories/my%20repo/vuln/scanners')
    spy.mockResolvedValue(json({}))
    expect(await listVulnScanners('r')).toEqual([])
  })

  it('license scans send only artifact_id', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ id: 's' }))
    await triggerLicenseScan('my repo', 'art-1')
    expect(lastUrl()).toBe('/api/v1/repositories/my%20repo/license/scan')
    expect(lastBody()).toEqual({ artifact_id: 'art-1' })
  })

  it('secret scans send the content to scan', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ id: 's' }))
    await triggerSecretScan('r', 'art-1', 'AKIA...')
    expect(lastBody()).toEqual({ artifact_id: 'art-1', content: 'AKIA...' })
  })

  it('vuln scans carry a real artifact id and an internal ecosystem id', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ id: 's' }))
    await startVulnScan('r', {
      artifact_id: 'art-1',
      package_name: 'left-pad',
      package_version: '1.0.0',
      ecosystem: 'npm',
      scanners: ['osv'],
    })
    expect(lastBody()).toEqual({
      artifact_id: 'art-1',
      package_name: 'left-pad',
      package_version: '1.0.0',
      ecosystem: 'npm',
      scanners: ['osv'],
    })
    expect(lastUrl()).toBe('/api/v1/repositories/r/vuln/scan')
    expect(VULN_ECOSYSTEMS).toContain('npm')
    expect(VULN_ECOSYSTEMS).not.toContain('Go')
  })

  it('vuln scans send every selected scanner in the scanners array', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ id: 's' }))
    await startVulnScan('r', {
      artifact_id: 'art-1',
      package_name: 'left-pad',
      package_version: '1.0.0',
      ecosystem: 'npm',
      scanners: ['osv', 'trivy', 'grype'],
    })
    expect(lastBody()).toMatchObject({ scanners: ['osv', 'trivy', 'grype'] })
    expect(lastBody()).not.toHaveProperty('scanner')
  })

  it('license quarantine approves with quarantined:false and quarantines with true', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () =>
      json({ id: 's1', quarantined: false, quarantine_approved: true }),
    )
    const approved = await setLicenseQuarantine('my repo', 's/1', false)
    expect(lastUrl()).toBe('/api/v1/repositories/my%20repo/license/scans/s%2F1/quarantine')
    expect((spy.mock.calls.at(-1)![1] as RequestInit).method).toBe('POST')
    expect(lastBody()).toEqual({ quarantined: false })
    expect(approved.quarantine_approved).toBe(true)

    await setLicenseQuarantine('my repo', 's1', true)
    expect(lastBody()).toEqual({ quarantined: true })
  })

  it('quarantine toggles post the new state', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ id: 's', quarantined: true }))
    await setVulnQuarantine('r', 's1', true)
    expect(lastUrl()).toBe('/api/v1/repositories/r/vuln/scans/s1/quarantine')
    expect(lastBody()).toEqual({ quarantined: true })
  })

  it('AI trigger posts manifest_text and artifact metadata to the artifact id route', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ status: 'queued', message: '' }))
    await triggerAIAnalysis('r', 'art-1', { manifest_text: 'm', artifact_name: 'n' })
    expect(lastUrl()).toBe('/api/v1/repositories/r/ai-analysis/art-1/trigger')
    expect(lastBody()).toEqual({ manifest_text: 'm', artifact_name: 'n' })
  })

  it('lists scans across repositories with only the set filters and the cursor', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ scans: [{ id: 's1' }], has_more: true, next_cursor: 'c2' }))
    const page = await listAllVulnScans({ repo: 'my repo', severity: 'high', quarantined: false, limit: 25, cursor: 'c1' })
    expect(page).toEqual({ scans: [{ id: 's1' } as never], has_more: true, next_cursor: 'c2' })
    expect(lastUrl()).toBe('/api/v1/vuln/scans?repo=my+repo&severity=high&quarantined=false&limit=25&cursor=c1')

    spy.mockResolvedValue(json({}))
    expect(await listAllVulnScans()).toEqual({ scans: [], has_more: false, next_cursor: undefined })
    expect(lastUrl()).toBe('/api/v1/vuln/scans')
  })

  it('reads the summary for an optional repository', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ total: 3 }))
    await getVulnScanSummary('r1')
    expect(lastUrl()).toBe('/api/v1/vuln/scans/summary?repo=r1')
    await getVulnScanSummary()
    expect(lastUrl()).toBe('/api/v1/vuln/scans/summary')
  })

  it('reads and deletes a single scan through its repository', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ id: 's/1' }))
    await getVulnScan('my repo', 's/1')
    expect(lastUrl()).toBe('/api/v1/repositories/my%20repo/vuln/scans/s%2F1')
    spy.mockResolvedValue(new Response(null, { status: 204 }))
    await deleteVulnScan('my repo', 's/1')
    expect(lastUrl()).toBe('/api/v1/repositories/my%20repo/vuln/scans/s%2F1')
    expect((spy.mock.calls.at(-1)![1] as RequestInit).method).toBe('DELETE')
  })
})

describe('scan exports', () => {
  it('builds export URLs that carry the overview filters and the format', () => {
    expect(vulnScansExportUrl({}, 'sarif')).toBe('/api/v1/vuln/scans/export?format=sarif')
    expect(vulnScansExportUrl({ repo: 'npm main', severity: 'high', quarantined: false }, 'csv')).toBe(
      '/api/v1/vuln/scans/export?repo=npm+main&severity=high&quarantined=false&format=csv',
    )
    expect(secretScansExportUrl('raw/x', 'json')).toBe('/api/v1/repositories/raw%2Fx/secrets/scans/export?format=json')
  })

  it('downloads through a preflight, then lets the browser stream the file', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{}', { status: 200, headers: { 'Content-Disposition': 'attachment; filename="vuln-scans.sarif"' } }),
    )
    const clicked: { href: string; download: string }[] = []
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.getAttribute('href') ?? '', download: this.download })
    })
    await downloadScanExport('/api/v1/vuln/scans/export?format=sarif', 'sarif')
    click.mockRestore()
    expect(lastUrl()).toBe('/api/v1/vuln/scans/export?format=sarif')
    expect(clicked).toEqual([{ href: '/api/v1/vuln/scans/export?format=sarif', download: 'vuln-scans.sarif' }])
  })

  it('rejects with the server error and starts no download', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ error: 'admin access required', code: 'forbidden' }), {
        status: 403,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    await expect(downloadScanExport('/x', 'csv')).rejects.toThrow()
    expect(click).not.toHaveBeenCalled()
    click.mockRestore()
  })
})

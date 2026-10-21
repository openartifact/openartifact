// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import {
  startVulnScan,
  triggerLicenseScan,
  triggerSecretScan,
  setVulnQuarantine,
  setLicenseQuarantine,
  VULN_ECOSYSTEMS,
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
})

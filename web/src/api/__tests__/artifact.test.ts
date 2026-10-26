// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { getSecuritySummary, listArtifactActivity, setVersionQuarantine } from '../artifact'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

interface Call {
  url: string
  method: string
}
const calls = (): Call[] => spy.mock.calls.map(([url, init]) => ({ url: String(url), method: init?.method ?? 'GET' }))

describe('artifact api', () => {
  it('asks for the security counts of one package version', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ scanned_at: null }))
    await getSecuritySummary('my repo', '@s/p', '1.0.0')
    expect(calls()[0]!.url).toBe('/api/v1/repositories/my%20repo/security/summary?package_name=%40s%2Fp&version=1.0.0')
  })

  it('pages activity by cursor and reports whether more follows', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ data: [{ id: 'e1' }], has_more: true, next_cursor: 'c2' }))
    const page = await listArtifactActivity('r', 'p', '1.0.0', { cursor: 'c1', action: 'scan', limit: 25 })
    expect(page).toEqual({ entries: [{ id: 'e1' }], hasMore: true, nextCursor: 'c2' })
    expect(calls()[0]!.url).toBe('/api/v1/repositories/r/activity?package_name=p&version=1.0.0&cursor=c1&action=scan&limit=25')
  })

  it('ends paging when the cursor is missing', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ data: [], has_more: true }))
    expect(await listArtifactActivity('r', 'p', '1')).toEqual({ entries: [], hasMore: false, nextCursor: null })
  })
})

describe('setVersionQuarantine', () => {
  const scan = (id: string, artifact_id: string, quarantined: boolean) => ({ id, artifact_id, quarantined })

  function serve(): void {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input)
      if ((init?.method ?? 'GET') !== 'GET') return json({})
      if (url.includes('/vuln/scans')) return json({ scans: [scan('v1', 'f1', false), scan('v2', 'other', false), scan('v3', 'f2', true)] })
      if (url.includes('/license')) return json({ scans: [scan('l1', 'f1', false)] })
      return json({ scans: [] })
    })
  }

  it('quarantines only unflagged scan results of the version files', async () => {
    serve()
    const changed = await setVersionQuarantine('r', ['f1', 'f2'], true)
    const writes = calls().filter((c) => c.method !== 'GET')
    expect(changed).toBe(2)
    const urls = writes.map((w) => w.url)
    expect(urls).toHaveLength(2)
    expect(urls.some((u) => u.includes('/vuln/scans/v1/quarantine'))).toBe(true)
    expect(urls.some((u) => u.includes('/license/scans/l1/quarantine'))).toBe(true)
  })

  it('releases only the quarantined ones and reports zero when nothing matches', async () => {
    serve()
    expect(await setVersionQuarantine('r', ['f2'], false)).toBe(1)
    spy.mockRestore()
    serve()
    expect(await setVersionQuarantine('r', ['nothing'], true)).toBe(0)
  })
})

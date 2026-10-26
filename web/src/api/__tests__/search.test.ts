// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, spyOn } from 'bun:test'
import { searchChecksum, searchPackageGroups } from '../search'

function mockFetch(body: unknown) {
  return spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
  )
}
const urlOf = (f: ReturnType<typeof mockFetch>): URL => new URL(String(f.mock.calls[0]![0]), 'http://x')

afterEach(() => undefined)

describe('grouped and checksum search', () => {
  it('asks for grouped packages with the filters and the page', async () => {
    const f = mockFetch({ packages: [{ repository: 'r', format: 'npm', name: 'a', version: '1.2.0', path: 'a-1.2.0.tgz', version_count: 2 }], total: 41 })
    const res = await searchPackageGroups({ q: 'a', format: 'npm', repo: 'r', flagged: true, limit: 20, page: 3 })
    const q = urlOf(f).searchParams
    expect(urlOf(f).pathname).toBe('/api/v1/search')
    expect([q.get('q'), q.get('group'), q.get('format'), q.get('repo'), q.get('flagged'), q.get('limit'), q.get('page')]).toEqual(['a', 'package', 'npm', 'r', 'true', '20', '3'])
    expect(res.total).toBe(41)
    expect(res.packages[0]).toEqual({ repository: 'r', format: 'npm', name: 'a', latest_version: '1.2.0', version_count: 2 })
  })

  it('omits unset filters and page one', async () => {
    const f = mockFetch({})
    const res = await searchPackageGroups({ q: 'a', page: 1 })
    expect([...urlOf(f).searchParams.keys()].sort()).toEqual(['group', 'q'])
    expect(res).toEqual({ packages: [], total: 0 })
  })

  it('sends the checksum parameter and maps the files', async () => {
    const f = mockFetch({ packages: [{ repository: 'r', format: 'raw', name: 'n', version: '1', filename: 'f', checksum: 'ab', algorithm: 'md5' }], total: 1 })
    const res = await searchChecksum({ checksum: 'ab', repo: 'r' })
    expect(urlOf(f).searchParams.get('checksum')).toBe('ab')
    expect(urlOf(f).searchParams.get('repo')).toBe('r')
    expect(res.packages[0]!.algorithm).toBe('md5')
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { deletePackage, downloadPackage, listComponents, listPackageVersions, listRepoMembers, presignModeOf, presignValueOf, updateRepository } from '../repositories'

function stubFetch(responses: unknown[]): { urls: string[]; methods: string[] } {
  const seen = { urls: [] as string[], methods: [] as string[] }
  let i = 0
  spyOn(globalThis, 'fetch').mockImplementation((async (url: string, init?: RequestInit) => {
    seen.urls.push(String(url))
    seen.methods.push(init?.method ?? 'GET')
    const body = responses[Math.min(i++, responses.length - 1)]
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as never)
  return seen
}

describe('repositories api', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('deletePackage targets the id-based route that exists on the backend', async () => {
    const seen = stubFetch([{}])
    await deletePackage('my repo', 'abc-123')
    expect(seen.methods[0]).toBe('DELETE')
    expect(seen.urls[0]).toBe('/api/v1/repositories/my%20repo/packages/abc-123')
  })

  it('listComponents walks every page of /packages', async () => {
    const page = (n: number, count: number) => ({
      total: 3,
      packages: Array.from({ length: count }, (_, k) => ({ id: `${n}-${k}`, name: 'p', version: '1', format: 'deb' })),
    })
    const seen = stubFetch([page(1, 2), page(2, 1)])
    // Force small pages through the server's total.
    const res = await listComponents('r')
    expect(res.items.length).toBeGreaterThanOrEqual(2)
    expect(seen.urls[0]).toContain('/api/v1/repositories/r/packages')
  })

  it('downloadPackage navigates the browser straight to the streaming download URL', () => {
    const hrefs: string[] = []
    spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      hrefs.push(this.getAttribute('href') ?? '')
    })

    downloadPackage('npm repo', { id: 'row/1', name: '@acme/widget', version: '1.0.0' })

    expect(hrefs).toEqual(['/api/v1/repositories/npm%20repo/packages/row%2F1/download'])
  })

  it('listRepoMembers unwraps the ordered members envelope', async () => {
    const members = [
      { id: '1', name: 'a', format: 'maven', repo_type: 'hosted', priority: 0 },
      { id: '2', name: 'b', format: 'maven', repo_type: 'proxy', priority: 1 },
    ]
    const seen = stubFetch([{ members }])
    expect(await listRepoMembers('my group')).toEqual(members as never)
    expect(seen.urls[0]).toBe('/api/v1/repositories/my%20group/members')
    stubFetch([{}])
    expect(await listRepoMembers('g')).toEqual([])
  })

  it('maps the tri-state presign override both ways', () => {
    expect(presignModeOf({})).toBe('inherit')
    expect(presignModeOf({ presign_downloads: null })).toBe('inherit')
    expect(presignModeOf({ presign_downloads: true })).toBe('always')
    expect(presignModeOf({ presign_downloads: false })).toBe('never')
    expect(presignValueOf('inherit')).toBeNull()
    expect(presignValueOf('always')).toBe(true)
    expect(presignValueOf('never')).toBe(false)
  })

  it('updateRepository sends presign_downloads: null so the server clears the override', async () => {
    let sent = ''
    spyOn(globalThis, 'fetch').mockImplementation((async (_url: string, init?: RequestInit) => {
      sent = String(init?.body)
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }) as never)
    await updateRepository('r', { presign_downloads: presignValueOf('inherit') })
    expect(JSON.parse(sent)).toEqual({ presign_downloads: null })
  })
})

describe('listPackageVersions', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('looks a package up by exact name server-side instead of walking the repository', async () => {
    const seen = stubFetch([{ total: 1, packages: [{ id: '1', name: '@a/b', version: '1.0.0', format: 'npm' }] }])
    const rows = await listPackageVersions('r', '@a/b')
    expect(rows).toHaveLength(1)
    expect(seen.urls).toHaveLength(1)
    expect(seen.urls[0]).toContain('package_name=%40a%2Fb')
  })
})

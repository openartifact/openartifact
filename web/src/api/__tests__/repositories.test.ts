// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { deletePackage, downloadPackage, listPackages, listPackageVersions, listRepoMembers, presignModeOf, presignValueOf, updateRepository } from '../repositories'
import { ApiError, attachmentFilename } from '../client'

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

  it('listPackages sends the page, search text and order to the server', async () => {
    const seen = stubFetch([{ total: 0, packages: [] }])
    await listPackages('my repo', { page: 3, limit: 25, q: 'wid get', sort: 'name' })
    expect(seen.urls[0]).toBe('/api/v1/repositories/my%20repo/packages?page=3&limit=25&q=wid+get&sort=name')

    await listPackages('r')
    expect(seen.urls[1]).toBe('/api/v1/repositories/r/packages')
  })

  it('listPackages sends the keyset cursor', async () => {
    const seen = stubFetch([{ packages: [], has_more: false }])
    await listPackages('r', { limit: 100, cursor: 'abc_-9' })
    expect(seen.urls[0]).toBe('/api/v1/repositories/r/packages?limit=100&cursor=abc_-9')
  })

  it('downloadPackage preflights without reading the body, then lets the browser stream the file', async () => {
    const seen: { url: string; signal?: AbortSignal }[] = []
    spyOn(globalThis, 'fetch').mockImplementation((async (url: string, init?: RequestInit) => {
      seen.push({ url: String(url), signal: init?.signal ?? undefined })
      return new Response('tarball-bytes', {
        status: 200,
        headers: { 'Content-Disposition': 'attachment; filename=widget-1.0.0.tgz' },
      })
    }) as never)
    const createUrl = spyOn(URL, 'createObjectURL')
    const clicked: { href: string; download: string }[] = []
    spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.getAttribute('href') ?? '', download: this.download })
    })

    await downloadPackage('npm repo', { id: 'row/1', name: '@acme/widget', version: '1.0.0' })

    const url = '/api/v1/repositories/npm%20repo/packages/row%2F1/download'
    expect(seen.map((r) => r.url)).toEqual([url])
    expect(seen[0].signal?.aborted).toBe(true)
    expect(createUrl).not.toHaveBeenCalled()
    expect(clicked).toEqual([{ href: url, download: 'widget-1.0.0.tgz' }])
  })

  it('downloadPackage rejects with the server error on 4xx and starts no download', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () =>
      new Response(JSON.stringify({ error: 'artifact quarantined', code: 'ARTIFACT_QUARANTINED' }), {
        status: 403,
        headers: { 'Content-Type': 'application/json' },
      })) as never)
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    const err = await downloadPackage('r', { id: 'x', name: 'n', version: '' }).catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(403)
    expect((err as ApiError).code).toBe('ARTIFACT_QUARANTINED')
    expect((err as ApiError).message).toBe('artifact quarantined')
    expect(click).not.toHaveBeenCalled()
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

  it('attachmentFilename reads quoted, bare and RFC 5987 names', () => {
    expect(attachmentFilename('attachment; filename="a b.tgz"')).toBe('a b.tgz')
    expect(attachmentFilename('attachment; filename=a.tgz')).toBe('a.tgz')
    expect(attachmentFilename("attachment; filename*=UTF-8''caf%C3%A9.json")).toBe('café.json')
    expect(attachmentFilename('inline')).toBeUndefined()
    expect(attachmentFilename(null)).toBeUndefined()
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

  it('walks the rows by next_cursor until has_more is false, without page or total', async () => {
    const row = (id: string) => ({ id, name: '@a/b', version: id, format: 'npm' })
    const seen = stubFetch([
      { total: 3, page: 1, limit: 100, has_more: true, next_cursor: 'c1', packages: [row('1'), row('2')] },
      { page: 1, limit: 100, has_more: true, next_cursor: 'c2', packages: [row('3')] },
      { page: 1, limit: 100, has_more: false, packages: [row('4')] },
    ])
    const rows = await listPackageVersions('r', '@a/b', 'npm')
    expect(rows.map((r) => r.id)).toEqual(['1', '2', '3', '4'])
    expect(seen.urls).toHaveLength(3)
    expect(seen.urls[0]).not.toContain('cursor=')
    expect(seen.urls[1]).toContain('cursor=c1')
    expect(seen.urls[2]).toContain('cursor=c2')
    for (const u of seen.urls) {
      expect(u).not.toContain('page=')
      expect(u).toContain('package_name=%40a%2Fb')
    }
  })

  it('stops on an empty page or a missing cursor even if has_more is set', async () => {
    const seen = stubFetch([{ has_more: true, packages: [{ id: '1', name: 'x', version: '1', format: 'npm' }] }])
    expect(await listPackageVersions('r', 'x')).toHaveLength(1)
    expect(seen.urls).toHaveLength(1)
  })

  it('fetches the pages after the first by number, all at once, and keeps the newest-first order', async () => {
    const row = (id: number) => ({ id: String(id), name: '@a/b', version: `1.0.${id}`, format: 'npm' })
    const pageRows = (page: number) => Array.from({ length: page === 4 ? 50 : 100 }, (_, i) => row((page - 1) * 100 + i))
    const urls: string[] = []
    let inFlight = 0
    let peak = 0
    spyOn(globalThis, 'fetch').mockImplementation((async (url: string) => {
      urls.push(String(url))
      const page = Number(new URL(String(url), 'http://x').searchParams.get('page') ?? '1')
      inFlight++
      peak = Math.max(peak, inFlight)
      await new Promise((r) => setTimeout(r, 2))
      inFlight--
      const body = { ...(page === 1 ? { total: 350 } : {}), page, limit: 100, has_more: page < 4, packages: pageRows(page) }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }) as never)

    const rows = await listPackageVersions('r', '@a/b')
    expect(rows).toHaveLength(350)
    expect(rows.map((r) => r.id)).toEqual(Array.from({ length: 350 }, (_, i) => String(i)))
    expect(urls).toHaveLength(4)
    expect(peak).toBeGreaterThan(1)
  })

  it('follows the cursor for rows the total did not cover and returns a shifted row once', async () => {
    const row = (id: string) => ({ id, name: 'x', version: id, format: 'npm' })
    const seen = stubFetch([
      { total: 150, has_more: true, next_cursor: 'c1', packages: [row('1')] },
      { page: 2, has_more: true, next_cursor: 'c2', packages: [row('1'), row('2')] },
      { has_more: false, packages: [row('3')] },
    ])
    const rows = await listPackageVersions('r', 'x')
    expect(rows.map((r) => r.id)).toEqual(['1', '2', '3'])
    expect(seen.urls[1]).toContain('page=2')
    expect(seen.urls[2]).toContain('cursor=c2')
  })

  it('walks a capped total (total_exact false) by cursor only, never by page number', async () => {
    const row = (id: string) => ({ id, name: 'x', version: id, format: 'npm' })
    const seen = stubFetch([
      { total: 10000, total_exact: false, has_more: true, next_cursor: 'c1', packages: [row('1')] },
      { has_more: true, next_cursor: 'c2', packages: [row('2')] },
      { has_more: false, packages: [row('3')] },
    ])
    const rows = await listPackageVersions('r', 'x')
    expect(rows.map((r) => r.id)).toEqual(['1', '2', '3'])
    expect(seen.urls.every((u) => !u.includes('page=2'))).toBe(true)
    expect(seen.urls[1]).toContain('cursor=c1')
  })

  it('rejects when any page fails', async () => {
    let n = 0
    spyOn(globalThis, 'fetch').mockImplementation((async () => {
      n++
      if (n === 3) return new Response('{"error":"boom"}', { status: 500, headers: { 'Content-Type': 'application/json' } })
      return new Response(JSON.stringify({ total: 400, has_more: true, next_cursor: 'c', packages: [{ id: String(n), name: 'x', version: '1', format: 'npm' }] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as never)
    await expect(listPackageVersions('r', 'x')).rejects.toThrow()
  })
})

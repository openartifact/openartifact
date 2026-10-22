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
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { deletePackage, downloadPackage, listComponents, listRepoMembers } from '../repositories'
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

  it('downloadPackage fetches the id route and saves the blob under the server filename', async () => {
    const urls: string[] = []
    spyOn(globalThis, 'fetch').mockImplementation((async (url: string) => {
      urls.push(String(url))
      return new Response('tarball-bytes', {
        status: 200,
        headers: { 'Content-Disposition': 'attachment; filename=widget-1.0.0.tgz' },
      })
    }) as never)
    const created: Blob[] = []
    const createUrl = spyOn(URL, 'createObjectURL').mockImplementation(((b: Blob) => {
      created.push(b)
      return 'blob:x'
    }) as never)
    spyOn(URL, 'revokeObjectURL').mockImplementation((() => {}) as never)
    const names: string[] = []
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download)
    })

    await downloadPackage('npm repo', { id: 'row/1', name: '@acme/widget', version: '1.0.0' })

    expect(urls[0]).toBe('/api/v1/repositories/npm%20repo/packages/row%2F1/download')
    expect(createUrl).toHaveBeenCalledTimes(1)
    expect(await created[0].text()).toBe('tarball-bytes')
    expect(click).toHaveBeenCalledTimes(1)
    expect(names).toEqual(['widget-1.0.0.tgz'])
  })

  it('downloadPackage rejects with the server error on 4xx so the page can toast it', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () =>
      new Response(JSON.stringify({ error: 'package not found', code: 'not_found' }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      })) as never)
    const createUrl = spyOn(URL, 'createObjectURL')

    const err = await downloadPackage('r', { id: 'x', name: 'n', version: '' }).catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(404)
    expect((err as ApiError).message).toBe('package not found')
    expect(createUrl).not.toHaveBeenCalled()
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
})

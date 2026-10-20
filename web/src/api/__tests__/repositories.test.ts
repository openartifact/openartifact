// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { deletePackage, listComponents } from '../repositories'

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
})

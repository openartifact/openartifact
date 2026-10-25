// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { getRepoQuota, removeRepoQuota, setRepoQuota } from '../quota'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

function stub(body: unknown = {}, status = 200): void {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async () => (status === 204 ? new Response(null, { status }) : json(body, status)))
}

function lastCall(): { url: string; method: string; body: unknown } {
  const [url, init] = spy.mock.calls.at(-1)!
  return { url: String(url), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined }
}

describe('repository quota api', () => {
  it('reads the quota of an encoded repository name', async () => {
    stub({ max_size_bytes: 10, current_size_bytes: 2 })
    expect(await getRepoQuota('my repo')).toEqual({ max_size_bytes: 10, current_size_bytes: 2 })
    expect(lastCall()).toMatchObject({ url: '/api/v1/repositories/my%20repo/quota', method: 'GET' })
  })

  it('sets the size limit with PUT', async () => {
    stub({ max_size_bytes: 1024, current_size_bytes: 0 })
    await setRepoQuota('r', 1024)
    expect(lastCall()).toEqual({ url: '/api/v1/repositories/r/quota', method: 'PUT', body: { max_size_bytes: 1024 } })
  })

  it('removes the quota with DELETE', async () => {
    stub(undefined, 204)
    await removeRepoQuota('r')
    expect(lastCall()).toMatchObject({ url: '/api/v1/repositories/r/quota', method: 'DELETE' })
  })
})

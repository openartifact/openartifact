// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { getSbomDocument, listSboms } from '../sbom'

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

describe('sbom api', () => {
  it('lists the reports of an encoded repository and tolerates a missing array', async () => {
    stub({ reports: [{ id: 's1', format: 'cyclonedx' }] })
    expect(await listSboms('my repo')).toEqual([{ id: 's1', format: 'cyclonedx' }])
    expect(lastCall()).toMatchObject({ url: '/api/v1/repositories/my%20repo/sbom', method: 'GET' })
    spy.mockRestore()
    stub({})
    expect(await listSboms('my repo')).toEqual([])
  })

  it('downloads one document by encoded id', async () => {
    stub({ components: [{ name: 'a' }] })
    expect(await getSbomDocument('my repo', 's/1')).toEqual({ components: [{ name: 'a' }] })
    expect(lastCall()).toMatchObject({ url: '/api/v1/repositories/my%20repo/sbom/s%2F1/download', method: 'GET' })
  })
})

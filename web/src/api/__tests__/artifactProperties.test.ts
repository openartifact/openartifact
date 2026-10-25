// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { deleteArtifactProperty, getArtifactProperties, setArtifactProperties } from '../artifactProperties'

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

const BASE = '/api/v1/repositories/my%20repo/artifacts/a%2F1/properties'

describe('artifact properties api', () => {
  it('reads properties', async () => {
    stub({ k: 'v' })
    expect(await getArtifactProperties('my repo', 'a/1')).toEqual({ k: 'v' })
    expect(lastCall()).toMatchObject({ url: BASE, method: 'GET' })
  })

  it('patches the given keys wrapped in a properties object', async () => {
    stub(undefined, 204)
    await setArtifactProperties('my repo', 'a/1', { k: 'v' })
    expect(lastCall()).toEqual({ url: BASE, method: 'PATCH', body: { properties: { k: 'v' } } })
  })

  it('deletes one property with the key encoded', async () => {
    stub(undefined, 204)
    await deleteArtifactProperty('my repo', 'a/1', 'a key')
    expect(lastCall()).toMatchObject({ url: `${BASE}/a%20key`, method: 'DELETE' })
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { createWebhook, deleteWebhook, listWebhooks, updateWebhook } from '../webhooks'

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

describe('webhooks api', () => {
  it('lists webhooks and tolerates a missing array', async () => {
    stub({ webhooks: [{ id: 'w1', name: 'ci', url: 'https://x', events: ['push'], enabled: true }] })
    expect(await listWebhooks()).toHaveLength(1)
    expect(lastCall()).toMatchObject({ url: '/api/v1/webhooks', method: 'GET' })
    spy.mockRestore()
    stub({})
    expect(await listWebhooks()).toEqual([])
  })

  it('creates a webhook with the given body', async () => {
    stub({ id: 'w1' }, 201)
    const input = { name: 'ci', url: 'https://x', events: ['push'], payload_format: 'openartifact', ssl_verify: true, secret: 's' }
    expect(await createWebhook(input)).toEqual({ id: 'w1' })
    expect(lastCall()).toEqual({ url: '/api/v1/webhooks', method: 'POST', body: input })
  })

  it('updates and deletes with the id encoded', async () => {
    stub({ id: 'a/b' })
    await updateWebhook('a/b', { enabled: false, ssl_verify: false })
    expect(lastCall()).toEqual({ url: '/api/v1/webhooks/a%2Fb', method: 'PUT', body: { enabled: false, ssl_verify: false } })
    spy.mockRestore()
    stub(undefined, 204)
    await deleteWebhook('a/b')
    expect(lastCall()).toMatchObject({ url: '/api/v1/webhooks/a%2Fb', method: 'DELETE' })
  })
})

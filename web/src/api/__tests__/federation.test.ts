// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import {
  createFederationPeer,
  createFederationToken,
  deleteFederationPeer,
  getFederationStatus,
  listFederationDeferred,
  resyncFederationPeer,
  revokeFederationToken,
  setFederationEnabled,
  updateFederationPeer,
} from '../federation'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: ReturnType<typeof spyOn<typeof globalThis, 'fetch'>>
afterEach(() => spy?.mockRestore())

const call = (i = 0) => ({
  url: String(spy.mock.calls[i]![0]),
  init: (spy.mock.calls[i]![1] ?? {}) as RequestInit,
})

describe('federation api', () => {
  it('reads the status of a repository with an escaped name', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ enabled: false }))
    await getFederationStatus('my repo')
    expect(call().url).toBe('/api/v1/repositories/my%20repo/federation')
    expect(call().init.method ?? 'GET').toBe('GET')
  })

  it('enables and disables with a PUT body', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ enabled: true }))
    await setFederationEnabled('r', true)
    await setFederationEnabled('r', false)
    expect(call(0).init.method).toBe('PUT')
    expect(JSON.parse(call(0).init.body as string)).toEqual({ enabled: true })
    expect(JSON.parse(call(1).init.body as string)).toEqual({ enabled: false })
  })

  it('creates and patches peers on the peer routes', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ id: 'p' }, 201))
    await createFederationPeer('r', { name: 'b', base_url: 'https://b', token: 't' })
    await updateFederationPeer('r', 'a/b', { enabled: false })
    expect(call(0).url).toBe('/api/v1/repositories/r/federation/peers')
    expect(call(0).init.method).toBe('POST')
    expect(JSON.parse(call(0).init.body as string)).toEqual({ name: 'b', base_url: 'https://b', token: 't' })
    expect(call(1).url).toBe('/api/v1/repositories/r/federation/peers/a%2Fb')
    expect(call(1).init.method).toBe('PATCH')
    expect(JSON.parse(call(1).init.body as string)).toEqual({ enabled: false })
  })

  it('deletes and resyncs a peer', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async (url) =>
      String(url).endsWith('/resync') ? json({ id: 'p', resync_requested: true }, 202) : new Response(null, { status: 204 }),
    )
    await deleteFederationPeer('r', 'p')
    const peer = await resyncFederationPeer('r', 'p')
    expect(call(0).url).toBe('/api/v1/repositories/r/federation/peers/p')
    expect(call(0).init.method).toBe('DELETE')
    expect(call(1).url).toBe('/api/v1/repositories/r/federation/peers/p/resync')
    expect(call(1).init.method).toBe('POST')
    expect(peer.resync_requested).toBe(true)
  })

  it('unwraps the deferred list and tolerates a missing items field', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValueOnce(json({ items: [{ unit_key: 'u', seq: 3 }] }))
    expect(await listFederationDeferred('r', 'p')).toEqual([{ unit_key: 'u', seq: 3 } as never])
    expect(call().url).toBe('/api/v1/repositories/r/federation/peers/p/deferred')
    spy.mockResolvedValueOnce(json({}))
    expect(await listFederationDeferred('r', 'p')).toEqual([])
  })

  it('creates a token with its name and revokes by id', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async (_url, init) =>
      init?.method === 'DELETE' ? new Response(null, { status: 204 }) : json({ id: 't1', name: 'site-b', token: 'secret' }, 201),
    )
    const created = await createFederationToken('r', 'site-b')
    await revokeFederationToken('r', 't1')
    expect(created.token).toBe('secret')
    expect(call(0).url).toBe('/api/v1/repositories/r/federation/tokens')
    expect(JSON.parse(call(0).init.body as string)).toEqual({ name: 'site-b' })
    expect(call(1).url).toBe('/api/v1/repositories/r/federation/tokens/t1')
    expect(call(1).init.method).toBe('DELETE')
  })
})

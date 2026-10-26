// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { loadFederationOverview, syncState } from '../federationOverview'

describe('syncState', () => {
  it('has no state without active peers', () => {
    expect(syncState([])).toEqual({ state: 'none', attention: 0 })
    expect(syncState([{ state: 'disabled' }])).toEqual({ state: 'none', attention: 0 })
  })
  it('flags lagging and failing peers', () => {
    expect(syncState([{ state: 'ok' }, { state: 'error' }, { state: 'lagging' }])).toEqual({ state: 'attention', attention: 2 })
  })
  it('is pending until a peer has synced, then ok', () => {
    expect(syncState([{ state: 'pending' }])).toEqual({ state: 'pending', attention: 0 })
    expect(syncState([{ state: 'pending' }, { state: 'ok' }, { state: 'disabled' }])).toEqual({ state: 'ok', attention: 0 })
  })
})

const json = (body: unknown): Response => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch> | undefined
afterEach(() => spy?.mockRestore())

function serve(handler: (url: string) => unknown): void {
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => json(handler(String(input)))) as typeof fetch)
}

const repos = {
  repositories: [
    { id: '1', name: 'a', format: 'npm', repo_type: 'hosted', federated: true },
    { id: '2', name: 'b', format: 'npm', repo_type: 'hosted', federated: false },
    { id: '3', name: 'c', format: 'deb', repo_type: 'hosted' },
  ],
  total: 3,
}

describe('loadFederationOverview', () => {
  it('reports an unknown server state when no repository can federate', async () => {
    serve(() => ({ repositories: [{ id: '3', name: 'c', format: 'deb', repo_type: 'hosted' }], total: 1 }))
    expect(await loadFederationOverview()).toEqual({ serverEnabled: null, instanceId: '', rows: [], failed: [] })
  })

  it('stops at the server flag when federation is off', async () => {
    serve((url) => (url.includes('/federation') ? { server_enabled: false, instance_id: 'i', peers: [] } : repos))
    const res = await loadFederationOverview()
    expect(res.serverEnabled).toBe(false)
    expect(res.rows).toEqual([])
  })

  it('lists the federated repositories with their peer state', async () => {
    serve((url) =>
      url.includes('/repositories/a/federation')
        ? { server_enabled: true, instance_id: 'inst', head_seq: 7, peers: [{ id: 'p', name: 'eu', state: 'error' }] }
        : repos,
    )
    const res = await loadFederationOverview()
    expect(res.serverEnabled).toBe(true)
    expect(res.instanceId).toBe('inst')
    expect(res.rows).toHaveLength(1)
    expect(res.rows[0]).toMatchObject({ repository: 'a', headSeq: 7, state: 'attention', attention: 1 })
  })
})

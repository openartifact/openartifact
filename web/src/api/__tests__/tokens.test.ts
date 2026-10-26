// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, it, expect, spyOn } from 'bun:test'
import { createMyToken, listTokens, revokeEndpoint, revokeToken } from '../tokens'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: ReturnType<typeof spyOn> | undefined
afterEach(() => spy?.mockRestore())

function fetched(i = 0): { url: string; init: RequestInit } {
  const call = spy!.mock.calls[i] as [string, RequestInit]
  return { url: String(call[0]), init: call[1] ?? {} }
}

describe('token creation', () => {
  it('self-service creation sends the kind the backend requires', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ token: 't', id: '1', name: 'cli', kind: 'access' }, 201))
    await createMyToken({ name: 'cli', kind: 'access' })
    expect(JSON.parse(fetched().init.body as string)).toEqual({ name: 'cli', kind: 'access' })
  })

  it('sends scopes and expiry when given', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ token: 't' }, 201))
    await createMyToken({ name: 'cli', kind: 'deploy', scopes: ['p1'], expires_at: '2030-01-01T00:00:00Z' })
    expect(JSON.parse(fetched().init.body as string)).toEqual({
      name: 'cli',
      kind: 'deploy',
      scopes: ['p1'],
      expires_at: '2030-01-01T00:00:00Z',
    })
  })
})

describe('unified token list', () => {
  it('sends only the filters that are set', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ tokens: [], total: 0, page: 1, limit: 25 }))
    await listTokens({})
    expect(fetched().url).toBe('/api/v1/tokens')
    spy.mockClear()
    await listTokens({ type: 'project', q: 'ci', expiresBefore: '2030-01-01T00:00:00Z', project: 'web', page: 2, limit: 25 })
    const qs = new URL(fetched().url, 'http://x').searchParams
    expect(Object.fromEntries(qs)).toEqual({
      type: 'project',
      q: 'ci',
      expires_before: '2030-01-01T00:00:00Z',
      project: 'web',
      page: '2',
      limit: '25',
    })
  })

  it('tolerates a missing token array', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({}))
    expect(await listTokens({ page: 3 })).toEqual({ tokens: [], total: 0, page: 3, limit: 25 })
  })
})

describe('revoke routing', () => {
  const me = { username: 'ada', isAdmin: false }
  const admin = { username: 'root', isAdmin: true }

  it('revokes a personal token of the caller through the self-service endpoint', () => {
    expect(revokeEndpoint({ id: 't1', type: 'personal', owner: 'ada' }, me)).toBe('/api/v1/auth/tokens/t1')
    expect(revokeEndpoint({ id: 't1', type: 'personal', owner: 'ada' }, { ...admin, username: 'ada' })).toBe('/api/v1/auth/tokens/t1')
  })

  it('lets an administrator revoke somebody else\'s personal token, nobody else', () => {
    expect(revokeEndpoint({ id: 't1', type: 'personal', owner: 'ada' }, admin)).toBe('/api/v1/admin/tokens/t1')
    expect(revokeEndpoint({ id: 't1', type: 'personal', owner: 'bob' }, me)).toBeNull()
  })

  it('revokes project tokens through their project', () => {
    expect(revokeEndpoint({ id: 't 2', type: 'project', owner: 'bob', project: 'my proj' }, me)).toBe(
      '/api/v1/projects/my%20proj/tokens/t%202',
    )
    expect(revokeEndpoint({ id: 't2', type: 'project', owner: 'bob' }, admin)).toBe('/api/v1/admin/tokens/t2')
    expect(revokeEndpoint({ id: 't2', type: 'project', owner: 'bob' }, me)).toBeNull()
  })

  it('revokes federation tokens through their repository', () => {
    expect(revokeEndpoint({ id: 't3', type: 'federation', owner: 'root', repository: 'libs' }, admin)).toBe(
      '/api/v1/repositories/libs/federation/tokens/t3',
    )
    expect(revokeEndpoint({ id: 't3', type: 'federation', owner: 'root' }, admin)).toBeNull()
  })

  it('sends DELETE to the resolved endpoint and refuses when there is none', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await revokeToken({ id: 't1', type: 'project', owner: 'bob', project: 'web' }, me)
    expect(fetched().url).toBe('/api/v1/projects/web/tokens/t1')
    expect(fetched().init.method).toBe('DELETE')
    await expect(revokeToken({ id: 't1', type: 'personal', owner: 'bob' }, me)).rejects.toThrow()
    expect(spy.mock.calls).toHaveLength(1)
  })
})

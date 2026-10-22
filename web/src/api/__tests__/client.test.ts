// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { createPinia, setActivePinia, type StoreGeneric } from 'pinia'
import { request, preflight, isSessionExpired, ApiError, SESSION_EXPIRED } from '../client'
import router from '../../router'
import { useSessionStore, type User } from '../../stores/session'

// The 401 handler lazily imports the session store and the router. Both are
// exercised for real: the store lives in a per-test Pinia and the router's
// push() is spied on. No mock.module() here — it is process-global and would
// replace the real modules for every test file that runs afterwards.
// Spies and the fetch stub are reverted by the global afterEach in
// test/isolation.ts.

// Pinia wraps actions and the store is a reactive proxy that spyOn() cannot
// patch, so observe action calls through $onAction instead.
function trackActions(store: StoreGeneric): string[] {
  const names: string[] = []
  store.$onAction(({ name }) => {
    names.push(name)
  })
  return names
}

describe('API client', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('rejects an error response with an empty body instead of treating it as success', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(null, { status: 403, headers: { 'content-length': '0' } }),
    )

    let caught: unknown
    try {
      await request('/api/v1/x', { method: 'DELETE' })
    } catch (err) {
      caught = err
    }
    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).status).toBe(403)
  })

  it('still resolves undefined for a successful empty body', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(null, { status: 200, headers: { 'content-length': '0' } }),
    )
    expect(await request('/api/v1/x', { method: 'DELETE' })).toBeUndefined()
  })

  it('sets X-Requested-With and credentials on every request', async () => {
    const mockFetch = spyOn(globalThis, 'fetch').mockResolvedValue({
      status: 200,
      ok: true,
      headers: new Headers({ 'content-type': 'application/json' }),
      text: async () => JSON.stringify({ ok: true }),
    } as unknown as Response)

    await request('/api/v1/test')

    expect(mockFetch).toHaveBeenCalledTimes(1)
    const [, init] = mockFetch.mock.calls[0] as [string, RequestInit & { headers: Headers }]
    const headers = init.headers as Headers
    expect(headers.get('X-Requested-With')).toBe('XMLHttpRequest')
    expect(init.credentials).toBe('include')
  })

  it('calls clearSession and router.push on 401', async () => {
    const session = useSessionStore()
    session.user = { id: '1', username: 'testuser' } as User
    const actions = trackActions(session)
    const pushSpy = spyOn(router, 'push').mockResolvedValue(undefined)

    spyOn(globalThis, 'fetch').mockResolvedValue({
      status: 401,
      ok: false,
      headers: new Headers(),
      text: async () => '',
    } as unknown as Response)

    await expect(request('/api/v1/protected')).rejects.toBeInstanceOf(ApiError)
    expect(actions).toContain('clearSession')
    expect(session.user).toBeNull()
    expect(pushSpy).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'login' })
    )
  })

  it('does not swallow 401 on the login endpoint — throws ApiError instead', async () => {
    const actions = trackActions(useSessionStore())
    const pushSpy = spyOn(router, 'push').mockResolvedValue(undefined)

    spyOn(globalThis, 'fetch').mockResolvedValue({
      status: 401,
      ok: false,
      headers: new Headers({ 'content-type': 'application/json' }),
      json: async () => ({ error: 'invalid username or password', code: 'invalid_credentials' }),
      text: async () => JSON.stringify({ error: 'invalid username or password', code: 'invalid_credentials' }),
    } as unknown as Response)

    try {
      await request('/api/v1/auth/login')
      expect.fail('should have thrown')
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError)
      const apiErr = err as ApiError
      expect(apiErr.status).toBe(401)
      expect(apiErr.message).toBe('invalid username or password')
    }
    expect(actions).not.toContain('clearSession')
    expect(pushSpy).not.toHaveBeenCalled()
  })

  it('does not log out on 401 from change-password — throws ApiError', async () => {
    const actions = trackActions(useSessionStore())
    const pushSpy = spyOn(router, 'push').mockResolvedValue(undefined)

    spyOn(globalThis, 'fetch').mockResolvedValue({
      status: 401,
      ok: false,
      headers: new Headers(),
      json: async () => ({ error: 'invalid current password', code: 'invalid_credentials' }),
      text: async () => '',
    } as unknown as Response)

    await expect(request('/api/v1/auth/change-password', { method: 'POST' })).rejects.toBeInstanceOf(ApiError)
    expect(actions).not.toContain('clearSession')
    expect(pushSpy).not.toHaveBeenCalled()
  })

  it('returns undefined on 204 No Content', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValue({
      status: 204,
      ok: true,
      headers: new Headers(),
      text: async () => '',
    } as unknown as Response)

    const result = await request('/api/v1/delete')
    expect(result).toBeUndefined()
  })

  it('throws ApiError with correct status and code on non-ok response', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValue({
      status: 400,
      ok: false,
      headers: new Headers({ 'content-type': 'application/json' }),
      json: async () => ({ error: 'bad', code: 'X' }),
      text: async () => JSON.stringify({ error: 'bad', code: 'X' }),
    } as unknown as Response)

    try {
      await request('/api/v1/bad')
      expect.fail('should have thrown')
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError)
      const apiErr = err as ApiError
      expect(apiErr.status).toBe(400)
      expect(apiErr.code).toBe('X')
    }
  })

  it('marks the error of an expired session so global handlers can stay quiet', async () => {
    spyOn(router, 'push').mockResolvedValue(undefined)
    spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 401 }))

    const err = await request('/api/v1/protected').catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe(SESSION_EXPIRED)
    expect(isSessionExpired(err)).toBe(true)
    expect(isSessionExpired(new ApiError('nope', 401, 'invalid_credentials'))).toBe(false)
    expect(isSessionExpired(new Error('x'))).toBe(false)
  })

  it('redirects to login once when several requests expire together', async () => {
    const session = useSessionStore()
    const actions = trackActions(session)
    const pushSpy = spyOn(router, 'push').mockResolvedValue(undefined)
    spyOn(globalThis, 'fetch').mockImplementation((async () => new Response(null, { status: 401 })) as never)

    const results = await Promise.allSettled([request('/api/v1/a'), request('/api/v1/b'), request('/api/v1/c')])

    expect(results.every((r) => r.status === 'rejected')).toBe(true)
    expect(actions.filter((a) => a === 'clearSession')).toHaveLength(1)
    expect(pushSpy).toHaveBeenCalledTimes(1)
  })

  it('does not redirect when signing out an already expired session', async () => {
    const pushSpy = spyOn(router, 'push').mockResolvedValue(undefined)
    spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 401 }))

    const err = await request('/api/v1/auth/logout', { method: 'POST' }).catch((e: unknown) => e)

    expect((err as ApiError).status).toBe(401)
    expect(isSessionExpired(err)).toBe(false)
    expect(pushSpy).not.toHaveBeenCalled()
  })

  describe('preflight', () => {
    it('returns the attachment filename and aborts the transfer without reading the body', async () => {
      let signal: AbortSignal | undefined
      spyOn(globalThis, 'fetch').mockImplementation((async (_url: string, init?: RequestInit) => {
        signal = init?.signal ?? undefined
        return new Response('large-body', { status: 200, headers: { 'Content-Disposition': 'attachment; filename="a b.tgz"' } })
      }) as never)

      const res = await preflight('/api/v1/repositories/r/packages/1/download')

      expect(res.filename).toBe('a b.tgz')
      expect(signal?.aborted).toBe(true)
    })

    it('throws the server error for a refused download', async () => {
      spyOn(globalThis, 'fetch').mockResolvedValue(
        new Response(JSON.stringify({ error: 'artifact quarantined', code: 'ARTIFACT_QUARANTINED' }), { status: 403 }),
      )

      const err = await preflight('/api/v1/x/download').catch((e: unknown) => e)

      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).code).toBe('ARTIFACT_QUARANTINED')
    })
  })
})

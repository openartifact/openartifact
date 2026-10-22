// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn, type Mock } from 'bun:test'
import { setActivePinia, createPinia } from 'pinia'
import { useSessionStore, type User } from '../session'

// Adds the Response surface that request() reads (headers, text) to a partial mock.
function mk(o: { status: number; ok: boolean; json?: () => Promise<unknown> }) {
  return {
    ...o,
    headers: new Headers(),
    text: async () => (o.json ? JSON.stringify(await o.json()) : ''),
  }
}

describe('useSessionStore', () => {
  let fetchMock: Mock<typeof fetch>

  beforeEach(() => {
    setActivePinia(createPinia())
    // Restored by the global afterEach in test/isolation.ts (mock.restore()).
    fetchMock = spyOn(globalThis, 'fetch')
  })

  it('isAuthenticated is false when user is null', () => {
    const store = useSessionStore()
    expect(store.user).toBeNull()
    expect(store.isAuthenticated).toBe(false)
  })

  it('isAuthenticated is true when user is set', () => {
    const store = useSessionStore()
    store.user = { id: '1', username: 'testuser' } as User

    expect(store.isAuthenticated).toBe(true)
  })

  it('fetchMe() with 401 response sets user to null', async () => {
    const store = useSessionStore()
    store.user = { id: '1', username: 'testuser' } as User

    fetchMock.mockResolvedValueOnce(mk({
      status: 401,
      ok: false,
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.user).toBeNull()
    expect(store.isAuthenticated).toBe(false)
  })

  it('fetchMe() with 200 response sets user from JSON body', async () => {
    const store = useSessionStore()
    const mockUser: User = {
      id: '123',
      username: 'john',
      is_admin: false,
    } as User

    fetchMock.mockResolvedValueOnce(mk({
      status: 200,
      ok: true,
      json: async () => mockUser,
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.user).toEqual(mockUser)
    expect(store.isAuthenticated).toBe(true)
  })

  it('keeps the caller\'s project roles from /auth/me', async () => {
    const store = useSessionStore()
    expect(store.projects).toEqual([])

    const projects = [
      { name: 'alpha', role: 'owner' },
      { name: 'beta', role: 'viewer' },
    ]
    fetchMock.mockResolvedValueOnce(mk({
      status: 200,
      ok: true,
      json: async () => ({ id: '1', username: 'ann', projects }),
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.projects).toEqual(projects)
  })

  it('has no projects when /auth/me omits them or the session is cleared', async () => {
    const store = useSessionStore()
    store.user = { id: '1', username: 'ann' } as User
    expect(store.projects).toEqual([])

    store.user = { id: '1', username: 'ann', projects: [{ name: 'alpha', role: 'developer' }] } as User
    expect(store.projects).toHaveLength(1)
    store.clearSession()
    expect(store.projects).toEqual([])
  })

  it('fetchMe() sends correct headers and credentials', async () => {
    const store = useSessionStore()

    fetchMock.mockResolvedValueOnce(mk({
      status: 200,
      ok: true,
      json: async () => ({ id: '1', username: 'test' } as User),
    }) as unknown as Response)

    await store.fetchMe()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/auth/me')
    expect((init.headers as Headers).get('X-Requested-With')).toBe('XMLHttpRequest')
    expect(init.credentials).toBe('include')
  })

  it('isAdmin is true when user.is_admin === true', () => {
    const store = useSessionStore()
    store.user = { id: '1', username: 'admin', is_admin: true } as User

    expect(store.isAdmin).toBe(true)
  })

  it('isAdmin is true when user is in admin group', () => {
    const store = useSessionStore()
    store.user = {
      id: '1',
      username: 'groupadmin',
      is_admin: false,
      groups: [{ name: 'admin' }],
    } as User

    expect(store.isAdmin).toBe(true)
  })

  it('isAdmin is false when user is not admin', () => {
    const store = useSessionStore()
    store.user = {
      id: '1',
      username: 'regular',
      is_admin: false,
      groups: [{ name: 'users' }],
    } as User

    expect(store.isAdmin).toBe(false)
  })

  it('isAdmin is false when user is null', () => {
    const store = useSessionStore()
    expect(store.user).toBeNull()
    expect(store.isAdmin).toBe(false)
  })

  it('mustChangePassword is true when user.must_change_password === true', () => {
    const store = useSessionStore()
    store.user = {
      id: '1',
      username: 'newuser',
      must_change_password: true,
    } as User

    expect(store.mustChangePassword).toBe(true)
  })

  it('mustChangePassword is false when user.must_change_password === false', () => {
    const store = useSessionStore()
    store.user = {
      id: '1',
      username: 'user',
      must_change_password: false,
    } as User

    expect(store.mustChangePassword).toBe(false)
  })

  it('mustChangePassword is false when user is null', () => {
    const store = useSessionStore()
    expect(store.user).toBeNull()
    expect(store.mustChangePassword).toBe(false)
  })

  it('clearSession() sets user to null', () => {
    const store = useSessionStore()
    store.user = { id: '1', username: 'testuser' } as User

    expect(store.isAuthenticated).toBe(true)

    store.clearSession()

    expect(store.user).toBeNull()
    expect(store.isAuthenticated).toBe(false)
  })

  it('fetchMe() with 403 and PASSWORD_CHANGE_REQUIRED code sets locked user', async () => {
    const store = useSessionStore()

    fetchMock.mockResolvedValueOnce(mk({
      status: 403,
      ok: false,
      json: async () => ({ code: 'password_change_required' }),
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.user).not.toBeNull()
    expect(store.user?.must_change_password).toBe(true)
    expect(store.isAuthenticated).toBe(true)
  })

  it('fetchMe() with 403 and PASSWORD_CHANGE_REQUIRED error sets locked user', async () => {
    const store = useSessionStore()

    fetchMock.mockResolvedValueOnce(mk({
      status: 403,
      ok: false,
      json: async () => ({ error: 'password_change_required' }),
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.user).not.toBeNull()
    expect(store.user?.must_change_password).toBe(true)
  })

  it('fetchMe() with 403 and invalid JSON sets user to null', async () => {
    const store = useSessionStore()

    fetchMock.mockResolvedValueOnce(mk({
      status: 403,
      ok: false,
      json: async () => {
        throw new Error('Invalid JSON')
      },
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.user).toBeNull()
  })

  it('fetchMe() with 403 and unrecognized error sets user to null', async () => {
    const store = useSessionStore()

    fetchMock.mockResolvedValueOnce(mk({
      status: 403,
      ok: false,
      json: async () => ({ error: 'SOME_OTHER_ERROR' }),
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.user).toBeNull()
  })

  it('fetchMe() handles fetch errors gracefully', async () => {
    const store = useSessionStore()
    store.user = { id: '1', username: 'testuser' } as User

    fetchMock.mockRejectedValueOnce(new Error('Network error'))

    await store.fetchMe()

    expect(store.user).toBeNull()
  })

  it('fetchMe() ignores non-2xx responses that are not 401 or 403', async () => {
    const store = useSessionStore()

    fetchMock.mockResolvedValueOnce(mk({
      status: 500,
      ok: false,
    }) as unknown as Response)

    await store.fetchMe()

    expect(store.user).toBeNull()
  })

  it('canCreateRepo needs admin or maintainer in some project', () => {
    const store = useSessionStore()
    expect(store.canCreateRepo).toBe(false)

    store.user = { id: '1', username: 'ann', projects: [{ name: 'a', role: 'developer' }, { name: 'b', role: 'viewer' }] } as User
    expect(store.canCreateRepo).toBe(false)

    store.user = { id: '1', username: 'ann', projects: [{ name: 'a', role: 'developer' }, { name: 'b', role: 'maintainer' }] } as User
    expect(store.canCreateRepo).toBe(true)

    store.user = { id: '1', username: 'ann', projects: [{ name: 'a', role: 'owner' }] } as User
    expect(store.canCreateRepo).toBe(true)

    store.user = { id: '1', username: 'root', is_admin: true } as User
    expect(store.canCreateRepo).toBe(true)
  })
})

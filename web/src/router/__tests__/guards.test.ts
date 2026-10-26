// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Router guard integration test.
// Drives the actual singleton router (router/index.ts) against a real session
// store living in a per-test Pinia. The guard calls useSessionStore(), which
// resolves to whatever Pinia is active, so each test just sets `user` on its own
// fresh store.
//
// Nothing is mocked with mock.module(): that rewrites Bun's process-wide module
// registry for every test file that runs afterwards (it replaced the real
// session store for session.spec.ts depending on file order). Page components
// are instead stubbed on the router's route records, and put back in afterAll.
import { describe, it, expect, beforeAll, afterAll, beforeEach, spyOn } from 'bun:test'
import { createPinia, setActivePinia, type StoreGeneric } from 'pinia'
import type { RouteRecordNormalized } from 'vue-router'
import router from '../index'
import { useSessionStore, type User } from '../../stores/session'
import { useToastStore } from '../../stores/toast'

// Every route lazy-loads its page, which drags in API clients, stores and
// sub-components that have nothing to do with the guards. Swap them for a stub.
const stubPage = { name: 'RouteStub', render: () => null }
const realComponents = new Map<RouteRecordNormalized, RouteRecordNormalized['components']>()

beforeAll(() => {
  for (const record of router.getRoutes()) {
    if (!record.components) continue
    realComponents.set(record, record.components)
    record.components = Object.fromEntries(Object.keys(record.components).map((name) => [name, stubPage]))
  }
})

afterAll(() => {
  for (const [record, components] of realComponents) record.components = components
  realComponents.clear()
})

let session: StoreGeneric

function signIn(overrides: Partial<User> = {}): void {
  session.user = { id: 'u1', username: 'testuser', ...overrides } as User
}

async function push(path: string) {
  try { await router.push(path) } catch { /* redirects throw in development mode */ }
}

describe('router beforeEach guards', () => {
  beforeEach(async () => {
    // The first navigation resolves the session through /auth/me: answer 401 instead of hitting the network.
    spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ error: 'unauthorized' }), { status: 401, headers: { 'Content-Type': 'application/json' } }),
    )
    setActivePinia(createPinia())
    session = useSessionStore()
    // Reset router to a neutral state
    try { await router.push('/login') } catch { /* */ }
  })

  describe('public routes', () => {
    it('/login is accessible without authentication', async () => {
      await push('/login')
      expect(router.currentRoute.value.path).toBe('/login')
    })
  })

  describe('unknown routes', () => {
    it('resolve to the catch-all not-found page instead of a blank view', async () => {
      signIn()
      await push('/repos/some-repo/artifacts')
      expect(router.currentRoute.value.name).toBe('not-found')
    })
  })

  describe('unauthenticated user', () => {
    it('protected route redirects to /login', async () => {
      await push('/repos')
      expect(router.currentRoute.value.path).toBe('/login')
    })
  })

  describe('authenticated user', () => {
    beforeEach(() => {
      signIn()
    })

    it('/login sends a signed-in user to the dashboard', async () => {
      await push('/login?x=1')
      expect(router.currentRoute.value.path).toBe('/')
    })

    it('/login follows a safe ?next=', async () => {
      await push('/login?next=/repos')
      expect(router.currentRoute.value.path).toBe('/repos')
    })

    it.each(['//evil.example', 'https://evil.example', '/\\evil.example', '/login'])(
      '/login ignores the unsafe ?next=%s',
      async (next) => {
        await push(`/login?next=${encodeURIComponent(next)}`)
        expect(router.currentRoute.value.path).toBe('/')
      },
    )

    it('can access protected route', async () => {
      await push('/')
      expect(router.currentRoute.value.path).toBe('/')
    })

    describe('with must_change_password', () => {
      beforeEach(() => {
        signIn({ must_change_password: true })
      })

      it('visiting /change-password is allowed directly', async () => {
        await push('/change-password')
        expect(router.currentRoute.value.path).toBe('/change-password')
      })

      it('another route is redirected to /change-password and remembers where the user was headed', async () => {
        await push('/repos')
        expect(router.currentRoute.value.path).toBe('/change-password')
        expect(router.currentRoute.value.query.next).toBe('/repos')
      })

      it('the login form hands its ?next= on to /change-password', async () => {
        await push('/login?next=/repos')
        expect(router.currentRoute.value.path).toBe('/login')
        await push('/change-password')
        expect(router.currentRoute.value.query.next).toBe('/repos')
      })

      it('never carries an unsafe ?next= through /change-password', async () => {
        await push('/login?next=//evil.example')
        await push('/change-password')
        expect(router.currentRoute.value.query.next).toBeUndefined()
      })
    })
  })

  describe('session probe', () => {
    it('a failing /auth/me still lets the app render and is retried on the next navigation', async () => {
      // A fresh session: the beforeEach navigation already resolved the previous one as signed out.
      setActivePinia(createPinia())
      session = useSessionStore()
      const fetchSpy = spyOn(globalThis, 'fetch').mockRejectedValue(new Error('connection refused'))

      await push('/repos')
      expect(router.currentRoute.value.path).toBe('/login')
      expect(session.ready).toBe(true)
      expect(session.probeFailed).toBe(true)

      fetchSpy.mockResolvedValue(
        new Response(JSON.stringify({ id: 'u1', username: 'testuser' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      )
      await push('/repos')
      expect(router.currentRoute.value.path).toBe('/repos')
      expect(session.probeFailed).toBe(false)
    })

    it('does not probe again once the server said the user is signed out', async () => {
      setActivePinia(createPinia())
      session = useSessionStore()
      const fetchSpy = spyOn(globalThis, 'fetch')
      fetchSpy.mockClear()
      await push('/repos')
      await push('/search')
      expect(fetchSpy).toHaveBeenCalledTimes(1)
    })
  })

  describe('admin access', () => {
    it.each([
      ['/security', '/security'],
      ['/security/vulns', '/security?type=vuln'],
      ['/security/licenses', '/security?type=license'],
      ['/security/secrets', '/security?type=secret'],
      ['/security/ai-analysis', '/security?type=ai'],
    ])('every signed-in user can open %s (the server scopes the findings): lands on %s', async (path, landing) => {
      for (const is_admin of [false, true]) {
        signIn({ is_admin })
        await push(path)
        expect(router.currentRoute.value.fullPath).toBe(landing)
      }
    })

    it('non-admin cannot open an admin settings section, nor its legacy /admin URL', async () => {
      signIn({ is_admin: false })
      await push('/settings/users')
      expect(router.currentRoute.value.path).toBe('/')
      await push('/admin/users')
      expect(router.currentRoute.value.path).toBe('/')
    })

    it('says why a non-admin was sent home, for settings sections and admin pages', async () => {
      signIn({ is_admin: false })
      for (const path of ['/settings/users', '/bundles']) {
        const toasts = useToastStore()
        const before = toasts.queue.length
        await push(path)
        expect(router.currentRoute.value.path).toBe('/')
        expect(toasts.queue.length).toBe(before + 1)
        expect(toasts.queue[before]?.title).toContain('administrators')
      }
    })

    it('non-admin can open the personal settings sections', async () => {
      signIn({ is_admin: false })
      await push('/settings/profile')
      expect(router.currentRoute.value.path).toBe('/settings/profile')
      await push('/settings')
      expect(router.currentRoute.value.name).toBe('settings-hub')
    })

    it('admin can open an admin settings section; /admin/users redirects to it', async () => {
      signIn({ is_admin: true })
      await push('/admin/users')
      expect(router.currentRoute.value.path).toBe('/settings/users')
    })

    it('the access-token section opens on personal tokens unless a type is chosen', async () => {
      signIn({ is_admin: false })
      await push('/settings/tokens')
      expect(router.currentRoute.value.fullPath).toBe('/settings/tokens?type=personal')
      await push('/settings/tokens?type=project')
      expect(router.currentRoute.value.fullPath).toBe('/settings/tokens?type=project')
    })

    it('an unknown settings section resolves (the page shows its own not-found state)', async () => {
      signIn({ is_admin: false })
      await push('/settings/nope')
      expect(router.currentRoute.value.name).toBe('settings-section')
    })
  })
})

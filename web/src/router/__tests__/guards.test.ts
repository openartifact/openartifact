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

    it('/login stays on login page', async () => {
      await push('/login')
      expect(router.currentRoute.value.path).toBe('/login')
    })

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
    })
  })

  describe('admin access', () => {
    it('non-admin cannot access /admin/users', async () => {
      signIn({ is_admin: false })
      await push('/admin/users')
      // Should redirect away from admin route
      expect(router.currentRoute.value.path).not.toBe('/admin/users')
    })

    it('admin can access /admin/users', async () => {
      signIn({ is_admin: true })
      await push('/admin/users')
      expect(router.currentRoute.value.path).toBe('/admin/users')
    })
  })
})

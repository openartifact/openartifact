// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The app mounts before its first navigation resolves (main.ts) while the router guard waits for
// /auth/me. The shell (sidebar, header) must stay hidden until the guard has decided where the
// visitor goes: a signed-out reload otherwise shows the navigation before the login page.
import { describe, it, expect, beforeEach } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createRouter, createMemoryHistory, type RouteLocationRaw } from 'vue-router'
import { createPinia, setActivePinia } from 'pinia'
import App from '../App.vue'

const page = { template: '<div data-testid="page" />' }
const stubs = {
  AppSidebar: { template: '<aside data-testid="sidebar" />' },
  AppHeader: { template: '<header data-testid="header" />' },
}

/**
 * Mounts the app and starts its first navigation to `/`, which the guard holds until `release`
 * decides it, as the real guard holds it for /auth/me. Later navigations (a redirect) pass.
 */
async function mountPending() {
  const decision = Promise.withResolvers<true | RouteLocationRaw>()
  let first = true
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: page },
      { path: '/login', component: page, meta: { layout: 'blank' } },
    ],
  })
  router.beforeEach(() => {
    if (!first) return true
    first = false
    return decision.promise
  })
  const wrapper = mount(App, { global: { plugins: [router], stubs } })
  void router.push('/')
  await flushPromises()
  return { wrapper, router, release: decision.resolve }
}

function shellShown(wrapper: VueWrapper): boolean {
  return wrapper.find('[data-testid="sidebar"]').exists() || wrapper.find('[data-testid="header"]').exists()
}

describe('App shell', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('stays hidden while the first navigation waits for the session', async () => {
    const { wrapper } = await mountPending()
    expect(shellShown(wrapper)).toBe(false)
  })

  it('never appears for a signed-out visitor sent to the login page', async () => {
    const { wrapper, router, release } = await mountPending()
    release('/login')
    await router.isReady()
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/login')
    expect(shellShown(wrapper)).toBe(false)
  })

  it('appears once the first navigation lands on a page of the app', async () => {
    const { wrapper, router, release } = await mountPending()
    release(true)
    await router.isReady()
    await flushPromises()
    expect(shellShown(wrapper)).toBe(true)
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import RouteAnnouncer from '../RouteAnnouncer.vue'
import { useBrandingStore } from '../../stores/branding'

const Page = { template: '<p>page</p>' }

async function setup() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: Page },
      { path: '/repos', component: Page },
      { path: '/repos/:name', component: Page },
    ],
  })
  router.afterEach((to) => useBrandingStore().setPageTitle(to.path === '/repos' ? 'Repositories' : to.path))
  await router.push('/')
  await router.isReady()
  document.body.innerHTML = '<main id="main-content" tabindex="-1"></main><button id="old">old page link</button>'
  const w = mount(RouteAnnouncer, { global: { plugins: [pinia, router] }, attachTo: document.body })
  return { w, router }
}

describe('RouteAnnouncer', () => {
  it('is a polite status region that is there before the first navigation', async () => {
    const { w } = await setup()
    const el = w.get('[data-testid="route-announcer"]')
    expect(el.attributes('role')).toBe('status')
    expect(el.attributes('aria-live')).toBe('polite')
    expect(el.text()).toBe('')
    w.unmount()
  })

  it('announces the new page title when the path changes', async () => {
    const { w, router } = await setup()
    await router.push('/repos')
    await flushPromises()
    expect(w.get('[data-testid="route-announcer"]').text()).toBe('Repositories')
    w.unmount()
  })

  it('does not announce a change of query only (a tab or a filter is not a new page)', async () => {
    const { w, router } = await setup()
    await router.push('/repos')
    await flushPromises()
    await router.push({ path: '/repos', query: { tab: 'trash' } })
    await flushPromises()
    expect(w.get('[data-testid="route-announcer"]').text()).toBe('Repositories')
    w.unmount()
  })

  it('moves focus to the main area when the focused element went with the old page', async () => {
    const { w, router } = await setup()
    document.getElementById('old')!.remove()
    await router.push('/repos')
    await flushPromises()
    expect(document.activeElement?.id).toBe('main-content')
    w.unmount()
  })

  it('leaves focus alone when it is still on an element of the page (the nav link just used)', async () => {
    const { w, router } = await setup()
    const old = document.getElementById('old')!
    old.focus()
    await router.push('/repos')
    await flushPromises()
    expect(document.activeElement).toBe(old)
    w.unmount()
  })
})

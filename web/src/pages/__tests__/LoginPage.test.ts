// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import LoginPage from '../LoginPage.vue'
import { t } from '../../i18n'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const PROVIDERS = [{ slug: 'corp', kind: 'oidc', display_name: 'Corp SSO' }]

async function mountLogin(path: string, providers: unknown[] = PROVIDERS) {
  setActivePinia(createPinia())
  spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
    if (String(input).includes('/auth/sso/providers')) return json({ providers })
    return json({})
  }) as never)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/login', name: 'login', component: LoginPage },
      { path: '/:rest(.*)*', component: { template: '<div/>' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const w = mount(LoginPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { w, router }
}

describe('LoginPage SSO', () => {
  it('shows a failed SSO login in the catalog language and drops sso_error from the address', async () => {
    const { w, router } = await mountLogin('/login?sso_error=user_not_provisioned&next=%2Frepos%2Ffoo')
    expect(w.get('[role="alert"]').text()).toBe(t('login.ssoError.notProvisioned'))
    expect(router.currentRoute.value.query).toEqual({ next: '/repos/foo' })
    w.unmount()
  })

  it('never shows the raw value of an unknown sso_error', async () => {
    const { w } = await mountLogin('/login?sso_error=%3Cb%3Eboom%3C%2Fb%3E')
    const alert = w.get('[role="alert"]')
    expect(alert.text()).toBe(t('login.ssoError.generic'))
    expect(alert.html()).not.toContain('boom')
    w.unmount()
  })

  it('keeps the page the user came for: the SSO link carries it as ?redirect=', async () => {
    const { w } = await mountLogin('/login?next=%2Frepos%2Ffoo%3Ftab%3Dfiles')
    expect(w.get('a[href^="/api/v1/auth/sso/"]').attributes('href')).toBe(
      '/api/v1/auth/sso/oidc/corp/login?redirect=' + encodeURIComponent('/repos/foo?tab=files'),
    )
    w.unmount()
  })

  it('has a plain SSO link without next, and ignores an unsafe next', async () => {
    for (const path of ['/login', '/login?next=https%3A%2F%2Fevil.example%2F']) {
      const { w } = await mountLogin(path)
      expect(w.get('a[href^="/api/v1/auth/sso/"]').attributes('href')).toBe('/api/v1/auth/sso/oidc/corp/login')
      w.unmount()
    }
  })
})

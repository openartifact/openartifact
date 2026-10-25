// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The AGPL attribution must show up whatever the branding says: on the login
// page and in the About dialog, with a normal payload, a hostile one, and none.
import { describe, expect, it, spyOn } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import LoginPage from '../../pages/LoginPage.vue'
import AboutDialog from '../AboutDialog.vue'
import { useBrandingStore } from '../../stores/branding'
import { useSessionStore } from '../../stores/session'

const UPSTREAM = 'https://github.com/openartifact/openartifact'
const json = (body: unknown): Response => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

function serve(): void {
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    const path = new URL(String(input), 'http://localhost').pathname
    if (path === '/api/v1/system/info') return json({ version: '1.2.3', docs_url: 'https://docs.example.com/v1.2/', api_reference_enabled: true })
    if (path === '/api/v1/admin/system/info') return json({ version: '1.2.3', commit: '0123456789abcdef' })
    if (path === '/api/v1/auth/sso/providers') return json({ providers: [] })
    return json({})
  }) as never)
}

const PAYLOADS: [string, unknown][] = [
  ['none', null],
  ['custom', { product_name: 'Acme Hub', source_url: 'https://git.acme.test/oa', support_url: 'https://help.acme.test' }],
  ['empty source and weird name', { product_name: '<img src=x onerror=alert(1)> Powered by Nobody', source_url: '', login_message: '[x](javascript:alert(1))' }],
  ['hostile urls', { product_name: 'X', source_url: 'javascript:alert(1)', support_url: 'javascript:alert(1)' }],
]

async function mountLogin(payload: unknown) {
  serve()
  const pinia = createPinia()
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/login')
  const wrapper = mount(LoginPage, { global: { plugins: [pinia, router] } })
  if (payload) useBrandingStore(pinia).set(payload)
  await flushPromises()
  return { wrapper, pinia }
}

describe('login page attribution', () => {
  for (const [label, payload] of PAYLOADS) {
    it(`shows Powered by OpenArtifact, the version and a source link (${label})`, async () => {
      const { wrapper } = await mountLogin(payload)
      const text = wrapper.get('[data-testid="powered-by-text"]').text()
      expect(text).toBe('Powered by OpenArtifact v1.2.3')
      const link = wrapper.get('[data-testid="source-link"]')
      const expected = (payload as { source_url?: string } | null)?.source_url
      expect(link.attributes('href')).toBe(expected && expected.startsWith('https://') ? expected : UPSTREAM)
      expect(link.attributes('target')).toBe('_blank')
      expect(link.attributes('rel')).toBe('noopener noreferrer')
      expect(wrapper.text()).toContain('AGPL-3.0-or-later')
    })
  }

  it('names the instance in the heading and renders the login message without html', async () => {
    const { wrapper } = await mountLogin({ product_name: 'Acme Hub', login_message: 'Use **SSO**. <img src=x onerror=alert(1)>' })
    expect(wrapper.get('h2').text()).toBe('Sign in to Acme Hub')
    const msg = wrapper.get('[data-testid="login-message"]')
    expect(msg.find('strong').text()).toBe('SSO')
    expect(msg.find('img').exists()).toBe(false)
    expect(msg.text()).toContain('<img src=x onerror=alert(1)>')
  })

  it('does not call a renamed instance OpenArtifact outside the attribution', async () => {
    const { wrapper } = await mountLogin({ product_name: 'Acme Hub' })
    const clone = wrapper.element.cloneNode(true) as HTMLElement
    clone.querySelector('[data-testid="powered-by"]')?.remove()
    expect(clone.textContent).not.toContain('OpenArtifact')
    expect(clone.querySelector('img[alt="OpenArtifact"]')).toBeNull()
  })
})

describe('About dialog attribution', () => {
  for (const [label, payload] of PAYLOADS) {
    it(`always carries the licence, source and upstream links (${label})`, async () => {
      serve()
      const pinia = createPinia()
      if (payload) useBrandingStore(pinia).set(payload)
      const session = useSessionStore(pinia)
      session.user = { id: 'u1', username: 'admin', is_admin: true } as never
      mount(AboutDialog, { props: { open: true }, global: { plugins: [pinia] }, attachTo: document.body })
      await flushPromises()

      const body = document.body
      expect(body.querySelector('[data-testid="about-powered-by"]')!.textContent).toBe('Powered by OpenArtifact v1.2.3 (0123456)')
      expect(body.querySelector('[data-testid="about-license"]')!.textContent).toBe('Licensed under the GNU AGPL-3.0-or-later')
      const source = body.querySelector<HTMLAnchorElement>('[data-testid="about-source"]')!
      const expected = (payload as { source_url?: string } | null)?.source_url
      expect(source.getAttribute('href')).toBe(expected && expected.startsWith('https://') ? expected : UPSTREAM)
      expect(source.getAttribute('rel')).toBe('noopener noreferrer')
      expect(body.querySelector('[data-testid="about-upstream"]')!.getAttribute('href')).toBe(UPSTREAM)
      expect(body.querySelector('[data-testid="about-docs"]')!.getAttribute('href')).toBe('https://docs.example.com/v1.2/')
      expect(body.querySelector('[data-testid="about-api-reference"]')!.getAttribute('href')).toBe('/api/reference')
      const support = body.querySelector('[data-testid="about-support"]')
      expect(support?.getAttribute('href') ?? null).toBe((payload as { support_url?: string } | null)?.support_url === 'https://help.acme.test' ? 'https://help.acme.test' : null)
    })
  }

  it('names the instance in the title but not in the attribution line', async () => {
    serve()
    const pinia = createPinia()
    useBrandingStore(pinia).set({ product_name: 'Acme Hub' })
    mount(AboutDialog, { props: { open: true }, global: { plugins: [pinia] }, attachTo: document.body })
    await flushPromises()
    expect(document.body.querySelector('h2')!.textContent).toBe('About Acme Hub')
    expect(document.body.querySelector('[data-testid="about-powered-by"]')!.textContent).toContain('Powered by OpenArtifact')
  })
})

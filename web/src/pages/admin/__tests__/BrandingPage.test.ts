// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import BrandingPage from '../BrandingPage.vue'
import { normalizeBranding } from '../../../lib/branding'
import type { BrandingAdmin } from '../../../api/branding'
import { useBrandingStore } from '../../../stores/branding'
import { useToastStore } from '../../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const NO_OVERRIDES = { product_name: false, primary_color: false, login_message: false, support_url: false, source_url: false, logo_light: false, logo_dark: false, favicon: false }

function state(over: Record<string, unknown> = {}, overrides: Partial<typeof NO_OVERRIDES> = {}): BrandingAdmin {
  return {
    effective: normalizeBranding({ product_name: 'OpenArtifact', ...over })!,
    defaults: { product_name: 'OpenArtifact', primary_color: '', login_message: '', support_url: '', source_url: 'https://github.com/openartifact/openartifact' },
    overrides: { ...NO_OVERRIDES, ...overrides },
  }
}

const THEME = {
  light: { primary: '#0f766e', primary_contrast: '#ffffff', primary_soft: '#ccfbf1' },
  dark: { primary: '#5eead4', primary_contrast: '#042f2e', primary_soft: '#134e4a' },
}

let fetchSpy: Mock<typeof fetch>
let current: BrandingAdmin

function calls(method: string): [string, RequestInit][] {
  return fetchSpy.mock.calls
    .filter((c) => ((c[1] as RequestInit | undefined)?.method ?? 'GET') === method)
    .map((c) => [String(c[0]), c[1] as RequestInit])
}

async function mountPage(initial: BrandingAdmin, reply?: (path: string, init: RequestInit) => BrandingAdmin | Response) {
  current = initial
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), 'http://localhost').pathname
    const method = init?.method ?? 'GET'
    if (!path.startsWith('/api/v1/admin/branding')) return json({})
    if (method === 'GET') return json(current)
    const out = reply?.(path, init!) ?? current
    if (out instanceof Response) return out
    current = out
    return json(out)
  }) as never)
  const pinia = createPinia()
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/admin/branding')
  const wrapper = mount(BrandingPage, { global: { plugins: [pinia, router] }, attachTo: document.body })
  await flushPromises()
  return { wrapper, pinia }
}

const field = (w: VueWrapper, name: string) => w.get(`[data-testid="field-${name}"]`)

describe('BrandingPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('shows each setting as overridden or default, with the default value', async () => {
    const { wrapper } = await mountPage(state({ product_name: 'Acme' }, { product_name: true }))
    expect(field(wrapper, 'product_name').get('[data-testid="field-state"]').text()).toBe('Overridden')
    expect(field(wrapper, 'product_name').find('[data-testid="field-reset"]').exists()).toBe(true)
    expect(field(wrapper, 'product_name').text()).toContain('Default: OpenArtifact')
    expect(field(wrapper, 'support_url').get('[data-testid="field-state"]').text()).toBe('Default')
    expect(field(wrapper, 'support_url').find('[data-testid="field-reset"]').exists()).toBe(false)
  })

  it('saves only the changed fields and refreshes the live branding', async () => {
    const { wrapper, pinia } = await mountPage(state(), () => state({ product_name: 'Acme Hub', primary_color: '#0f766e', theme: THEME }, { product_name: true, primary_color: true }))
    await wrapper.get('#branding-name').setValue('Acme Hub')
    await wrapper.get('#branding-color').setValue('#0F766E')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()

    const [path, init] = calls('PUT')[0]!
    expect(path).toBe('/api/v1/admin/branding')
    expect(JSON.parse(init.body as string)).toEqual({ product_name: 'Acme Hub', primary_color: '#0f766e' })
    expect(useBrandingStore(pinia).productName).toBe('Acme Hub')
    expect(useToastStore(pinia).visible.some((x) => x.level === 'success')).toBe(true)
    // the contrast-adjusted colours of the server answer are shown
    expect(wrapper.get('[data-testid="swatch-dark"]').text()).toContain('#5eead4')
    expect(wrapper.get('[data-testid="swatch-light"]').text()).toContain('#0f766e')
  })

  it('does not send anything for an invalid colour and shows the error', async () => {
    const { wrapper } = await mountPage(state())
    await wrapper.get('#branding-color').setValue('teal')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(calls('PUT')).toHaveLength(0)
    expect(field(wrapper, 'primary_color').get('[role="alert"]').text()).toBe('Use a colour in the form #rrggbb.')
  })

  it('validates the name and the source url', async () => {
    const { wrapper } = await mountPage(state())
    await wrapper.get('#branding-name').setValue('')
    await wrapper.get('#branding-source').setValue('javascript:alert(1)')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(calls('PUT')).toHaveLength(0)
    expect(field(wrapper, 'product_name').get('[role="alert"]').text()).toBe('The product name is required.')
    expect(field(wrapper, 'source_url').get('[role="alert"]').text()).toBe('Use an http(s) URL.')
  })

  it('sends null for an emptied colour', async () => {
    const { wrapper } = await mountPage(state({ primary_color: '#0f766e', theme: THEME }, { primary_color: true }))
    await wrapper.get('#branding-color').setValue('')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(JSON.parse(calls('PUT')[0]![1].body as string)).toEqual({ primary_color: null })
  })

  it('renders the login message preview without html', async () => {
    const { wrapper } = await mountPage(state())
    await wrapper.get('#branding-message').setValue('**Hi** <img src=x onerror=alert(1)> [x](javascript:alert(1))')
    const preview = wrapper.get('[data-testid="message-preview"]')
    expect(preview.find('strong').text()).toBe('Hi')
    expect(preview.find('img').exists()).toBe(false)
    expect(preview.find('a').exists()).toBe(false)
  })

  it('resets one field with a null PUT and takes the default back into the form', async () => {
    const { wrapper } = await mountPage(state({ product_name: 'Acme' }, { product_name: true }), () => state())
    await field(wrapper, 'product_name').get('[data-testid="field-reset"]').trigger('click')
    await flushPromises()
    expect(JSON.parse(calls('PUT')[0]![1].body as string)).toEqual({ product_name: null })
    expect((wrapper.get('#branding-name').element as HTMLInputElement).value).toBe('OpenArtifact')
  })

  it('resets everything only after the confirmation', async () => {
    const { wrapper } = await mountPage(state({ product_name: 'Acme' }, { product_name: true }), () => state())
    await wrapper.get('[data-testid="reset-all"]').trigger('click')
    await flushPromises()
    expect(calls('DELETE')).toHaveLength(0)

    const confirm = [...document.body.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Reset to defaults' && b.closest('[role="dialog"]'))!
    confirm.click()
    await flushPromises()
    expect(calls('DELETE')[0]![0]).toBe('/api/v1/admin/branding')
    expect((wrapper.get('#branding-name').element as HTMLInputElement).value).toBe('OpenArtifact')
  })

  async function pick(wrapper: VueWrapper, asset: string, file: File): Promise<void> {
    const input = wrapper.get(`[data-testid="file-${asset}"]`).element as HTMLInputElement
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    await wrapper.get(`[data-testid="file-${asset}"]`).trigger('change')
    await flushPromises()
  }

  it('uploads an image as a raw body with its content type', async () => {
    const { wrapper } = await mountPage(state(), () => state({ logo_light_url: '/api/v1/branding/logo-light?v=2' }, { logo_light: true }))
    const file = new File([new Uint8Array([137, 80, 78, 71])], 'logo.png', { type: 'image/png' })
    await pick(wrapper, 'logo-light', file)

    const [path, init] = calls('PUT')[0]!
    expect(path).toBe('/api/v1/admin/branding/logo-light')
    expect(new Headers(init.headers).get('Content-Type')).toBe('image/png')
    expect(init.body).toBe(file)
    expect(wrapper.get('[data-testid="image-logo-light"] [data-testid="image-preview"]').attributes('src')).toBe('/api/v1/branding/logo-light?v=2')
  })

  it('refuses a too large or wrongly typed image before sending it', async () => {
    const { wrapper } = await mountPage(state())
    await pick(wrapper, 'favicon', new File([new Uint8Array(512 * 1024 + 1)], 'big.png', { type: 'image/png' }))
    expect(wrapper.get('[data-testid="image-favicon"] [data-testid="image-error"]').text()).toBe('The image is larger than 512 KiB.')
    await pick(wrapper, 'favicon', new File(['x'], 'a.gif', { type: 'image/gif' }))
    expect(wrapper.get('[data-testid="image-favicon"] [data-testid="image-error"]').text()).toBe('Only PNG and SVG images are accepted.')
    expect(calls('PUT')).toHaveLength(0)
  })

  it('removes an uploaded image', async () => {
    const { wrapper } = await mountPage(state({ favicon_url: '/api/v1/branding/favicon?v=1' }, { favicon: true }), () => state())
    await wrapper.get('[data-testid="image-favicon"] [data-testid="image-remove"]').trigger('click')
    await flushPromises()
    expect(calls('DELETE')[0]![0]).toBe('/api/v1/admin/branding/favicon')
    expect(wrapper.find('[data-testid="image-favicon"] [data-testid="image-preview"]').exists()).toBe(false)
  })

  it('shows a server error when saving fails', async () => {
    const { wrapper, pinia } = await mountPage(state(), () => json({ error: 'primary_color: invalid' }, 400))
    await wrapper.get('#branding-name').setValue('Acme')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    const toast = useToastStore(pinia).visible.find((x) => x.level === 'error')
    expect(toast?.description).toBe('primary_color: invalid')
  })
})

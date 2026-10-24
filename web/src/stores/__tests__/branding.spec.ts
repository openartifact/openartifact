// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, spyOn } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { useBrandingStore } from '../branding'

const THEME = {
  light: { primary: '#0f766e', primary_contrast: '#ffffff', primary_soft: '#ccfbf1' },
  dark: { primary: '#5eead4', primary_contrast: '#042f2e', primary_soft: '#134e4a' },
}

const BRANDED = {
  product_name: 'Acme Hub',
  primary_color: '#0f766e',
  theme: THEME,
  logo_light_url: '/api/v1/branding/logo-light?v=1',
  logo_dark_url: '',
  favicon_url: '/api/v1/branding/favicon?v=1',
  login_message: 'hi',
  support_url: '',
  source_url: 'https://git.acme.test/oa',
  revision: '1',
}

function setupHead(): void {
  document.head.insertAdjacentHTML(
    'beforeend',
    '<title>OpenArtifact</title>' +
      '<meta name="apple-mobile-web-app-title" content="OpenArtifact" />' +
      '<link rel="icon" type="image/svg+xml" href="/favicon.svg" />' +
      '<link rel="apple-touch-icon" href="/icons/apple-touch-icon.png" />',
  )
}

function cleanHead(): void {
  document.head.querySelectorAll('title, meta[name="apple-mobile-web-app-title"], link[rel], #oa-brand-style, #oa-branding').forEach((el) => el.remove())
}

const icon = () => document.querySelector<HTMLLinkElement>('link[rel="icon"]')!
const style = () => document.getElementById('oa-brand-style')

describe('branding store', () => {
  beforeEach(() => {
    setupHead()
    setActivePinia(createPinia())
  })
  afterEach(cleanHead)

  it('falls back to the built-in values before anything is known', () => {
    const store = useBrandingStore()
    expect(store.productName).toBe('OpenArtifact')
    expect(store.sourceUrl).toBe('https://github.com/openartifact/openartifact')
    expect(store.logoFor('light')).toBe('')
    expect(store.hasCustomLogo).toBe(false)
  })

  it('applies title, apple title, favicon, colours and the offline entry', () => {
    const store = useBrandingStore()
    store.set(BRANDED)

    expect(document.title).toBe('Acme Hub')
    expect(document.querySelector('meta[name="apple-mobile-web-app-title"]')!.getAttribute('content')).toBe('Acme Hub')
    expect(icon().getAttribute('href')).toBe('/api/v1/branding/favicon?v=1')
    expect(icon().hasAttribute('type')).toBe(false)
    expect(document.querySelector('link[rel="apple-touch-icon"]')!.getAttribute('href')).toBe('/api/v1/branding/favicon?v=1')
    expect(style()!.textContent).toBe(
      ':root{--color-primary:15 118 110;--color-primary-contrast:255 255 255;--color-primary-soft:204 251 241}' +
        '.dark{--color-primary:94 234 212;--color-primary-contrast:4 47 46;--color-primary-soft:19 78 74}',
    )
    expect(JSON.parse(localStorage.getItem('oa.branding')!)).toMatchObject({ product_name: 'Acme Hub', primary_color: '#0f766e' })
  })

  it('reuses the server-written style element and keeps it last in <head>', () => {
    const el = document.createElement('style')
    el.id = 'oa-brand-style'
    el.textContent = 'old'
    document.head.appendChild(el)
    document.head.appendChild(document.createElement('title'))

    useBrandingStore().set(BRANDED)
    expect(document.querySelectorAll('#oa-brand-style')).toHaveLength(1)
    expect(style()!.textContent).toContain('--color-primary:15 118 110')
    expect(document.head.lastElementChild).toBe(style())
  })

  it('removes the overrides and restores the shell icon when branding goes back to the defaults', () => {
    const store = useBrandingStore()
    store.set(BRANDED)
    store.set({ ...BRANDED, product_name: 'OpenArtifact', primary_color: '', theme: null, favicon_url: '' })

    expect(document.title).toBe('OpenArtifact')
    expect(style()!.textContent).toBe('')
    expect(icon().getAttribute('href')).toBe('/favicon.svg')
    expect(icon().getAttribute('type')).toBe('image/svg+xml')
    expect(document.querySelector('link[rel="apple-touch-icon"]')!.getAttribute('href')).toBe('/icons/apple-touch-icon.png')
    expect(localStorage.getItem('oa.branding')).toBeNull()
  })

  it('composes the title with the current page', () => {
    const store = useBrandingStore()
    store.set(BRANDED)
    store.setPageTitle('Repositories')
    expect(document.title).toBe('Repositories · Acme Hub')
    store.setPageTitle('')
    expect(document.title).toBe('Acme Hub')
  })

  it('logoFor serves a single uploaded logo for both themes', () => {
    const store = useBrandingStore()
    store.set(BRANDED)
    expect(store.logoFor('light')).toBe('/api/v1/branding/logo-light?v=1')
    expect(store.logoFor('dark')).toBe('/api/v1/branding/logo-light?v=1')
    expect(store.hasCustomLogo).toBe(true)
  })

  it('boots from the embedded payload and survives a failed revalidation', async () => {
    const el = document.createElement('script')
    el.type = 'application/json'
    el.id = 'oa-branding'
    el.textContent = JSON.stringify(BRANDED)
    document.head.appendChild(el)
    spyOn(globalThis, 'fetch').mockRejectedValue(new TypeError('offline'))

    const store = useBrandingStore()
    store.initFromDocument()
    expect(document.title).toBe('Acme Hub')

    await store.refresh()
    expect(store.productName).toBe('Acme Hub')
  })

  it('revalidates through GET /api/v1/branding and re-applies', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ ...BRANDED, product_name: 'Acme Two' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )
    const store = useBrandingStore()
    store.set(BRANDED)
    await store.refresh()
    expect(String(fetchSpy.mock.calls[0]![0])).toBe('/api/v1/branding')
    expect(document.title).toBe('Acme Two')
  })

  it('ignores a payload that is not an object', () => {
    const store = useBrandingStore()
    store.set(BRANDED)
    store.set('garbage')
    expect(store.productName).toBe('Acme Hub')
  })
})

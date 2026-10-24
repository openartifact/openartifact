// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import BrandLogo from '../BrandLogo.vue'
import { useBrandingStore } from '../../stores/branding'

function base(over: Record<string, unknown> = {}) {
  return { product_name: 'OpenArtifact', source_url: 'https://github.com/openartifact/openartifact', ...over }
}

function mountLogo(branding: unknown, props: { variant?: 'horizontal' | 'stacked' | 'mark'; size?: number } = {}) {
  const pinia = createPinia()
  setActivePinia(pinia)
  if (branding) useBrandingStore().set(branding)
  return mount(BrandLogo, { props, global: { plugins: [pinia] } })
}

describe('BrandLogo', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('shows the built-in lockup, light and dark, for the default name', () => {
    const w = mountLogo(base())
    expect(w.attributes('data-brand-logo')).toBe('lockup')
    const imgs = w.findAll('img')
    expect(imgs).toHaveLength(2)
    expect(imgs[0]!.classes()).toContain('dark:hidden')
    expect(imgs[1]!.classes()).toContain('dark:block')
    expect(imgs[0]!.attributes('alt')).toBe('OpenArtifact')
    expect(imgs[0]!.attributes('src')).not.toBe(imgs[1]!.attributes('src'))
  })

  it('shows the built-in lockup when no branding is known at all', () => {
    expect(mountLogo(null).attributes('data-brand-logo')).toBe('lockup')
  })

  it('stacked uses another artwork than horizontal', () => {
    const h = mountLogo(base(), { variant: 'horizontal' }).find('img').attributes('src')
    const s = mountLogo(base(), { variant: 'stacked' }).find('img').attributes('src')
    expect(h).not.toBe(s)
  })

  it('uses the custom logo pair, switched by the dark class', () => {
    const w = mountLogo(base({ product_name: 'Acme', logo_light_url: '/api/v1/branding/logo-light?v=1', logo_dark_url: '/api/v1/branding/logo-dark?v=1' }))
    expect(w.attributes('data-brand-logo')).toBe('custom')
    const imgs = w.findAll('img')
    expect(imgs.map((i) => i.attributes('src'))).toEqual(['/api/v1/branding/logo-light?v=1', '/api/v1/branding/logo-dark?v=1'])
    expect(imgs[0]!.classes()).toContain('dark:hidden')
    expect(imgs[1]!.classes()).toContain('dark:block')
    expect(imgs[0]!.attributes('alt')).toBe('Acme')
  })

  it('renders one image when both themes share the logo', () => {
    const w = mountLogo(base({ logo_light_url: '/api/v1/branding/logo-light?v=1', logo_dark_url: '/api/v1/branding/logo-light?v=1' }))
    expect(w.findAll('img')).toHaveLength(1)
  })

  it('fits a custom horizontal logo to the header height whatever its aspect ratio', () => {
    // A square logo must not blow past the sidebar header: height is fixed, width follows.
    const style = mountLogo(base({ product_name: 'Acme', logo_light_url: '/api/v1/branding/logo-light?v=1' })).find('img').attributes('style')
    expect(style).toContain('height: 36px')
    expect(style).toContain('width: auto')
  })

  it('shows a custom logo even under the default name, and never the built-in wordmark', () => {
    const w = mountLogo(base({ logo_dark_url: '/api/v1/branding/logo-dark?v=1' }))
    expect(w.attributes('data-brand-logo')).toBe('custom')
    expect(w.findAll('img').every((i) => i.attributes('src')!.startsWith('/api/v1/branding/'))).toBe(true)
  })

  it('shows the built-in mark plus the name as text for a renamed instance without a logo', () => {
    const w = mountLogo(base({ product_name: 'Acme Hub' }), { variant: 'horizontal' })
    expect(w.attributes('data-brand-logo')).toBe('mark-name')
    expect(w.text()).toBe('Acme Hub')
    expect(w.findAll('img').every((i) => i.attributes('aria-hidden') === 'true')).toBe(true)
  })

  it('renders only the mark (with an accessible name) for the mark variant', () => {
    const w = mountLogo(base({ product_name: 'Acme Hub' }), { variant: 'mark' })
    expect(w.text()).toBe('Acme Hub')
    expect(w.find('.sr-only').exists()).toBe(true)
    const defaults = mountLogo(base(), { variant: 'mark' })
    expect(defaults.attributes('data-brand-logo')).toBe('mark-name')
  })

  it('uses the favicon for the mark variant of a custom setup', () => {
    const w = mountLogo(base({ logo_light_url: '/api/v1/branding/logo-light?v=1', favicon_url: '/api/v1/branding/favicon?v=1' }), { variant: 'mark' })
    expect(w.find('img').attributes('src')).toBe('/api/v1/branding/favicon?v=1')
  })

  it('never renders below the brand minimum sizes', () => {
    const lockup = mountLogo(base(), { variant: 'horizontal', size: 40 }).find('img')
    expect(lockup.attributes('style')).toContain('width: 120px')
    const mark = mountLogo(base(), { variant: 'mark', size: 4 }).find('span span')
    expect(mark.attributes('style')).toContain('width: 16px')
    const custom = mountLogo(base({ favicon_url: '/api/v1/branding/favicon?v=1', logo_light_url: '/api/v1/branding/logo-light?v=1' }), { variant: 'mark', size: 4 }).find('img')
    expect(custom.attributes('style')).toContain('width: 16px')
  })

  it('does not take the wordmark from an unsafe logo url', () => {
    const w = mountLogo(base({ product_name: 'Acme', logo_light_url: 'javascript:alert(1)' }))
    expect(w.attributes('data-brand-logo')).toBe('mark-name')
  })
})

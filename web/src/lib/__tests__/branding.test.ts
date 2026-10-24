// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it } from 'bun:test'
import {
  DEFAULT_PRODUCT_NAME,
  DEFAULT_SOURCE_URL,
  buildBrandCss,
  composeTitle,
  hexToTriple,
  logoSize,
  normalizeBranding,
  offlineBrand,
  readEmbeddedBranding,
  safeImageUrl,
  safeUrl,
} from '../branding'

const palette = (primary: string) => ({ primary, primary_contrast: '#ffffff', primary_soft: '#e0e7ff' })

describe('hexToTriple', () => {
  it('converts #rrggbb to the space-separated form of the CSS variables', () => {
    expect(hexToTriple('#4f46e5')).toBe('79 70 229')
    expect(hexToTriple('#FFFFFF')).toBe('255 255 255')
    expect(hexToTriple('#000000')).toBe('0 0 0')
  })

  it('rejects anything else', () => {
    for (const bad of ['', '4f46e5', '#fff', '#4f46e5ff', '#gggggg', 'red', 'rgb(1 2 3)', '#4f46e5;}body{display:none']) {
      expect(hexToTriple(bad)).toBeNull()
    }
  })
})

describe('buildBrandCss', () => {
  it('writes both themes', () => {
    expect(buildBrandCss({ light: palette('#4f46e5'), dark: palette('#818cf8') })).toBe(
      ':root{--color-primary:79 70 229;--color-primary-contrast:255 255 255;--color-primary-soft:224 231 255}' +
        '.dark{--color-primary:129 140 248;--color-primary-contrast:255 255 255;--color-primary-soft:224 231 255}',
    )
  })

  it('is empty without a theme or with an invalid colour', () => {
    expect(buildBrandCss(null)).toBe('')
    expect(buildBrandCss(undefined)).toBe('')
    expect(buildBrandCss({ light: palette('#4f46e5'), dark: palette('x}') })).toBe('')
  })
})

describe('composeTitle', () => {
  it('puts the page before the product name', () => {
    expect(composeTitle('Repositories', 'Acme Hub')).toBe('Repositories · Acme Hub')
  })

  it('is the product name alone without a page', () => {
    expect(composeTitle('', 'Acme Hub')).toBe('Acme Hub')
    expect(composeTitle('  ', 'Acme Hub')).toBe('Acme Hub')
    expect(composeTitle(null, 'Acme Hub')).toBe('Acme Hub')
  })
})

describe('safeUrl / safeImageUrl', () => {
  it('keeps http(s), and mailto only for contact links', () => {
    expect(safeUrl('https://example.com/a')).toBe('https://example.com/a')
    expect(safeUrl('mailto:ops@example.com')).toBe('')
    expect(safeUrl('mailto:ops@example.com', 'contact')).toBe('mailto:ops@example.com')
  })

  it('drops dangerous, relative and non-string values', () => {
    for (const bad of ['javascript:alert(1)', 'data:text/html,x', '//evil.example', '/relative', 'example.com', '', 42, null]) {
      expect(safeUrl(bad, 'contact')).toBe('')
    }
  })

  it('accepts same-origin paths for images but not protocol-relative ones', () => {
    expect(safeImageUrl('/api/v1/branding/logo-light?v=ab12')).toBe('/api/v1/branding/logo-light?v=ab12')
    expect(safeImageUrl('//evil.example/x.png')).toBe('')
    expect(safeImageUrl('javascript:alert(1)')).toBe('')
  })
})

describe('logoSize', () => {
  it('uses the default and never goes under the brand minimum', () => {
    expect(logoSize('horizontal')).toBe(144)
    expect(logoSize('horizontal', 80)).toBe(120)
    expect(logoSize('mark', 8)).toBe(16)
    expect(logoSize('mark', 40)).toBe(40)
    expect(logoSize('mark', Number.NaN)).toBe(32)
  })
})

describe('normalizeBranding', () => {
  it('is null for non-objects', () => {
    for (const bad of [null, undefined, 'x', 3, [], true]) expect(normalizeBranding(bad)).toBeNull()
  })

  it('falls back to the defaults on an empty or malformed payload', () => {
    const b = normalizeBranding({ product_name: 42, source_url: '', primary_color: 'blue', theme: { light: 1 } })!
    expect(b.product_name).toBe(DEFAULT_PRODUCT_NAME)
    expect(b.source_url).toBe(DEFAULT_SOURCE_URL)
    expect(b.primary_color).toBe('')
    expect(b.theme).toBeNull()
  })

  it('scheme-checks the URLs', () => {
    const b = normalizeBranding({
      product_name: 'Acme',
      source_url: 'javascript:alert(1)',
      support_url: 'javascript:alert(1)',
      logo_light_url: 'javascript:alert(1)',
      favicon_url: '/api/v1/branding/favicon?v=1',
    })!
    expect(b.source_url).toBe(DEFAULT_SOURCE_URL)
    expect(b.support_url).toBe('')
    expect(b.logo_light_url).toBe('')
    expect(b.favicon_url).toBe('/api/v1/branding/favicon?v=1')
  })
})

describe('readEmbeddedBranding', () => {
  afterEach(() => document.getElementById('oa-branding')?.remove())

  function embed(text: string): void {
    const el = document.createElement('script')
    el.type = 'application/json'
    el.id = 'oa-branding'
    el.textContent = text
    document.head.appendChild(el)
  }

  it('reads the server-embedded JSON', () => {
    embed(JSON.stringify({ product_name: 'Acme', source_url: 'https://git.acme.test/oa' }))
    expect(readEmbeddedBranding()?.product_name).toBe('Acme')
  })

  it('is null when absent or not JSON', () => {
    expect(readEmbeddedBranding()).toBeNull()
    embed('{not json')
    expect(readEmbeddedBranding()).toBeNull()
  })
})

describe('offlineBrand', () => {
  it('is null for the built-in look', () => {
    expect(offlineBrand(normalizeBranding({})!)).toBeNull()
  })

  it('carries the name and the per-theme colours', () => {
    const b = normalizeBranding({
      product_name: 'Acme',
      primary_color: '#112233',
      theme: { light: palette('#112233'), dark: palette('#aabbcc') },
    })!
    expect(offlineBrand(b)).toEqual({
      product_name: 'Acme',
      primary_color: '#112233',
      light: { primary: '#112233', primary_contrast: '#ffffff' },
      dark: { primary: '#aabbcc', primary_contrast: '#ffffff' },
    })
  })
})

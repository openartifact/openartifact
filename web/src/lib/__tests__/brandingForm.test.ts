// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { BrandingAdmin } from '../../api/branding'
import { normalizeBranding } from '../branding'
import { diffBranding, formFromAdmin, panelVars, previewVars, validateBrandingForm, validateImageFile } from '../brandingForm'

function admin(over: Record<string, unknown> = {}): BrandingAdmin {
  return {
    effective: normalizeBranding({ product_name: 'OpenArtifact', ...over })!,
    defaults: { product_name: 'OpenArtifact', primary_color: '', login_message: '', support_url: '', source_url: 'https://github.com/openartifact/openartifact' },
    overrides: { product_name: false, primary_color: false, login_message: false, support_url: false, source_url: false, logo_light: false, logo_dark: false, favicon: false },
  }
}

describe('validateBrandingForm', () => {
  const ok = formFromAdmin(admin())

  it('accepts the defaults', () => {
    expect(validateBrandingForm(ok)).toEqual({})
  })

  it('checks the product name', () => {
    expect(validateBrandingForm({ ...ok, product_name: '  ' }).product_name).toBe('adm.branding.error.nameRequired')
    expect(validateBrandingForm({ ...ok, product_name: 'x'.repeat(65) }).product_name).toBe('adm.branding.error.nameLength')
    expect(validateBrandingForm({ ...ok, product_name: 'a\u0007b' }).product_name).toBe('adm.branding.error.nameControl')
    expect(validateBrandingForm({ ...ok, product_name: 'x'.repeat(64) }).product_name).toBeUndefined()
  })

  it('checks the colour', () => {
    for (const bad of ['red', '#fff', '#12345g', '4f46e5']) {
      expect(validateBrandingForm({ ...ok, primary_color: bad }).primary_color).toBe('adm.branding.error.color')
    }
    expect(validateBrandingForm({ ...ok, primary_color: '#4F46E5' }).primary_color).toBeUndefined()
    expect(validateBrandingForm({ ...ok, primary_color: '' }).primary_color).toBeUndefined()
  })

  it('checks the URLs and the message length', () => {
    expect(validateBrandingForm({ ...ok, support_url: 'javascript:alert(1)' }).support_url).toBe('adm.branding.error.supportUrl')
    expect(validateBrandingForm({ ...ok, support_url: 'mailto:help@example.com' }).support_url).toBeUndefined()
    expect(validateBrandingForm({ ...ok, support_url: '' }).support_url).toBeUndefined()
    expect(validateBrandingForm({ ...ok, source_url: '' }).source_url).toBe('adm.branding.error.sourceRequired')
    expect(validateBrandingForm({ ...ok, source_url: 'mailto:a@b.c' }).source_url).toBe('adm.branding.error.sourceUrl')
    expect(validateBrandingForm({ ...ok, login_message: 'x'.repeat(2001) }).login_message).toBe('adm.branding.error.messageLength')
  })
})

describe('diffBranding', () => {
  it('sends only what changed and null for an emptied colour', () => {
    const a = admin({ primary_color: '#112233' })
    const form = formFromAdmin(a)
    expect(diffBranding(form, a)).toEqual({})
    expect(diffBranding({ ...form, product_name: ' Acme ', primary_color: '' }, a)).toEqual({ product_name: 'Acme', primary_color: null })
    expect(diffBranding({ ...form, primary_color: '#112233'.toUpperCase() }, a)).toEqual({})
  })
})

describe('validateImageFile', () => {
  it('accepts PNG and SVG up to 512 KiB', () => {
    expect(validateImageFile({ type: 'image/png', size: 512 * 1024 })).toBeNull()
    expect(validateImageFile({ type: 'image/svg+xml', size: 10 })).toBeNull()
  })

  it('rejects other types, empty and oversize files', () => {
    expect(validateImageFile({ type: 'image/jpeg', size: 10 })).toBe('adm.branding.error.imageType')
    expect(validateImageFile({ type: '', size: 10 })).toBe('adm.branding.error.imageType')
    expect(validateImageFile({ type: 'image/png', size: 512 * 1024 + 1 })).toBe('adm.branding.error.imageSize')
    expect(validateImageFile({ type: 'image/png', size: 0 })).toBe('adm.branding.error.imageEmpty')
  })
})

describe('previews', () => {
  it('previewVars picks readable text for the colour', () => {
    expect(previewVars('#ffff00', 'light')!['--color-primary-contrast']).toBe('17 24 39')
    expect(previewVars('#101010', 'light')!['--color-primary-contrast']).toBe('255 255 255')
    expect(previewVars('nope', 'light')).toBeNull()
  })

  it('panelVars uses the saved palette until the colour is edited', () => {
    const a = admin({
      primary_color: '#112233',
      theme: {
        light: { primary: '#112233', primary_contrast: '#ffffff', primary_soft: '#ddeeff' },
        dark: { primary: '#aabbcc', primary_contrast: '#000000', primary_soft: '#001122' },
      },
    })
    expect(panelVars('#112233', a, 'dark')['--color-primary']).toBe('170 187 204')
    expect(panelVars('#ff0000', a, 'dark')['--color-primary']).toBe('255 0 0')
    expect(panelVars('', a, 'light')['--color-primary']).toBe('79 70 229')
  })
})

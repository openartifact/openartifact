// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Form logic of the admin branding page: client-side validation (mirrors the
// server's rules so the admin gets the message before the round trip), the
// partial update to send, and the approximate live colour preview.

import type { BrandingAdmin, BrandingUpdate } from '../api/branding'
import type { BrandingPalette } from './branding'
import { IMAGE_MIME_TYPES, MAX_IMAGE_BYTES, hexToTriple, isHexColor, safeUrl } from './branding'
import type { TranslationKey } from '../i18n/en'

export const PRODUCT_NAME_MAX = 64
export const LOGIN_MESSAGE_MAX = 2000

export interface BrandingForm {
  product_name: string
  primary_color: string
  login_message: string
  support_url: string
  source_url: string
}

export type BrandingFormField = keyof BrandingForm
export type BrandingFormErrors = Partial<Record<BrandingFormField, TranslationKey>>

export function formFromAdmin(admin: BrandingAdmin): BrandingForm {
  const e = admin.effective
  return {
    product_name: e.product_name,
    primary_color: e.primary_color,
    login_message: e.login_message,
    support_url: e.support_url,
    source_url: e.source_url,
  }
}

const CONTROL_CHARS = /[\u0000-\u001f\u007f]/

export function validateBrandingForm(form: BrandingForm): BrandingFormErrors {
  const errors: BrandingFormErrors = {}
  const name = form.product_name.trim()
  if (!name) errors.product_name = 'adm.branding.error.nameRequired'
  else if ([...name].length > PRODUCT_NAME_MAX) errors.product_name = 'adm.branding.error.nameLength'
  else if (CONTROL_CHARS.test(name)) errors.product_name = 'adm.branding.error.nameControl'

  if (form.primary_color && !isHexColor(form.primary_color.trim())) errors.primary_color = 'adm.branding.error.color'

  if ([...form.login_message].length > LOGIN_MESSAGE_MAX) errors.login_message = 'adm.branding.error.messageLength'

  const support = form.support_url.trim()
  if (support && !safeUrl(support, 'contact')) errors.support_url = 'adm.branding.error.supportUrl'

  const source = form.source_url.trim()
  if (!source) errors.source_url = 'adm.branding.error.sourceRequired'
  else if (!safeUrl(source)) errors.source_url = 'adm.branding.error.sourceUrl'

  return errors
}

/** The fields that differ from the effective values, as a partial update (an emptied colour removes the override). */
export function diffBranding(form: BrandingForm, admin: BrandingAdmin): BrandingUpdate {
  const effective = formFromAdmin(admin)
  const update: BrandingUpdate = {}
  const name = form.product_name.trim()
  if (name !== effective.product_name) update.product_name = name
  const color = form.primary_color.trim().toLowerCase()
  if (color !== effective.primary_color.toLowerCase()) update.primary_color = color === '' ? null : color
  if (form.login_message !== effective.login_message) update.login_message = form.login_message
  const support = form.support_url.trim()
  if (support !== effective.support_url) update.support_url = support
  const source = form.source_url.trim()
  if (source !== effective.source_url) update.source_url = source
  return update
}

/** Why an image cannot be uploaded, or null when it can. */
export function validateImageFile(file: { size: number; type: string }): TranslationKey | null {
  if (!(IMAGE_MIME_TYPES as readonly string[]).includes(file.type)) return 'adm.branding.error.imageType'
  if (file.size > MAX_IMAGE_BYTES) return 'adm.branding.error.imageSize'
  if (file.size === 0) return 'adm.branding.error.imageEmpty'
  return null
}

function channels(triple: string): [number, number, number] {
  const [r = 0, g = 0, b = 0] = triple.split(' ').map(Number)
  return [r, g, b]
}

function luminance([r, g, b]: [number, number, number]): number {
  const lin = (c: number) => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
}

function mix(a: [number, number, number], b: [number, number, number], weightA: number): string {
  return a.map((c, i) => Math.round(c * weightA + b[i]! * (1 - weightA))).join(' ')
}

const LIGHT_TEXT = '255 255 255'
const DARK_TEXT = '17 24 39'

/**
 * Approximate CSS variable values (`R G B` triples) of a colour in one theme, for
 * the live preview before saving. The server computes the real, contrast-checked
 * palette; this only picks a readable text colour and a tint.
 */
export function previewVars(hex: string, theme: 'light' | 'dark'): Record<string, string> | null {
  const triple = hexToTriple(hex.trim())
  if (!triple) return null
  const c = channels(triple)
  const text = luminance(c) > 0.18 ? DARK_TEXT : LIGHT_TEXT
  const soft = theme === 'light' ? mix(c, [255, 255, 255], 0.12) : mix(c, [17, 20, 32], 0.2)
  return { '--color-primary': triple, '--color-primary-contrast': text, '--color-primary-soft': soft }
}

/** Built-in primary colour per theme (mirrors --color-primary in style.css); the colour picker's starting point. */
export const BUILTIN_PRIMARY = { light: '#4f46e5', dark: '#818cf8' } as const

/** CSS variables of a server-computed palette. */
export function paletteVars(p: BrandingPalette): Record<string, string> {
  return {
    '--color-primary': hexToTriple(p.primary) ?? '',
    '--color-primary-contrast': hexToTriple(p.primary_contrast) ?? '',
    '--color-primary-soft': hexToTriple(p.primary_soft) ?? '',
  }
}

/**
 * Variables for one preview panel: the saved server palette while the field
 * still holds the saved colour, an approximation while it is being edited, the
 * built-in colour when empty or invalid.
 */
export function panelVars(typed: string, admin: BrandingAdmin | null, theme: 'light' | 'dark'): Record<string, string> {
  const saved = admin?.effective
  const value = typed.trim()
  if (saved?.theme && value.toLowerCase() === saved.primary_color.toLowerCase()) return paletteVars(saved.theme[theme])
  return previewVars(value, theme) ?? previewVars(BUILTIN_PRIMARY[theme], theme) ?? {}
}

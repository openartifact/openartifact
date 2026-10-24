// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure helpers behind the branding store and the branding components.

import type { Branding, BrandingPalette, BrandingTheme } from '../api/generated/types.gen'

export type { Branding, BrandingPalette, BrandingTheme }

/** Name of the product this software is: used when no (valid) branding is known. */
export const DEFAULT_PRODUCT_NAME = 'OpenArtifact'

/** The upstream project; also the source offered when a payload carries no usable source_url. */
export const UPSTREAM_URL = 'https://github.com/openartifact/openartifact'
export const DEFAULT_SOURCE_URL = UPSTREAM_URL

/** Licence the software is offered under; shown by the attribution, never configurable. */
export const LICENSE_NAME = 'GNU AGPL-3.0-or-later'

/** Largest image the server accepts for a logo or favicon. */
export const MAX_IMAGE_BYTES = 512 * 1024
export const IMAGE_MIME_TYPES = ['image/png', 'image/svg+xml'] as const

/** localStorage key read by public/offline-brand.js for the offline page. */
export const OFFLINE_STORAGE_KEY = 'oa.branding'

/** `<style>` element that carries the colour overrides (the server writes the same id). */
export const BRAND_STYLE_ID = 'oa-brand-style'

/** Brand usage rules: minimum rendered sizes in px (the lockup SVGs carry their own clear space). */
export const LOGO_MIN_PX = { mark: 16, horizontal: 120, stacked: 64 } as const
export const LOGO_DEFAULT_PX = { mark: 32, horizontal: 144, stacked: 176 } as const
export type LogoVariant = keyof typeof LOGO_MIN_PX

/** Rendered width of a logo in px, never below the brand minimum. */
export function logoSize(variant: LogoVariant, requested?: number): number {
  const wanted = requested !== undefined && Number.isFinite(requested) ? requested : LOGO_DEFAULT_PX[variant]
  return Math.max(LOGO_MIN_PX[variant], Math.round(wanted))
}

const HEX_COLOR = /^#[0-9a-f]{6}$/i

export function isHexColor(value: string): boolean {
  return HEX_COLOR.test(value)
}

/** `#4f46e5` -> `79 70 229`, the space-separated form of the CSS variables; null when not #rrggbb. */
export function hexToTriple(hex: string): string | null {
  if (!isHexColor(hex)) return null
  const n = Number.parseInt(hex.slice(1), 16)
  return `${(n >> 16) & 255} ${(n >> 8) & 255} ${n & 255}`
}

function paletteBlock(p: BrandingPalette): string | null {
  const primary = hexToTriple(p.primary)
  const contrast = hexToTriple(p.primary_contrast)
  const soft = hexToTriple(p.primary_soft)
  if (!primary || !contrast || !soft) return null
  return `--color-primary:${primary};--color-primary-contrast:${contrast};--color-primary-soft:${soft}`
}

/** CSS text overriding the primary colour in both themes; empty without a (valid) theme. */
export function buildBrandCss(theme: BrandingTheme | null | undefined): string {
  if (!theme) return ''
  const light = paletteBlock(theme.light)
  const dark = paletteBlock(theme.dark)
  if (!light || !dark) return ''
  return `:root{${light}}.dark{${dark}}`
}

/** Document title: `Page · Product`, or the product name alone. */
export function composeTitle(page: string | null | undefined, productName: string): string {
  const p = page?.trim()
  return p ? `${p} · ${productName}` : productName
}

const SAFE_SCHEMES = {
  web: ['http:', 'https:'],
  contact: ['http:', 'https:', 'mailto:'],
} as const

/**
 * The URL when it parses as absolute with an allowed scheme, else ''. Values
 * end up in href attributes, so anything else (javascript:, data:, relative
 * paths) is dropped.
 */
export function safeUrl(value: unknown, kind: keyof typeof SAFE_SCHEMES = 'web'): string {
  if (typeof value !== 'string') return ''
  const v = value.trim()
  try {
    const u = new URL(v)
    return (SAFE_SCHEMES[kind] as readonly string[]).includes(u.protocol) ? v : ''
  } catch {
    return ''
  }
}

/** Same-origin path or http(s) URL usable as an image src; '' otherwise. */
export function safeImageUrl(value: unknown): string {
  if (typeof value !== 'string') return ''
  const v = value.trim()
  if (v.startsWith('/') && !v.startsWith('//')) return v
  return safeUrl(v)
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function palette(value: unknown): BrandingPalette | null {
  if (!value || typeof value !== 'object') return null
  const v = value as Record<string, unknown>
  const p = { primary: str(v.primary), primary_contrast: str(v.primary_contrast), primary_soft: str(v.primary_soft) }
  return paletteBlock(p) ? p : null
}

function theme(value: unknown): BrandingTheme | null {
  if (!value || typeof value !== 'object') return null
  const v = value as Record<string, unknown>
  const light = palette(v.light)
  const dark = palette(v.dark)
  return light && dark ? { light, dark } : null
}

/**
 * Coerces an untrusted payload (the embedded JSON or an API answer) into a
 * Branding the UI can use: wrong types become empty values, URLs are
 * scheme-checked, an empty name falls back to the product default and an
 * unusable source_url to the upstream repository. Null for non-objects.
 */
export function normalizeBranding(raw: unknown): Branding | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const v = raw as Record<string, unknown>
  const t = theme(v.theme)
  const color = str(v.primary_color)
  return {
    product_name: str(v.product_name).trim() || DEFAULT_PRODUCT_NAME,
    primary_color: isHexColor(color) ? color : '',
    theme: t,
    logo_light_url: safeImageUrl(v.logo_light_url),
    logo_dark_url: safeImageUrl(v.logo_dark_url),
    favicon_url: safeImageUrl(v.favicon_url),
    login_message: str(v.login_message),
    support_url: safeUrl(v.support_url, 'contact'),
    source_url: safeUrl(v.source_url) || DEFAULT_SOURCE_URL,
    revision: str(v.revision),
  }
}

/** Reads the `<script type="application/json" id="oa-branding">` the server embeds in index.html. */
export function readEmbeddedBranding(doc: Document = document): Branding | null {
  const el = doc.getElementById('oa-branding')
  if (!el?.textContent) return null
  try {
    return normalizeBranding(JSON.parse(el.textContent))
  } catch {
    return null
  }
}

/** True when the product still carries the built-in name (case-sensitive: the name is spelled OpenArtifact). */
export function isDefaultName(name: string): boolean {
  return name === DEFAULT_PRODUCT_NAME
}

/** The value offline-brand.js needs: the name and the contrast-adjusted colours of both themes. */
export interface OfflineBrand {
  product_name: string
  primary_color: string
  light?: { primary: string; primary_contrast: string }
  dark?: { primary: string; primary_contrast: string }
}

/** Null when the branding is the built-in one: the offline page then keeps its defaults. */
export function offlineBrand(b: Branding): OfflineBrand | null {
  if (isDefaultName(b.product_name) && !b.theme) return null
  const out: OfflineBrand = { product_name: b.product_name, primary_color: b.primary_color }
  if (b.theme) {
    out.light = { primary: b.theme.light.primary, primary_contrast: b.theme.light.primary_contrast }
    out.dark = { primary: b.theme.dark.primary, primary_contrast: b.theme.dark.primary_contrast }
  }
  return out
}

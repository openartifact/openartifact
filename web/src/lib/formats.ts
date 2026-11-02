// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Single source of truth for package-format metadata: display label, brand
// color, and a single-path SVG icon. Brand marks come from the simple-icons
// package (CC0); `raw` uses a generic heroicons archive-box glyph and `ivy`
// uses the Apache mark (Ivy is an Apache project with no mark of its own).
// Ansible uses the Ansible mark, apk the Alpine Linux mark, Cargo the Rust mark,
// Composer the Composer mark, Conda the Anaconda mark, nix the NixOS mark, pacman the Arch Linux mark,
// RPM the Red Hat mark, sbt the Scala mark and Terraform the Terraform mark (also used for OpenTofu
// repositories: one `terraform` format serves both clients).

import {
  siAlpinelinux,
  siAnaconda,
  siAnsible,
  siApache,
  siApachemaven,
  siArchlinux,
  siComposer,
  siConan,
  siDebian,
  siDocker,
  siGo,
  siHelm,
  siNixos,
  siNpm,
  siNuget,
  siPypi,
  siRedhat,
  siRubygems,
  siRust,
  siScala,
  siTerraform,
} from 'simple-icons'

import { relativeLuminance } from './brandColors'
import { FORMAT_IDS, type FormatId } from './formats.generated'

export type { FormatId }

interface FormatMeta {
  label: string
  /** Brand accent color, used for the fallback dot and to tint the icon. */
  color: string
  /** Single-path `d` data for the format's icon. */
  icon?: string
  /** Render `icon` with the even-odd fill rule (needed by the heroicons glyph). */
  evenodd?: boolean
  /** SVG viewBox for `icon`; defaults to simple-icons' standard 24x24 grid. */
  viewBox?: string
}

const FALLBACK_COLOR = '#6B7280'

function brand(label: string, icon: { hex: string; path: string }): FormatMeta {
  return { label, color: `#${icon.hex}`, icon: icon.path }
}

// heroicons v2 solid ArchiveBoxIcon (two sub-paths, needs even-odd fill).
const RAW_ICON =
  'M3.375 3C2.339 3 1.5 3.84 1.5 4.875v.75c0 1.036.84 1.875 1.875 1.875h17.25c1.035 0 1.875-.84 1.875-1.875v-.75C22.5 3.839 21.66 3 20.625 3H3.375ZM3.087 9l.54 9.176A3 3 0 0 0 6.62 21h10.757a3 3 0 0 0 2.995-2.824L20.913 9H3.087Zm6.163 3.75A.75.75 0 0 1 10 12h4a.75.75 0 0 1 0 1.5h-4a.75.75 0 0 1-.75-.75Z'

export const FORMAT_META: Record<FormatId, FormatMeta> = {
  ansible: brand('Ansible', siAnsible),
  apk: brand('Alpine (apk)', siAlpinelinux),
  cargo: brand('Cargo', siRust),
  composer: brand('Composer', siComposer),
  conan: brand('Conan', siConan),
  conda: brand('Conda', siAnaconda),
  deb: brand('Deb', siDebian),
  docker: brand('Docker', siDocker),
  go: brand('Go', siGo),
  helm: brand('Helm', siHelm),
  ivy: brand('Ivy', siApache),
  maven: brand('Maven', siApachemaven),
  nix: brand('Nix', siNixos),
  npm: brand('npm', siNpm),
  nuget: brand('NuGet', siNuget),
  pacman: brand('Arch Linux (pacman)', siArchlinux),
  pypi: brand('PyPI', siPypi),
  raw: { label: 'Raw', color: FALLBACK_COLOR, icon: RAW_ICON, evenodd: true },
  rpm: brand('RPM', siRedhat),
  rubygems: brand('RubyGems', siRubygems),
  sbt: brand('sbt', siScala),
  terraform: brand('Terraform', siTerraform),
}

/** Ordered format ids — drives the format pickers on repo-create screens. Generated from the Go registry. */
export const ALL_FORMATS: FormatId[] = [...FORMAT_IDS]

const DEFAULT_VIEWBOX = '0 0 24 24'

function isFormatId(id: string): id is FormatId {
  return Object.hasOwn(FORMAT_META, id)
}

export function formatLabel(id: string): string {
  const key = id?.toLowerCase() ?? ''
  return isFormatId(key) ? FORMAT_META[key].label : id
}

export function formatColor(id: string): string {
  const key = id?.toLowerCase() ?? ''
  return isFormatId(key) ? FORMAT_META[key].color : FALLBACK_COLOR
}

function hexRgb(hex: string): [number, number, number] {
  const n = Number.parseInt(hex.slice(1), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

function hexLuminance(hex: string): number {
  const [r, g, b] = hexRgb(hex)
  return relativeLuminance(r, g, b)
}

/** WCAG contrast ratio of two `#rrggbb` colors (1 to 21). */
export function hexContrast(a: string, b: string): number {
  const [hi, lo] = [hexLuminance(a), hexLuminance(b)].sort((x, y) => y - x)
  return (hi! + 0.05) / (lo! + 0.05)
}

/**
 * The surface a format badge sits on, per theme (`--color-surface-hover` in
 * `style.css`; the darker of the surface tokens in light, the lighter in
 * dark, so the worst case for the icon). Keep in sync with the tokens.
 */
export const BADGE_SURFACE = { light: '#f3f4f6', dark: '#1a1f30' } as const

/** Minimum contrast of a format icon against the badge surface (WCAG 1.4.11 non-text contrast). */
export const ICON_MIN_CONTRAST = 3

// Aim a little above the minimum so rounding in the browser cannot dip below it.
const ICON_TARGET_CONTRAST = 3.2

function rgbToHsl([r, g, b]: [number, number, number]): [number, number, number] {
  const [rn, gn, bn] = [r / 255, g / 255, b / 255]
  const max = Math.max(rn, gn, bn)
  const min = Math.min(rn, gn, bn)
  const l = (max + min) / 2
  if (max === min) return [0, 0, l]
  const d = max - min
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
  let h: number
  if (max === rn) h = (gn - bn) / d + (gn < bn ? 6 : 0)
  else if (max === gn) h = (bn - rn) / d + 2
  else h = (rn - gn) / d + 4

  return [h * 60, s, l]
}

function hslToHex(h: number, s: number, l: number): string {
  const f = (n: number) => {
    const k = (n + h / 30) % 12
    const a = s * Math.min(l, 1 - l)
    const c = l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1))

    return Math.round(c * 255)
      .toString(16)
      .padStart(2, '0')
  }

  return `#${f(0)}${f(8)}${f(4)}`
}

/**
 * `hex` moved along lightness (hue and saturation kept) until it holds the
 * target contrast against `surface`: darker on a light surface, lighter on a
 * dark one. A color that already holds it is returned unchanged.
 */
function withContrast(hex: string, surface: string, direction: 'darker' | 'lighter'): string {
  if (hexContrast(hex, surface) >= ICON_TARGET_CONTRAST) return hex
  const [h, s, l] = rgbToHsl(hexRgb(hex))
  let from = l
  let to = direction === 'darker' ? 0 : 1
  // Bisect for the lightness closest to the original that still passes.
  for (let i = 0; i < 20; i++) {
    const mid = (from + to) / 2
    if (hexContrast(hslToHex(h, s, mid), surface) >= ICON_TARGET_CONTRAST) to = mid
    else from = mid
  }

  return hslToHex(h, s, to)
}

const themeColors = new Map<string, { light: string; dark: string }>()

/**
 * Icon color of a format per theme: the brand color, lightened or darkened
 * only as far as needed to hold 3:1 on the badge surface. Brand navy (Helm,
 * NuGet) and black (Cargo) turn into a lighter blue and grey on dark instead
 * of vanishing, and the light brand blues and greens (Go, Docker, Conda,
 * Conan) get darker on light. Hue is kept, so formats stay recognisable.
 */
export function formatThemeColors(id: string): { light: string; dark: string } {
  const brand = formatColor(id).toLowerCase()
  let c = themeColors.get(brand)
  if (!c) {
    c = { light: withContrast(brand, BADGE_SURFACE.light, 'darker'), dark: withContrast(brand, BADGE_SURFACE.dark, 'lighter') }
    themeColors.set(brand, c)
  }

  return c
}

// Categorical palette for charts: brand colors collide (deb, maven, npm and
// rpm are all red) and some vanish on dark. These stay distinct and readable
// on both themes.
const CHART_PALETTE = [
  '#60A5FA', '#F59E0B', '#34D399', '#F472B6', '#A78BFA', '#F87171',
  '#2DD4BF', '#FBBF24', '#818CF8', '#FB923C', '#4ADE80', '#E879F9',
]

export function chartColor(index: number): string {
  return CHART_PALETTE[index % CHART_PALETTE.length]
}

export function formatIcon(id: string): string | undefined {
  const key = id?.toLowerCase() ?? ''
  return isFormatId(key) ? FORMAT_META[key].icon : undefined
}

export function formatViewBox(id: string): string {
  const key = id?.toLowerCase() ?? ''
  return (isFormatId(key) && FORMAT_META[key].viewBox) || DEFAULT_VIEWBOX
}

export function formatIconEvenOdd(id: string): boolean {
  const key = id?.toLowerCase() ?? ''
  return isFormatId(key) && FORMAT_META[key].evenodd === true
}

// Every format's hosted packages can be deleted by id (server: DELETE
// /repositories/{name}/packages/{id}, the deb and rpm handlers plus the
// generic deleter of every other hosted format, internal/worker/cleanup_formats.go).
export function supportsPackageDelete(format: string | undefined): boolean {
  return format !== undefined && isFormatId(format)
}

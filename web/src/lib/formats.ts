// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Single source of truth for package-format metadata: display label, brand
// color, and a single-path SVG icon. Brand marks come from the simple-icons
// package (CC0); `raw` uses a generic heroicons archive-box glyph and `ivy`
// uses the Apache mark (Ivy is an Apache project with no mark of its own).
// RPM uses the Red Hat mark, Cargo the Rust mark, Conda the Anaconda mark and
// sbt the Scala mark.

import {
  siAnaconda,
  siApache,
  siApachemaven,
  siConan,
  siDebian,
  siDocker,
  siGo,
  siHelm,
  siNpm,
  siNuget,
  siPypi,
  siRedhat,
  siRubygems,
  siRust,
  siScala,
} from 'simple-icons'

export type FormatId =
  | 'docker'
  | 'npm'
  | 'pypi'
  | 'maven'
  | 'helm'
  | 'cargo'
  | 'rubygems'
  | 'deb'
  | 'rpm'
  | 'nuget'
  | 'gomod'
  | 'raw'
  | 'conda'
  | 'conan'
  | 'ivy'
  | 'sbt'

export interface FormatMeta {
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
  docker: brand('Docker', siDocker),
  npm: brand('npm', siNpm),
  pypi: brand('PyPI', siPypi),
  maven: brand('Maven', siApachemaven),
  helm: brand('Helm', siHelm),
  cargo: brand('Cargo', siRust),
  rubygems: brand('RubyGems', siRubygems),
  deb: brand('Deb', siDebian),
  rpm: brand('RPM', siRedhat),
  nuget: brand('NuGet', siNuget),
  gomod: brand('Go', siGo),
  raw: { label: 'Raw', color: FALLBACK_COLOR, icon: RAW_ICON, evenodd: true },
  conda: brand('Conda', siAnaconda),
  conan: brand('Conan', siConan),
  ivy: brand('Ivy', siApache),
  sbt: brand('sbt', siScala),
}

/** Ordered format ids — drives the format pickers on repo-create screens. */
export const ALL_FORMATS: FormatId[] = [
  'docker', 'npm', 'pypi', 'maven', 'helm', 'cargo', 'rubygems', 'deb',
  'rpm', 'nuget', 'gomod', 'raw', 'conda', 'conan', 'ivy', 'sbt',
]

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

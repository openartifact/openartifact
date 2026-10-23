// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Which formats can be created as proxy or group repositories. Mirrors the
// server policy in internal/repotype; the server stays authoritative and
// rejects anything else with validation_failed.

import { FORMAT_LOCALE } from './datetime'
import { ALL_FORMATS, type FormatId } from './formats'

export type RepoType = 'hosted' | 'proxy' | 'group'

const PROXY_FORMATS: readonly FormatId[] = ['cargo', 'deb', 'docker', 'go', 'helm', 'maven', 'npm', 'nuget', 'pypi', 'raw', 'rpm']
const GROUP_FORMATS: readonly FormatId[] = ['cargo', 'deb', 'docker', 'go', 'helm', 'maven', 'npm', 'nuget', 'pypi', 'raw', 'rpm']

/** Docker, pypi, cargo, go and nuget proxies default to Docker Hub / pypi.org / crates.io / proxy.golang.org / nuget.org; every other proxy needs an upstream URL. */
export function upstreamUrlRequired(format: FormatId): boolean {
  return format !== 'docker' && format !== 'pypi' && format !== 'cargo' && format !== 'go' && format !== 'nuget'
}

/** Placeholder for the upstream URL field. */
export function upstreamUrlPlaceholder(format: FormatId): string {
  if (format === 'docker') return 'https://registry-1.docker.io'
  if (format === 'nuget') return 'https://api.nuget.org/v3/index.json'
  if (format === 'pypi') return 'https://pypi.org/simple'
  if (format === 'cargo') return 'https://index.crates.io'
  if (format === 'go') return 'https://proxy.golang.org'
  return 'https://registry.example.com'
}

/** Formats selectable for a repository type, in picker order. */
export function allowedFormats(type: RepoType): FormatId[] {
  switch (type) {
    case 'proxy':
      return ALL_FORMATS.filter((f) => PROXY_FORMATS.includes(f))
    case 'group':
      return ALL_FORMATS.filter((f) => GROUP_FORMATS.includes(f))
    default:
      return [...ALL_FORMATS]
  }
}

export function isFormatAllowed(type: RepoType, format: FormatId): boolean {
  return allowedFormats(type).includes(format)
}

/** Keeps `format` when the type supports it, else falls back to the first allowed one. */
export function coerceFormat(type: RepoType, format: FormatId): FormatId {
  return isFormatAllowed(type, format) ? format : allowedFormats(type)[0]
}

const TTL_UNITS: ReadonlyArray<{ unit: 'day' | 'hour' | 'minute' | 'second'; seconds: number }> = [
  { unit: 'day', seconds: 86400 },
  { unit: 'hour', seconds: 3600 },
  { unit: 'minute', seconds: 60 },
  { unit: 'second', seconds: 1 },
]

/** Renders a TTL in seconds as a locale-aware duration, e.g. 5400 -> "1 hour 30 minutes". */
export function formatTtl(seconds: number | null | undefined): string {
  const total = Math.floor(seconds ?? 0)
  const parts: string[] = []
  let rest = Math.max(0, Number.isFinite(total) ? total : 0)
  for (const { unit, seconds: size } of TTL_UNITS) {
    const n = Math.floor(rest / size)
    if (n === 0 && !(unit === 'second' && parts.length === 0)) continue
    rest -= n * size
    parts.push(new Intl.NumberFormat(FORMAT_LOCALE, { style: 'unit', unit, unitDisplay: 'long' }).format(n))
  }
  return parts.join(' ')
}

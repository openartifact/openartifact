// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Which formats can be created as proxy or group repositories. Mirrors the
// server policy in internal/repotype; the server stays authoritative and
// rejects anything else with validation_failed.

import { ALL_FORMATS, type FormatId } from './formats'

export type RepoType = 'hosted' | 'proxy' | 'group'

const PROXY_FORMATS: readonly FormatId[] = ['deb', 'maven', 'raw', 'rpm']
const GROUP_FORMATS: readonly FormatId[] = ['maven', 'raw']

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
    parts.push(new Intl.NumberFormat(undefined, { style: 'unit', unit, unitDisplay: 'long' }).format(n))
  }
  return parts.join(' ')
}

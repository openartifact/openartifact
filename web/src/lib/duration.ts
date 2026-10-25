// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { FORMAT_LOCALE } from './datetime'

const UNITS = [
  { unit: 'day', seconds: 86_400 },
  { unit: 'hour', seconds: 3_600 },
  { unit: 'minute', seconds: 60 },
  { unit: 'second', seconds: 1 },
] as const

type DurationUnit = (typeof UNITS)[number]['unit']

function unitLabel(n: number, unit: DurationUnit, display: 'long' | 'short'): string {
  return new Intl.NumberFormat(FORMAT_LOCALE, { style: 'unit', unit, unitDisplay: display }).format(n)
}

/**
 * Renders a duration in seconds as locale-aware units, largest first, e.g.
 * 5400 -> "1 hour 30 minutes". Negative or non-finite input counts as zero,
 * which renders as "0 seconds". `maxUnits` keeps only the leading units.
 */
export function formatDuration(
  seconds: number | null | undefined,
  { display = 'long', maxUnits = UNITS.length }: { display?: 'long' | 'short'; maxUnits?: number } = {},
): string {
  const total = Math.floor(seconds ?? 0)
  let rest = Number.isFinite(total) ? Math.max(0, total) : 0
  const parts: string[] = []
  for (const { unit, seconds: size } of UNITS) {
    const n = Math.floor(rest / size)
    if (n === 0 && !(unit === 'second' && parts.length === 0)) continue
    rest -= n * size
    parts.push(unitLabel(n, unit, display))
    if (parts.length === maxUnits) break
  }
  return parts.join(' ')
}

/** Largest single unit that divides the duration exactly: 172800 -> "2 days", 90 -> "90 seconds". */
export function formatExactDuration(seconds: number): string {
  for (const { unit, seconds: size } of UNITS) {
    if (seconds >= size && seconds % size === 0) return unitLabel(seconds / size, unit, 'long')
  }
  return unitLabel(seconds, 'second', 'long')
}

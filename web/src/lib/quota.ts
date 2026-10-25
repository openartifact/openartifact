// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure helpers shared by the quota screens: byte formatting, the size unit
// picker and the usage bar colour.

export const SIZE_UNITS = [
  { label: 'MB', bytes: 1024 ** 2 },
  { label: 'GB', bytes: 1024 ** 3 },
  { label: 'TB', bytes: 1024 ** 4 },
] as const

export type SizeUnit = (typeof SIZE_UNITS)[number]['label']

/** Human readable size, one decimal from KB upwards. */
export function formatBytes(bytes: number): string {
  if (bytes >= 1024 ** 4) return `${(bytes / 1024 ** 4).toFixed(1)} TB`
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${bytes} B`
}

/** {@link formatBytes}, or an em dash for a missing or zero size. */
export function formatBytesOrDash(bytes: number | null | undefined): string {
  return bytes ? formatBytes(bytes) : '—'
}

/** Splits a byte limit into the largest unit that keeps the value >= 1; 0 (unlimited) gives an empty value. */
export function splitBytes(bytes: number): { value: number; unit: SizeUnit } {
  if (!(bytes > 0)) return { value: 0, unit: 'GB' }
  const unit = [...SIZE_UNITS].reverse().find((u) => bytes >= u.bytes) ?? SIZE_UNITS[0]
  return { value: parseFloat((bytes / unit.bytes).toFixed(2)), unit: unit.label }
}

/** Inverse of {@link splitBytes}; an empty or non-positive value means unlimited (0). */
export function toBytes(value: number | null | undefined, unit: SizeUnit): number {
  if (!value || value <= 0) return 0
  const u = SIZE_UNITS.find((s) => s.label === unit) ?? SIZE_UNITS[1]
  return Math.round(value * u.bytes)
}

type UsageLevel = 'ok' | 'warn' | 'full'

/**
 * Level of a usage bar: `full` at 100 % or more, `warn` from the warn
 * threshold (0 or missing disables the warning level), otherwise `ok`.
 */
export function usageLevel(percent: number, warnThreshold?: number): UsageLevel {
  if (percent >= 100) return 'full'
  if (warnThreshold && warnThreshold > 0 && percent >= warnThreshold) return 'warn'
  return 'ok'
}

const LEVEL_CLASS: Record<UsageLevel, string> = { ok: 'bg-success', warn: 'bg-warning', full: 'bg-danger' }

/** Tailwind background class of a usage bar. */
export function usageBarClass(percent: number, warnThreshold?: number): string {
  return LEVEL_CLASS[usageLevel(percent, warnThreshold)]
}

/** Percentage of `used` against `limit`; 0 when there is no limit. */
export function percentOf(used: number | undefined, limit: number | undefined): number {
  if (!limit || limit <= 0) return 0
  return ((used ?? 0) / limit) * 100
}

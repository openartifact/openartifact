// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n'
import { formatLocale } from './intl'

type DateInput = string | number | Date | null | undefined

function toDate(value: DateInput): Date | null {
  if (value === null || value === undefined || value === '') return null
  const d = value instanceof Date ? value : new Date(value)
  return Number.isNaN(d.getTime()) ? null : d
}

export function formatDate(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleDateString(formatLocale(), { dateStyle: 'medium' }) : fallback
}

export function formatDateTime(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleString(formatLocale(), { dateStyle: 'medium', timeStyle: 'short' }) : fallback
}

/** Full timestamp with seconds, for audit trails where ordering within a minute matters. */
export function formatTimestamp(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleString(formatLocale(), { dateStyle: 'medium', timeStyle: 'medium' }) : fallback
}

/**
 * Timestamp in UTC regardless of the viewer's zone ("2026-10-22 14:03:07"), for logs that are
 * compared with server logs and exports, which are always UTC.
 */
export function formatUtc(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toISOString().slice(0, 19).replace('T', ' ') : fallback
}

export function formatTime(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleTimeString(formatLocale()) : fallback
}

function compactUnit(n: number, unit: 'minute' | 'hour' | 'day'): string {
  return new Intl.NumberFormat(formatLocale(), { style: 'unit', unit, unitDisplay: 'narrow' }).format(n)
}

/**
 * Compact relative time: "just now", then the narrow unit form of the active
 * language ("5m", "3h", "2d" in English). With `maxDays` set,
 * anything older renders as a date instead.
 */
export function timeAgo(value: DateInput, { fallback = '—', maxDays }: { fallback?: string; maxDays?: number } = {}): string {
  const d = toDate(value)
  if (!d) return fallback
  const minutes = Math.floor((Date.now() - d.getTime()) / 60_000)
  if (minutes < 1) return t('common.justNow')
  if (minutes < 60) return compactUnit(minutes, 'minute')
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return compactUnit(hours, 'hour')
  const days = Math.floor(hours / 24)
  if (maxDays !== undefined && days >= maxDays) return formatDate(d)
  return compactUnit(days, 'day')
}

/** True when `value` is a valid timestamp in the past; a missing expiry never expires. */
export function isExpired(value: DateInput): boolean {
  const d = toDate(value)
  return d !== null && d.getTime() < Date.now()
}

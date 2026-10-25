// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n/en'

/**
 * Locale used for every date, time, number and duration rendering. It must
 * match the i18n message bundle (src/i18n/en.ts) so that numbers and dates
 * never appear in the browser's language while the UI text is English.
 */
export const FORMAT_LOCALE = 'en'

type DateInput = string | number | Date | null | undefined

function toDate(value: DateInput): Date | null {
  if (value === null || value === undefined || value === '') return null
  const d = value instanceof Date ? value : new Date(value)
  return Number.isNaN(d.getTime()) ? null : d
}

export function formatDate(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleDateString(FORMAT_LOCALE, { dateStyle: 'medium' }) : fallback
}

export function formatDateTime(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleString(FORMAT_LOCALE, { dateStyle: 'medium', timeStyle: 'short' }) : fallback
}

/** Full timestamp with seconds, for audit trails where ordering within a minute matters. */
export function formatTimestamp(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleString(FORMAT_LOCALE, { dateStyle: 'medium', timeStyle: 'medium' }) : fallback
}

export function formatTime(value: DateInput, fallback = '—'): string {
  const d = toDate(value)
  return d ? d.toLocaleTimeString(FORMAT_LOCALE) : fallback
}

/**
 * Compact relative time: "just now", "5m", "3h", "2d". With `maxDays` set,
 * anything older renders as a date instead.
 */
export function timeAgo(value: DateInput, { fallback = '—', maxDays }: { fallback?: string; maxDays?: number } = {}): string {
  const d = toDate(value)
  if (!d) return fallback
  const minutes = Math.floor((Date.now() - d.getTime()) / 60_000)
  if (minutes < 1) return t('common.justNow')
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  if (maxDays !== undefined && days >= maxDays) return formatDate(d)
  return `${days}d`
}

/** True when `value` is a valid timestamp in the past; a missing expiry never expires. */
export function isExpired(value: DateInput): boolean {
  const d = toDate(value)
  return d !== null && d.getTime() < Date.now()
}

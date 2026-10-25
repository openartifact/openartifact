// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { formatDate, formatDateTime, formatTimestamp, isExpired, timeAgo } from '../datetime'
import { formatDuration } from '../duration'

describe('datetime', () => {
  it('returns fallback for nullish/invalid', () => {
    expect(formatDate(null)).toBe('—')
    expect(formatDate(undefined, 'n/a')).toBe('n/a')
    expect(formatDate('garbage', 'x')).toBe('x')
    expect(formatDateTime('garbage')).toBe('—')
    expect(formatDateTime(null, 'never')).toBe('never')
  })
  it('formats valid ISO like Intl medium', () => {
    const iso = '2026-10-21T13:45:00Z'
    const d = new Date(iso)
    expect(formatDate(iso)).toBe(d.toLocaleDateString('en', { dateStyle: 'medium' }))
    expect(formatDateTime(iso)).toBe(d.toLocaleString('en', { dateStyle: 'medium', timeStyle: 'short' }))
  })
})

describe('formatting locale', () => {
  it('stays English when the browser language is it-IT', () => {
    const orig = Object.getOwnPropertyDescriptor(globalThis, 'navigator')
    Object.defineProperty(globalThis, 'navigator', { value: { language: 'it-IT', languages: ['it-IT'] }, configurable: true })
    try {
      expect(formatDate('2026-10-21T13:45:00Z')).toBe('Oct 21, 2026')
      expect(formatDuration(3600)).toBe('1 hour')
      expect(formatDuration(60)).toBe('1 minute')
    } finally {
      if (orig) Object.defineProperty(globalThis, 'navigator', orig)
      else delete (globalThis as { navigator?: unknown }).navigator
    }
  })
})

describe('formatTimestamp', () => {
  it('includes seconds and falls back like the other formatters', () => {
    const iso = '2026-10-21T13:45:12Z'
    expect(formatTimestamp(iso)).toBe(new Date(iso).toLocaleString('en', { dateStyle: 'medium', timeStyle: 'medium' }))
    expect(formatTimestamp(null)).toBe('—')
    expect(formatTimestamp('garbage', 'x')).toBe('x')
  })
})

describe('timeAgo', () => {
  const ago = (ms: number) => new Date(Date.now() - ms).toISOString()

  it('renders compact units', () => {
    expect(timeAgo(ago(10_000))).toBe('just now')
    expect(timeAgo(ago(5 * 60_000))).toBe('5m')
    expect(timeAgo(ago(3 * 3_600_000))).toBe('3h')
    expect(timeAgo(ago(2 * 86_400_000))).toBe('2d')
  })
  it('falls back for a missing or invalid value', () => {
    expect(timeAgo(undefined)).toBe('—')
    expect(timeAgo('garbage', { fallback: 'n/a' })).toBe('n/a')
  })
  it('switches to a date past maxDays', () => {
    const old = ago(10 * 86_400_000)
    expect(timeAgo(old, { maxDays: 7 })).toBe(formatDate(old))
    expect(timeAgo(ago(3 * 86_400_000), { maxDays: 7 })).toBe('3d')
  })
})

describe('isExpired', () => {
  it('is true only for a valid past timestamp', () => {
    expect(isExpired(new Date(Date.now() - 1000).toISOString())).toBe(true)
    expect(isExpired(new Date(Date.now() + 60_000).toISOString())).toBe(false)
    expect(isExpired(undefined)).toBe(false)
    expect(isExpired('')).toBe(false)
    expect(isExpired('garbage')).toBe(false)
  })
})

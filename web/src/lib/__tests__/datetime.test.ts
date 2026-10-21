// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { formatDate, formatDateTime } from '../datetime'
import { formatTtl } from '../repoTypes'

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
      expect(formatTtl(3600)).toBe('1 hour')
      expect(formatTtl(60)).toBe('1 minute')
    } finally {
      if (orig) Object.defineProperty(globalThis, 'navigator', orig)
      else delete (globalThis as { navigator?: unknown }).navigator
    }
  })
})

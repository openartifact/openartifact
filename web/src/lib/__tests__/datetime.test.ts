// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { formatDate, formatDateTime } from '../datetime'

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
    expect(formatDate(iso)).toBe(d.toLocaleDateString(undefined, { dateStyle: 'medium' }))
    expect(formatDateTime(iso)).toBe(d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }))
  })
})

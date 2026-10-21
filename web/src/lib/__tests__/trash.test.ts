// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { confirmNameMatches, daysUntilPurge, parseRetentionInput, purgeInText } from '../trash'

const now = Date.parse('2026-10-22T12:00:00Z')

describe('confirmNameMatches', () => {
  it('requires an exact match', () => {
    expect(confirmNameMatches('repo-a', 'repo-a')).toBe(true)
    expect(confirmNameMatches('Repo-a', 'repo-a')).toBe(false)
    expect(confirmNameMatches('repo-a ', 'repo-a')).toBe(false)
    expect(confirmNameMatches('', '')).toBe(false)
  })
})

describe('purge timing', () => {
  it('rounds up to whole days', () => {
    expect(daysUntilPurge('2026-10-25T00:00:00Z', now)).toBe(3)
    expect(daysUntilPurge('2026-10-22T12:00:01Z', now)).toBe(1)
  })
  it('clamps past dates and handles missing values', () => {
    expect(daysUntilPurge('2026-10-01T00:00:00Z', now)).toBe(0)
    expect(daysUntilPurge(undefined, now)).toBeNull()
    expect(daysUntilPurge('junk', now)).toBeNull()
  })
  it('renders text', () => {
    expect(purgeInText('2026-11-01T12:00:00Z', now)).toBe('Purges in 10 days')
    expect(purgeInText('2026-10-23T00:00:00Z', now)).toBe('Purges in 1 day')
    expect(purgeInText('2026-10-01T00:00:00Z', now)).toBe('Purges today')
    expect(purgeInText(null, now)).toBe('—')
  })
})

describe('parseRetentionInput', () => {
  it('maps the form value', () => {
    expect(parseRetentionInput('')).toBeNull()
    expect(parseRetentionInput('  ')).toBeNull()
    expect(parseRetentionInput(null)).toBeNull()
    expect(parseRetentionInput('0')).toBe(0)
    expect(parseRetentionInput(30)).toBe(30)
    expect(parseRetentionInput('-1')).toBeUndefined()
    expect(parseRetentionInput('1.5')).toBeUndefined()
  })
})

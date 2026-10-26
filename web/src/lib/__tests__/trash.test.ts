// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import {
  confirmNameMatches,
  daysUntilPurge,
  emptyTrashResultText,
  parseRetentionInput,
  purgeInText,
  trashCapabilities,
} from '../trash'

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

describe('emptyTrashResultText', () => {
  it('reports the purged count', () => {
    expect(emptyTrashResultText(3, 0)).toBe('Purged 3 items from the trash.')
  })
  it('uses the singular for a single item', () => {
    expect(emptyTrashResultText(1, 1)).toBe('Purged 1 item from the trash. 1 item remains; empty the trash again.')
  })
  it('mentions what is left when the request was capped', () => {
    expect(emptyTrashResultText(1000, 250)).toBe('Purged 1000 items from the trash. 250 items remain; empty the trash again.')
  })
})

describe('trashCapabilities', () => {
  it('gives admins everything in repo scope', () => {
    expect(trashCapabilities('repo', true, undefined)).toEqual({ restore: true, empty: true, purge: true })
  })
  it('maps delete to restore and manage to empty for non-admins', () => {
    expect(trashCapabilities('repo', false, { write: true, delete: true, manage: false })).toEqual({
      restore: true,
      empty: false,
      purge: false,
    })
    expect(trashCapabilities('repo', false, { write: true, delete: true, manage: true }).empty).toBe(true)
  })
  it('grants nothing without permissions', () => {
    expect(trashCapabilities('repo', false, undefined)).toEqual({ restore: false, empty: false, purge: false })
  })
  it('global scope is admin-only and has no empty action', () => {
    expect(trashCapabilities('global', true, undefined)).toEqual({ restore: true, empty: false, purge: true })
    expect(trashCapabilities('global', false, { write: true, delete: true, manage: true }).restore).toBe(false)
  })
})

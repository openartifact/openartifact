// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { securityState } from '../repoList'
import type { RepositoryListItem } from '../../api/repositories'

const sec = { critical: 0, high: 0, medium: 0, low: 0, secrets: 0, license_violations: 0 }
const item = (count: number, security: Record<string, unknown>): Pick<RepositoryListItem, 'stats'> => ({
  stats: { package_count: count, size_bytes: 0, security: { ...sec, ...security }, quota: { used_bytes: 0 } },
}) as never

describe('securityState', () => {
  it('is unknown without stats', () => expect(securityState({}).kind).toBe('unknown'))
  it('is empty for a repository without packages', () => expect(securityState(item(0, {})).kind).toBe('empty'))
  it('is unscanned when packages exist but nothing was scanned', () => expect(securityState(item(3, {})).kind).toBe('unscanned'))
  it('is clean once scanned without findings', () => expect(securityState(item(3, { scanned_at: '2026-01-01T00:00:00Z' })).kind).toBe('clean'))
  it('reports findings of any kind, secrets included', () => {
    expect(securityState(item(3, { secrets: 1 })).kind).toBe('findings')
    expect(securityState(item(3, { high: 2, scanned_at: 'x' })).kind).toBe('findings')
  })
})

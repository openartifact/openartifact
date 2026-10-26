// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { SecuritySummary } from '../../api/artifact'
import { artifactCapabilities, securityFlag, securityLevel } from '../artifactSecurity'

const summary = (over: Partial<SecuritySummary> = {}): SecuritySummary => ({
  scanned_at: '2025-01-01T00:00:00Z',
  vuln: { critical: 0, high: 0, medium: 0, low: 0 },
  license: { violations: 0 },
  secret: { count: 0 },
  ai: { quarantine_recommended: 0, review_required: 0 },
  quarantined: false,
  ...over,
})

describe('artifactCapabilities', () => {
  it('gives admins everything and readers nothing', () => {
    expect(artifactCapabilities({ isAdmin: true })).toEqual({ maintain: true, admin: true, write: true, remove: true })
    expect(artifactCapabilities({ isAdmin: false })).toEqual({ maintain: false, admin: false, write: false, remove: false })
  })
  it('follows the repository permissions of a non-admin', () => {
    const caps = artifactCapabilities({ isAdmin: false, permissions: { write: true, delete: false, manage: false, admin: false } })
    expect(caps).toEqual({ maintain: false, admin: false, write: true, remove: false })
    // A project maintainer reads the activity feed, but the scan routes stay global-admin only.
    const maintainer = artifactCapabilities({ isAdmin: false, permissions: { write: true, delete: true, manage: true, admin: true } })
    expect(maintainer.maintain).toBe(true)
    expect(maintainer.admin).toBe(false)
  })
})

describe('securityLevel', () => {
  it('is the worst severity, clean after a scan and unscanned otherwise', () => {
    expect(securityLevel(summary({ vuln: { critical: 1, high: 0, medium: 0, low: 0 } }))).toBe('critical')
    expect(securityLevel(summary({ vuln: { critical: 0, high: 0, medium: 0, low: 2 } }))).toBe('low')
    expect(securityLevel(summary())).toBe('clean')
    expect(securityLevel(summary({ scanned_at: null }))).toBe('unscanned')
    expect(securityLevel(null)).toBe('unscanned')
  })
})

describe('securityFlag', () => {
  it('names critical before high and stays silent below', () => {
    expect(securityFlag(summary({ vuln: { critical: 2, high: 5, medium: 0, low: 0 } }))).toBe('2 critical')
    expect(securityFlag(summary({ vuln: { critical: 0, high: 5, medium: 0, low: 0 } }))).toBe('5 high')
    expect(securityFlag(summary({ vuln: { critical: 0, high: 0, medium: 9, low: 0 } }))).toBeUndefined()
    expect(securityFlag(null)).toBeUndefined()
  })
})

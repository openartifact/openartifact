// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { PackageItem } from '../../api/repositories'
import { compareVersionStrings, countTotal, groupVersions, primaryFile, selectVersion, worstCount, worstSeverity } from '../artifactVersions'

const row = (over: Partial<PackageItem>): PackageItem => ({ id: 'x', name: 'pkg', version: '1.0.0', format: 'deb', ...over }) as PackageItem

describe('groupVersions', () => {
  it('rolls the files of one version up and marks the newest version as latest', () => {
    const entries = groupVersions([
      row({ id: 'a', version: '1.0.0', size: 10, created_at: '2025-01-01T00:00:00Z' }),
      row({ id: 'b', version: '1.1.0', size: 5, created_at: '2025-02-01T00:00:00Z' }),
      row({ id: 'c', version: '1.1.0', size: 7, created_at: '2025-02-02T00:00:00Z' }),
    ])
    expect(entries.map((e) => e.version)).toEqual(['1.1.0', '1.0.0'])
    expect(entries[0]).toMatchObject({ size: 12, latest: true, createdAt: '2025-02-01T00:00:00.000Z' })
    expect(entries[0]!.files.map((f) => f.id)).toEqual(['b', 'c'])
    expect(entries[1]!.latest).toBe(false)
  })

  it('sums the severity counts, keeps the latest scan time and flags any flagged file', () => {
    const [e] = groupVersions([
      row({ id: 'a', security: { critical: 1, high: 0, medium: 2, low: 0 }, scanned_at: '2025-03-01T00:00:00Z' }),
      row({ id: 'b', security: { critical: 0, high: 3, medium: 0, low: 1 }, scanned_at: '2025-04-01T00:00:00Z', flagged: true }),
    ])
    expect(e!.security).toEqual({ critical: 1, high: 3, medium: 2, low: 1 })
    expect(e!.scannedAt).toBe('2025-04-01T00:00:00Z')
    expect(e!.flagged).toBe(true)
  })

  it('reports no security when the server gave none, and detects prereleases', () => {
    const entries = groupVersions([row({ version: '2.0.0-rc.1', created_at: '2025-05-01T00:00:00Z' }), row({ version: '1.0.0', created_at: '2025-01-01T00:00:00Z' })])
    expect(entries[0]!.security).toBeNull()
    expect(entries[0]!.prerelease).toBe(true)
    expect(entries[1]!.prerelease).toBe(false)
  })
})

describe('selectVersion', () => {
  const entries = groupVersions([row({ version: '1.0.0', created_at: '2025-01-01T00:00:00Z' }), row({ version: '2.0.0', created_at: '2025-02-01T00:00:00Z' })])
  it('defaults to the latest and returns null for an unknown version', () => {
    expect(selectVersion(entries, undefined)!.version).toBe('2.0.0')
    expect(selectVersion(entries, '1.0.0')!.version).toBe('1.0.0')
    expect(selectVersion(entries, '9.9.9')).toBeNull()
    expect(selectVersion([], undefined)).toBeNull()
  })
  it('picks the first listed file as primary', () => {
    expect(primaryFile(entries[0]!)).toBe(entries[0]!.files[0])
  })
})

describe('severity helpers', () => {
  it('finds the worst severity and totals the counts', () => {
    expect(worstSeverity({ critical: 0, high: 0, medium: 2, low: 9 })).toBe('medium')
    expect(worstSeverity({ critical: 0, high: 0, medium: 0, low: 0 })).toBe('none')
    expect(worstSeverity(null)).toBe('none')
    expect(countTotal({ critical: 1, high: 2, medium: 3, low: 4 })).toBe(10)
    expect(countTotal(null)).toBe(0)
  })
  it('orders version strings numerically', () => {
    expect(['1.10.0', '1.9.0', '1.2.0'].sort(compareVersionStrings)).toEqual(['1.2.0', '1.9.0', '1.10.0'])
  })
})

describe('worstCount', () => {
  it('counts only the findings of the worst severity, the figure the chip names', () => {
    expect(worstCount({ critical: 0, high: 2, medium: 3, low: 4 })).toBe(2)
    expect(worstCount({ critical: 0, high: 0, medium: 0, low: 0 })).toBe(0)
    expect(worstCount(null)).toBe(0)
  })
})

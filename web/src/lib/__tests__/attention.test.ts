// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { FindingsSummary, RepositoryWithStats, SecurityHealth, TokenRow } from '../../api/dashboard'
import { deriveAttention } from '../attention'

const now = Date.parse('2026-10-25T12:00:00Z')
const counts = { total: 0, critical: 0, high: 0, medium: 0, low: 0, quarantined: 0 }
const summary = (vuln: Partial<typeof counts>): FindingsSummary => ({
  by_type: { vuln: { ...counts, ...vuln }, license: counts, secret: counts, ai: counts },
  scanned_repositories: 3,
  total_repositories: 4,
})
const repo = (name: string, used: number, limit: number): RepositoryWithStats =>
  ({ id: name, name, format: 'npm', stats: { quota: { used_bytes: used, limit_bytes: limit } } }) as RepositoryWithStats
const token = (over: Partial<TokenRow>): TokenRow => ({ id: 't1', type: 'personal', name: 'ci', owner: 'a', created_at: '2026-01-01T00:00:00Z', expired: false, ...over })

describe('deriveAttention', () => {
  it('is empty when every source is clean or unavailable', () => {
    expect(deriveAttention({ now })).toEqual([])
    expect(deriveAttention({ now, summary: summary({}), health: { scanners: [{ name: 'osv', ready: true }] } as SecurityHealth, storage: [{ name: 'fs', status: 'healthy' }], nearQuota: [], tokens: [] })).toEqual([])
  })

  it('turns API-shaped inputs into items linking to where to act, most severe first', () => {
    const items = deriveAttention({
      now,
      unscanned: { total: 2 },
      nearQuota: [repo('docker-main', 90, 100)],
      tokens: [token({ id: 't9', name: 'deploy', expires_at: '2026-10-27T12:00:00Z' })],
      summary: summary({ critical: 3, quarantined: 1 }),
      storage: [{ name: 's3', status: 'unreachable' }],
      health: { scanners: [{ name: 'trivy', ready: false, message: 'database missing' }], scans_failed_24h: 2 },
      flagged: { total: 1 },
    })
    expect(items.map((i) => i.kind)).toEqual([
      'storage-down',
      'critical-findings',
      'scanner-down',
      'scans-failed',
      'flagged-repos',
      'token-expiring',
      'quarantined',
      'quota',
      'unscanned',
    ])
  })

  it('links each item', () => {
    const items = deriveAttention({ now, storage: [{ name: 's3', status: 'unreachable' }], nearQuota: [repo('a b', 95, 100)], tokens: [token({ expires_at: '2026-11-01T00:00:00Z' })], unscanned: { total: 1 }, flagged: { total: 2 } })
    const to = Object.fromEntries(items.map((i) => [i.kind, i.to]))
    expect(to['storage-down']).toBe('/settings/backends')
    expect(to['quota']).toBe('/repos/a%20b?tab=settings')
    expect(to['token-expiring']).toBe('/settings/tokens?expiring=30')
    expect(to['unscanned']).toBe('/repos?unscanned=1')
    expect(to['flagged-repos']).toBe('/repos?flagged=1')
  })

  it('reports the quota percentage and raises the severity near the limit', () => {
    const [a, b] = deriveAttention({ now, nearQuota: [repo('a', 96, 100), repo('b', 82, 100)] })
    expect(a).toMatchObject({ severity: 'high', params: { name: 'a', percent: '96' } })
    expect(b).toMatchObject({ severity: 'medium', params: { percent: '82' } })
  })

  it('separates expired tokens from expiring ones and counts days', () => {
    const items = deriveAttention({
      now,
      tokens: [
        token({ id: 'x', name: 'old', expired: true, expires_at: '2026-10-01T00:00:00Z' }),
        token({ id: 'y', name: 'soon', expires_at: '2026-10-27T12:00:00Z' }),
        token({ id: 'z', name: 'later', expires_at: '2026-11-10T00:00:00Z' }),
      ],
    })
    expect(items.map((i) => [i.kind, i.severity])).toEqual([
      ['token-expired', 'high'],
      ['token-expiring', 'high'],
      ['token-expiring', 'medium'],
    ])
    expect(items[1]!.params.days).toBe('2')
  })

  it('ignores Dependency-Track unless it is enabled and its last push failed', () => {
    const h = (dt: SecurityHealth['dependency_track']): SecurityHealth => ({ scanners: [], dependency_track: dt })
    expect(deriveAttention({ now, health: h({ enabled: false, last_push_ok: false }) })).toEqual([])
    expect(deriveAttention({ now, health: h({ enabled: true, last_push_ok: true }) })).toEqual([])
    expect(deriveAttention({ now, health: h({ enabled: true, last_push_ok: false }) })[0]!.kind).toBe('dtrack-failed')
  })
})

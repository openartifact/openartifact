// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  countsLabel,
  findingArtifact,
  findingLink,
  hasNarrowingFilters,
  isCellActive,
  stripPatch,
  summaryTotals,
  toFindingFilters,
  typeTotal,
  type InboxFilters,
} from '../securityFindings'
import type { SecuritySummary } from '../../api/security'

const base: InboxFilters = { type: 'all', severity: '', status: '', repo: '', q: '' }
const z = { total: 0, critical: 0, high: 0, medium: 0, low: 0, quarantined: 0 }
const summary = {
  by_type: { vuln: { ...z, total: 5, critical: 2, high: 3 }, license: { ...z, total: 1, low: 1 }, secret: { ...z, total: 2, high: 2, quarantined: 1 }, ai: { ...z } },
  scanned_repositories: 1,
  total_repositories: 1,
} as SecuritySummary

describe('securityFindings', () => {
  it('sends only the filters that are set; "all" means every type', () => {
    expect(toFindingFilters(base)).toEqual({ type: undefined, repo: undefined, severity: undefined, status: undefined, q: undefined })
    expect(toFindingFilters({ ...base, type: 'vuln', repo: 'r', q: ' x ' })).toMatchObject({ type: 'vuln', repo: 'r', q: 'x' })
  })

  it('treats severity, status and text as narrowing but not the type', () => {
    expect(hasNarrowingFilters(base)).toBe(false)
    expect(hasNarrowingFilters({ ...base, type: 'secret' })).toBe(false)
    expect(hasNarrowingFilters({ ...base, severity: 'high' })).toBe(true)
    expect(hasNarrowingFilters({ ...base, status: 'open' })).toBe(true)
    expect(hasNarrowingFilters({ ...base, q: '  ' })).toBe(false)
  })

  it('sums the summary over every type or reads one', () => {
    expect(summaryTotals(summary, 'all')).toEqual({ total: 8, critical: 2, high: 5, medium: 0, low: 1, quarantined: 1 })
    expect(typeTotal(summary, 'secret')).toBe(2)
    expect(typeTotal(null, 'vuln')).toBeUndefined()
  })

  it('toggles strip cells: a click sets the filter, a second releases it', () => {
    expect(stripPatch(base, 'critical')).toEqual({ severity: 'critical', status: '' })
    expect(stripPatch({ severity: 'critical', status: '' }, 'critical')).toEqual({ severity: '', status: '' })
    expect(stripPatch(base, 'quarantined')).toEqual({ status: 'quarantined', severity: '' })
    expect(stripPatch({ severity: '', status: 'quarantined' }, 'quarantined')).toEqual({ status: '', severity: '' })
    expect(isCellActive({ severity: 'high', status: '' }, 'high')).toBe(true)
    expect(isCellActive({ severity: 'high', status: '' }, 'quarantined')).toBe(false)
  })

  it('lists only non-zero counts', () => {
    expect(countsLabel({ critical: 2, high: 0, medium: 0, low: 1 }, (s) => s)).toBe('2 critical, 1 low')
    expect(countsLabel({ critical: 0, high: 0, medium: 0, low: 0 }, (s) => s)).toBe('')
  })

  it('labels a finding with its package and links its Security tab', () => {
    const f = { repository: 'npm-internal', package_name: '@acme/ui-kit', package_version: '1.1.0' }
    expect(findingArtifact(f)).toBe('@acme/ui-kit@1.1.0')
    expect(findingLink(f)).toBe('/repos/npm-internal/packages/@acme/ui-kit/versions/1.1.0?tab=security')
    expect(findingArtifact({ package_name: 'keys/deploy.env', package_version: '' })).toBe('keys/deploy.env')
  })

  it('falls back to the repository when the package is gone', () => {
    const f = { repository: 'raw-assets', package_name: '', package_version: '' }
    expect(findingArtifact(f)).toBe('')
    expect(findingLink(f)).toBe('/repos/raw-assets?tab=security')
  })
})

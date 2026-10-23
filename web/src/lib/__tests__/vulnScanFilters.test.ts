// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  EMPTY_VULN_FILTERS,
  buildVulnQuery,
  hasVulnFilters,
  parseVulnFilters,
  toVulnScanFilters,
} from '../vulnScanFilters'

describe('vulnScanFilters', () => {
  it('parses known values and ignores unknown or malformed ones', () => {
    expect(
      parseVulnFilters({ repo: ' main ', severity: 'high', status: 'nope', scanner: 'trivy', quarantined: 'false' }),
    ).toEqual({ repo: 'main', severity: 'high', status: '', scanner: 'trivy', quarantined: 'false' })
    expect(parseVulnFilters({ severity: ['critical', 'low'], quarantined: 'yes', scanner: null })).toEqual({
      ...EMPTY_VULN_FILTERS,
      severity: 'critical',
    })
  })

  it('builds a query that keeps unrelated parameters and omits empty filters', () => {
    expect(buildVulnQuery({ keep: '1', status: 'failed' }, { ...EMPTY_VULN_FILTERS, severity: 'low' })).toEqual({
      keep: '1',
      severity: 'low',
    })
  })

  it('maps the filters to request parameters', () => {
    expect(toVulnScanFilters({ ...EMPTY_VULN_FILTERS, repo: 'r', quarantined: 'true', scanner: 'osv' })).toEqual({
      repo: 'r',
      severity: undefined,
      status: undefined,
      scanner: 'osv',
      quarantined: true,
    })
    expect(toVulnScanFilters({ ...EMPTY_VULN_FILTERS, quarantined: 'false' }).quarantined).toBe(false)
    expect(hasVulnFilters(EMPTY_VULN_FILTERS)).toBe(false)
    expect(hasVulnFilters({ ...EMPTY_VULN_FILTERS, status: 'failed' })).toBe(true)
  })
})

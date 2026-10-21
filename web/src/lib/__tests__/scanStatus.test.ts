// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  licenseActionLabel,
  licenseActionVariant,
  licenseQuarantineState,
  normalizeScannerSelection,
  vulnCountsState,
  vulnScanHasFailures,
  vulnScanLabel,
  vulnScanVariant,
  type VulnCountsState,
} from '../scanStatus'

describe('vuln scan status mapping', () => {
  it.each([
    ['failed', 'danger', 'Failed'],
    ['partial', 'warning', 'Partial'],
    ['completed', 'success', 'Completed'],
    ['scanning', 'warning', 'Scanning'],
    ['pending', 'neutral', 'Pending'],
  ])('%s is a %s badge labelled %s', (status, variant, label) => {
    expect(vulnScanVariant(status)).toBe(variant)
    expect(vulnScanLabel(status)).toBe(label)
  })

  it('shows an unknown status verbatim on a neutral badge', () => {
    expect(vulnScanVariant('weird')).toBe('neutral')
    expect(vulnScanLabel('weird')).toBe('weird')
  })

  it('flags only failed and partial scans as having scanner failures', () => {
    expect(['pending', 'scanning', 'completed', 'partial', 'failed'].filter(vulnScanHasFailures)).toEqual([
      'partial',
      'failed',
    ])
  })
})

describe('vulnCountsState', () => {
  it.each<[string, number, VulnCountsState]>([
    ['failed', 0, 'notScanned'],
    ['failed', 3, 'notScanned'],
    ['completed', 0, 'clean'],
    ['completed', 2, 'counts'],
    ['partial', 0, 'partialEmpty'],
    ['partial', 4, 'counts'],
    ['scanning', 0, 'pending'],
    ['pending', 0, 'pending'],
  ])('%s with %d findings renders as %s', (scan_status, vuln_count, want) => {
    expect(vulnCountsState({ scan_status, vuln_count } as Parameters<typeof vulnCountsState>[0])).toBe(want)
  })
})

describe('normalizeScannerSelection', () => {
  it.each([
    [[], ['osv']],
    [['osv'], ['osv']],
    [['grype', 'osv'], ['osv', 'grype']],
    [['trivy', 'trivy', 'bogus'], ['trivy']],
    [['grype', 'trivy', 'osv'], ['osv', 'trivy', 'grype']],
  ])('%j -> %j', (input, want) => {
    expect(normalizeScannerSelection(input)).toEqual(want)
  })
})

describe('license helpers', () => {
  it.each([
    [{ quarantined: true, quarantine_approved: false }, 'quarantined'],
    [{ quarantined: false, quarantine_approved: true }, 'approved'],
    [{ quarantined: false, quarantine_approved: false }, 'none'],
  ])('%j is %s', (scan, want) => {
    expect(licenseQuarantineState(scan)).toBe(want)
  })

  it('emphasises block over warn over allow', () => {
    expect(licenseActionVariant('block')).toBe('danger')
    expect(licenseActionVariant('warn')).toBe('warning')
    expect(licenseActionVariant('allow')).toBe('neutral')
    expect(licenseActionLabel('block')).toBe('Block')
    expect(licenseActionLabel('custom')).toBe('custom')
  })
})

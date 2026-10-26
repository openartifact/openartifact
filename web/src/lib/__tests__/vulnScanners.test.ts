// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { VULN_SCANNER_OPTIONS, safeDocsUrl, vulnScannerLabel } from '../vulnScanners'

describe('vulnScanners', () => {
  it('offers the three scanners with their display names', () => {
    expect(VULN_SCANNER_OPTIONS.map((s) => s.value)).toEqual(['osv', 'trivy', 'grype'])
    expect(vulnScannerLabel('trivy')).toBe('Trivy')
    expect(vulnScannerLabel('custom')).toBe('custom')
  })

  it('only follows https docs links', () => {
    expect(safeDocsUrl('https://example.test/doc')).toBe('https://example.test/doc')
    expect(safeDocsUrl('http://example.test/doc')).toBeUndefined()
    expect(safeDocsUrl('javascript:alert(1)')).toBeUndefined()
    expect(safeDocsUrl(null)).toBeUndefined()
  })
})

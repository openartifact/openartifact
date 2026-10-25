// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { VulnScanner, VulnScanRow } from '../api/security'

export const VULN_SCANNER_OPTIONS: readonly { value: VulnScanner; label: string }[] = [
  { value: 'osv', label: 'OSV-Scanner' },
  { value: 'trivy', label: 'Trivy' },
  { value: 'grype', label: 'Grype' },
]

/** Display name of a scanner; an unknown name is shown as is. */
export function vulnScannerLabel(name: string): string {
  return VULN_SCANNER_OPTIONS.find((x) => x.value === name)?.label ?? name
}

/** The link comes from the server; only follow https URLs. */
export function safeDocsUrl(url: string | null | undefined): string | undefined {
  return url && url.startsWith('https://') ? url : undefined
}

/** Scanners that produced a scan; scans recorded before the scanner column existed ran OSV. */
export function scannerNames(scan: Pick<VulnScanRow, 'scanner'>): string[] {
  const names = scan.scanner.split(',').filter(Boolean)
  return names.length ? names : ['osv']
}

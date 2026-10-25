// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { tDynamic } from '../i18n/en'
import type { LicenseScan, VulnScan, VulnScanner } from '../api/security'
import { VULN_SCANNERS } from '../api/security'

export type BadgeVariant = 'success' | 'warning' | 'neutral' | 'danger'

/** Vulnerability scan statuses the server can report. */
const VULN_SCAN_STATUSES = ['pending', 'scanning', 'completed', 'partial', 'failed'] as const

/**
 * Badge colour for a vulnerability scan status: failed is red, partial and
 * in-flight scans amber, completed green. Unknown statuses stay neutral.
 */
export function vulnScanVariant(status: string): BadgeVariant {
  switch (status) {
    case 'failed':
      return 'danger'
    case 'partial':
    case 'scanning':
      return 'warning'
    case 'completed':
      return 'success'
    default:
      return 'neutral'
  }
}

/** Translated label for a vulnerability scan status; unknown values are shown verbatim. */
export function vulnScanLabel(status: string): string {
  return (VULN_SCAN_STATUSES as readonly string[]).includes(status) ? tDynamic(`sec.vuln.status.${status}`) : status
}

/** True when the scan ended without a usable result from every scanner. */
export function vulnScanHasFailures(status: string): boolean {
  return status === 'failed' || status === 'partial'
}

export type VulnCountsState =
  /** Severity counts are meaningful (completed, or partial with findings). */
  | 'counts'
  /** Completed with no findings: genuinely clean. */
  | 'clean'
  /** Partial with no findings: only the scanners that ran found nothing. */
  | 'partialEmpty'
  /** Failed: nothing was scanned, so zero counts must not read as clean. */
  | 'notScanned'
  /** Pending or scanning: no result yet. */
  | 'pending'

/** Decides how the findings cell of a scan row is rendered. */
export function vulnCountsState(scan: Pick<VulnScan, 'scan_status' | 'vuln_count'>): VulnCountsState {
  switch (scan.scan_status) {
    case 'failed':
      return 'notScanned'
    case 'pending':
    case 'scanning':
      return 'pending'
    case 'partial':
      return scan.vuln_count > 0 ? 'counts' : 'partialEmpty'
    case 'completed':
      return scan.vuln_count > 0 ? 'counts' : 'clean'
    default:
      return scan.vuln_count > 0 ? 'counts' : 'pending'
  }
}

/**
 * Scanners to send for a selection: known names only, de-duplicated, in the
 * canonical osv/trivy/grype order; an empty selection falls back to osv.
 */
export function normalizeScannerSelection(selected: readonly string[]): VulnScanner[] {
  const picked = VULN_SCANNERS.filter((s) => selected.includes(s))
  return picked.length ? picked : ['osv']
}

type LicenseQuarantineState = 'quarantined' | 'approved' | 'none'

/**
 * Quarantine state of a license scan: `quarantined` blocks downloads;
 * `approved` means an admin released the artifact for its current licenses.
 */
export function licenseQuarantineState(
  scan: Pick<LicenseScan, 'quarantined' | 'quarantine_approved'>,
): LicenseQuarantineState {
  if (scan.quarantined) return 'quarantined'
  return scan.quarantine_approved ? 'approved' : 'none'
}

/** Badge colour of a license policy action: block is red, warn amber, allow neutral. */
export function licenseActionVariant(action: string | undefined): BadgeVariant {
  if (action === 'block') return 'danger'
  if (action === 'warn') return 'warning'
  return 'neutral'
}

/** Translated label of a license policy action; unknown values are shown verbatim. */
export function licenseActionLabel(action: string | undefined): string {
  return action === 'block' || action === 'warn' || action === 'allow'
    ? tDynamic(`sec.license.action.${action}`)
    : (action ?? '')
}

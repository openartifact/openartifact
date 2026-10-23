// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type {
  LicensePolicyViolation,
  LicenseScanResult,
  LicenseScanRunResult,
  VulnScanner as VulnScannerInfo,
  VulnScanRequest,
  VulnScanResult,
} from './generated/types.gen'

// Scan endpoints are admin-only and keyed by repository name. Every scan is
// recorded against an `artifact_id`, which for packages listed by
// GET /repositories/{name}/packages is the package `id`.

const repoBase = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}`

const jsonPost = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
})

/** Ecosystems (= format ids) the vulnerability scanners can map to OSV. */
export const VULN_ECOSYSTEMS = [
  'npm', 'pypi', 'go', 'cargo', 'rubygems', 'maven', 'nuget', 'deb', 'rpm', 'helm',
] as const

export type VulnScanner = 'osv' | 'trivy' | 'grype'

/** The scanners a vulnerability scan can run, in the order they are offered. */
export const VULN_SCANNERS: readonly VulnScanner[] = ['osv', 'trivy', 'grype']

/**
 * Whether a scanner can run on the server: `ready` is false when its binary is
 * missing or its vulnerability database is not there yet, and `message` /
 * `docs_url` then say how to fix it.
 */
export type VulnScannerStatus = VulnScannerInfo

export async function listVulnScanners(repo: string): Promise<VulnScannerStatus[]> {
  const res = await request<{ scanners?: VulnScannerStatus[] }>(`${repoBase(repo)}/vuln/scanners`)
  return res.scanners ?? []
}

/**
 * A vulnerability scan row. `scan_status` is pending | scanning | completed |
 * partial | failed; `scan_error` is a sanitized summary and `failed_scanners`
 * names the scanners that produced no result (failed and partial scans).
 */
export type VulnScan = VulnScanResult

export async function listVulnScans(repo: string): Promise<VulnScan[]> {
  const res = await request<{ scans?: VulnScan[] }>(`${repoBase(repo)}/vuln/scans`)
  return res.scans ?? []
}

export function startVulnScan(
  repo: string,
  body: Omit<VulnScanRequest, 'scanner' | 'scanners'> & { scanners: VulnScanner[] },
): Promise<VulnScan> {
  return request<VulnScan>(`${repoBase(repo)}/vuln/scan`, jsonPost(body))
}

export function setVulnQuarantine(repo: string, scanId: string, quarantined: boolean): Promise<VulnScan> {
  return request<VulnScan>(
    `${repoBase(repo)}/vuln/scans/${encodeURIComponent(scanId)}/quarantine`,
    jsonPost({ quarantined }),
  )
}

/** `quarantined` blocks downloads (403 ARTIFACT_QUARANTINED) until an admin approves the artifact. */
export type LicenseScan = LicenseScanResult
export type { LicensePolicyViolation, LicenseScanRunResult }

export async function listLicenseScans(repo: string): Promise<LicenseScan[]> {
  const res = await request<{ scans?: LicenseScan[] }>(`${repoBase(repo)}/license/scans`)
  return res.scans ?? []
}

/** Licenses are derived server-side from the artifact's stored properties. */
export function triggerLicenseScan(repo: string, artifactId: string): Promise<LicenseScanRunResult> {
  return request<LicenseScanRunResult>(`${repoBase(repo)}/license/scan`, jsonPost({ artifact_id: artifactId }))
}

/** Admin only: `false` approves a quarantined artifact, `true` quarantines it. */
export function setLicenseQuarantine(repo: string, scanId: string, quarantined: boolean): Promise<LicenseScan> {
  return request<LicenseScan>(
    `${repoBase(repo)}/license/scans/${encodeURIComponent(scanId)}/quarantine`,
    jsonPost({ quarantined }),
  )
}

export interface SecretFinding {
  type: string
  description: string
  severity: string
  redacted_value: string
  line_hint: string
}

export interface SecretScan {
  id: string
  artifact_id: string
  scan_status: string
  findings?: SecretFinding[] | null
  finding_count: number
  quarantined: boolean
  created_at: string
  scanned_at?: string | null
  scan_error?: string | null
}

export async function listSecretScans(repo: string): Promise<SecretScan[]> {
  const res = await request<{ scans?: SecretScan[] }>(`${repoBase(repo)}/secrets/scans`)
  return res.scans ?? []
}

/** Scans the supplied text; the server does not read artifact bytes itself. */
export function triggerSecretScan(repo: string, artifactId: string, content: string): Promise<SecretScan> {
  return request<SecretScan>(`${repoBase(repo)}/secrets/scan`, jsonPost({ artifact_id: artifactId, content }))
}

export function setSecretQuarantine(repo: string, scanId: string, quarantined: boolean): Promise<SecretScan> {
  return request<SecretScan>(
    `${repoBase(repo)}/secrets/scans/${encodeURIComponent(scanId)}/quarantine`,
    jsonPost({ quarantined }),
  )
}

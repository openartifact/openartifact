// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, downloadAttachment, JSON_HEADERS } from './client'
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
  headers: JSON_HEADERS,
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

export type VulnSeverity = 'critical' | 'high' | 'medium' | 'low'

export interface VulnScanSummary {
  total: number
  completed: number
  partial: number
  failed: number
  scanning: number
  quarantined: number
  /** Scans with at least one finding of the severity or worse (`high` counts critical + high). */
  severity_at_least: Record<VulnSeverity, number>
}

function queryString(params: Record<string, string | number | boolean | undefined>): string {
  const q = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') q.set(key, String(value))
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

/** Admin only: scan totals, optionally for one repository. */
export function getVulnScanSummary(repo?: string): Promise<VulnScanSummary> {
  return request<VulnScanSummary>(`/api/v1/vuln/scans/summary${queryString({ repo })}`)
}

/** One scan with its full findings. */
export function getVulnScan(repo: string, scanId: string): Promise<VulnScan> {
  return request<VulnScan>(`${repoBase(repo)}/vuln/scans/${encodeURIComponent(scanId)}`)
}

/** One finding of a scan, as stored by the scanners. */
interface VulnFinding {
  id: string
  summary: string
  severity: string
  cvss: number
  fixed_in: string
}

/** The findings of a full scan, with missing fields normalised. */
export function vulnFindings(scan: VulnScan): VulnFinding[] {
  return (scan.vulnerabilities ?? []).map((v) => ({
    id: String(v.id ?? ''),
    summary: String(v.summary ?? ''),
    severity: String(v.severity ?? ''),
    cvss: Number(v.cvss ?? 0),
    fixed_in: String(v.fixed_in ?? ''),
  }))
}

export function deleteVulnScan(repo: string, scanId: string): Promise<void> {
  return request<void>(`${repoBase(repo)}/vuln/scans/${encodeURIComponent(scanId)}`, { method: 'DELETE' })
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

/** Licenses are derived server-side from the stored package file and the artifact's license properties. */
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

export function getLicenseScan(repo: string, scanId: string): Promise<LicenseScan> {
  return request<LicenseScan>(`${repoBase(repo)}/license/scans/${encodeURIComponent(scanId)}`)
}

export function getSecretScan(repo: string, scanId: string): Promise<SecretScan> {
  return request<SecretScan>(`${repoBase(repo)}/secrets/scans/${encodeURIComponent(scanId)}`)
}

export function deleteSecretScan(repo: string, scanId: string): Promise<void> {
  return request<void>(`${repoBase(repo)}/secrets/scans/${encodeURIComponent(scanId)}`, { method: 'DELETE' })
}

// ── Findings inbox ────────────────────────────────────────────────────────────

export type FindingType = 'vuln' | 'license' | 'secret' | 'ai'
export const FINDING_TYPES: readonly FindingType[] = ['vuln', 'license', 'secret', 'ai']

export type FindingSeverity = 'critical' | 'high' | 'medium' | 'low' | 'none'
export type FindingStatus = 'open' | 'quarantined' | 'suppressed'

export interface FindingCounts {
  critical: number
  high: number
  medium: number
  low: number
}

/** One stored scan result (a package version of one type) that reports something. */
export interface SecurityFinding {
  id: string
  type: FindingType
  repository: string
  format: string
  package_name: string
  package_version: string
  /** The artifact the result belongs to, `name@version`. */
  artifact_id: string
  severity: FindingSeverity
  counts: FindingCounts
  /** One line: the worst vulnerability id, the licenses, the worst secret finding or the AI note. */
  title: string
  /** Scanner of a vulnerability result (osv, trivy, grype or a comma-joined list); empty for the other types. */
  scanner: string
  status: FindingStatus
  quarantined: boolean
  vex_status?: 'affected' | 'under_investigation' | 'fixed' | 'not_affected'
  scanned_at: string
}

/** Filters the findings list, summary and export share. `type` is a comma-separated list; empty means every type. */
export interface FindingFilters {
  type?: string
  repo?: string
  severity?: Exclude<FindingSeverity, 'none'>
  status?: FindingStatus
  q?: string
}

interface FindingsPage {
  findings: SecurityFinding[]
  has_more: boolean
  next_cursor?: string
}

export interface FindingTypeSummary extends FindingCounts {
  total: number
  quarantined: number
}

export interface SecuritySummary {
  by_type: Record<FindingType, FindingTypeSummary>
  scanned_repositories: number
  total_repositories: number
  last_scan_at: string | null
}

export interface SecurityHealth {
  scanners: { name: string; ready: boolean; message?: string; docs_url?: string }[]
  last_scan_at: string | null
  scans_pending: number
  scans_failed_24h: number
  dependency_track: { enabled: boolean; last_push_ok?: boolean; last_push_at?: string }
  ai: { enabled: boolean }
  unscanned_repositories: number
}

/** The findings the caller may triage, worst first, keyset-paginated. Admins see every repository; project maintainers their own. */
export async function listFindings(params: FindingFilters & { limit?: number; cursor?: string }): Promise<FindingsPage> {
  const res = await request<Partial<FindingsPage>>(`/api/v1/security/findings${queryString({ ...params })}`)
  return { findings: res.findings ?? [], has_more: res.has_more ?? false, next_cursor: res.next_cursor }
}

/** Counts per type and severity; follows the repository filter only. */
export function getFindingsSummary(repo?: string): Promise<SecuritySummary> {
  return request<SecuritySummary>(`/api/v1/security/findings/summary${queryString({ repo })}`)
}

/** Admin only: scanner readiness and scan backlog. */
export function getSecurityHealth(): Promise<SecurityHealth> {
  return request<SecurityHealth>('/api/v1/security/health')
}

/** `vex` is a CycloneDX VEX document of the vulnerability findings. */
export type FindingsExportFormat = 'csv' | 'json' | 'vex'
export const FINDINGS_EXPORT_FORMATS: readonly FindingsExportFormat[] = ['csv', 'json', 'vex']

/** Export of the findings the inbox filters select, not only the loaded pages. */
export function findingsExportUrl(filters: FindingFilters, format: FindingsExportFormat): string {
  return `/api/v1/security/findings/export${queryString({ ...filters, format })}`
}

/** SARIF 2.1.0 (GitHub code scanning, DefectDojo), CSV or JSON export of one vulnerability scan. */
export type ScanExportFormat = 'sarif' | 'csv' | 'json'
export const SCAN_EXPORT_FORMATS: readonly ScanExportFormat[] = ['sarif', 'csv', 'json']

export function vulnScanExportUrl(repo: string, scanId: string, format: ScanExportFormat): string {
  return `${repoBase(repo)}/vuln/scans/${encodeURIComponent(scanId)}/export${queryString({ format })}`
}

/**
 * Downloads an export route through the shared authenticated path: a refusal
 * (forbidden, invalid filter) surfaces as an ApiError, then the browser saves the file.
 */
export function downloadScanExport(url: string, fallbackName: string): Promise<void> {
  return downloadAttachment(url, fallbackName)
}

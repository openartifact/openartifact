// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { severityPriority } from '../lib/exploitPriority'
import { request, downloadAttachment, JSON_HEADERS } from './client'
import type {
  ExploitAnnotation,
  ExploitFeed,
  ExploitFeedsResponse,
  ExploitPriority,
  LicensePolicyViolation,
  LicenseScanResult,
  LicenseScanRunResult,
  OsvMirrorStatus,
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

/** Ecosystems (= format ids) the vulnerability scanners can map to OSV; generated from the Go registry. */
export { VULN_ECOSYSTEMS } from '../lib/formats.generated'

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

export type { ExploitAnnotation, ExploitFeed, ExploitFeedsResponse, ExploitPriority }

/** One finding of a scan, as stored by the scanners, with what the KEV and EPSS feeds say about it. */
export interface VulnFinding {
  id: string
  summary: string
  severity: string
  cvss: number
  fixed_in: string
  /** The CVE id the signals were looked up by; '' when the finding has none. */
  cve: string
  kev: boolean
  kev_date_added: string
  kev_due_date: string
  kev_ransomware: boolean
  epss: number | null
  epss_percentile: number | null
  epss_date: string | null
  priority: ExploitPriority
}

/** The findings of a full scan, with missing fields normalised (a server without exploitation data sends none of the signals). */
export function vulnFindings(scan: VulnScan): VulnFinding[] {
  return (scan.vulnerabilities ?? []).map((v) => {
    const severity = String(v.severity ?? '')
    return {
      id: String(v.id ?? ''),
      summary: String(v.summary ?? ''),
      severity,
      cvss: Number(v.cvss ?? 0),
      fixed_in: String(v.fixed_in ?? ''),
      cve: String(v.cve ?? ''),
      kev: v.kev === true,
      kev_date_added: String(v.kev_date_added ?? ''),
      kev_due_date: String(v.kev_due_date ?? ''),
      kev_ransomware: v.kev_ransomware === true,
      epss: v.epss ?? null,
      epss_percentile: v.epss_percentile ?? null,
      epss_date: v.epss_date ?? null,
      priority: v.priority ?? severityPriority(severity),
    }
  })
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
  /** Vulnerabilities of a vuln finding in the CISA KEV catalog; 0 for the other types. */
  kev_count: number
  /** Highest EPSS score (0..1) among the vulnerabilities; null when none is scored. */
  max_epss: number | null
  priority: ExploitPriority
  scanned_at: string
}

/** Filters the findings list, summary and export share. `type` is a comma-separated list; empty means every type. */
export interface FindingFilters {
  type?: string
  repo?: string
  severity?: Exclude<FindingSeverity, 'none'>
  status?: FindingStatus
  q?: string
  /** `severity` (the server's default) or `priority`: KEV first, then high EPSS, then the severity. A cursor is only valid under the sort it was issued for. */
  sort?: 'severity' | 'priority'
  /** `true`: only findings with a vulnerability in the KEV catalog; `false`: only those without. */
  kev?: boolean
  /** Only findings whose highest EPSS score is at least this (0..1). */
  epss_min?: number
  /** Only findings with exactly this priority. */
  priority?: Exclude<ExploitPriority, 'none'>
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

/** State of the KEV and EPSS feeds and the EPSS score from which a finding has priority `epss`. */
export function getExploitFeeds(): Promise<ExploitFeedsResponse> {
  return request<ExploitFeedsResponse>('/api/v1/security/exploit-feeds')
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

/** Admin only: state of the local OSV advisory mirror. */
export function getOsvMirrorStatus(): Promise<OsvMirrorStatus> {
  return request<OsvMirrorStatus>('/api/v1/vuln/osv-mirror')
}

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, preflight } from './client'
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

export type VulnSeverity = 'critical' | 'high' | 'medium' | 'low'
export type VulnScanStatusFilter = 'completed' | 'partial' | 'failed' | 'scanning'

/**
 * A row of the cross-repository scan list: the scan summary (no findings) plus
 * the repository it belongs to, which every per-repository action needs.
 */
export type VulnScanRow = Omit<VulnScan, 'vulnerabilities'> & {
  repository_name: string
  repository_format: string
}

export interface VulnScanFilters {
  /** Repository name. */
  repo?: string
  /** Scans with at least one finding of this severity or worse. */
  severity?: VulnSeverity
  status?: VulnScanStatusFilter
  /** Matches scans whose scanner list contains it. */
  scanner?: VulnScanner
  quarantined?: boolean
}

export interface VulnScanListParams extends VulnScanFilters {
  limit?: number
  /** Opaque `next_cursor` of the previous page. */
  cursor?: string
}

export interface VulnScanPage {
  scans: VulnScanRow[]
  has_more: boolean
  next_cursor?: string
}

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

/** Admin only: scans across every repository, worst first, keyset-paginated. */
export async function listAllVulnScans(params: VulnScanListParams = {}): Promise<VulnScanPage> {
  const res = await request<Partial<VulnScanPage>>(`/api/v1/vuln/scans${queryString({ ...params })}`)
  return { scans: res.scans ?? [], has_more: res.has_more ?? false, next_cursor: res.next_cursor }
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
export interface VulnFinding {
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

// ── Exports ───────────────────────────────────────────────────────────────────

/** SARIF 2.1.0 (GitHub code scanning, DefectDojo), CSV or JSON. */
export type ScanExportFormat = 'sarif' | 'csv' | 'json'

export const SCAN_EXPORT_FORMATS: readonly ScanExportFormat[] = ['sarif', 'csv', 'json']

/** Export of the scans the overview filters select, across repositories. */
export function vulnScansExportUrl(filters: VulnScanFilters, format: ScanExportFormat): string {
  return `/api/v1/vuln/scans/export${queryString({ ...filters, format })}`
}

/** Export of the secret scans of one repository. */
export function secretScansExportUrl(repo: string, format: ScanExportFormat): string {
  return `${repoBase(repo)}/secrets/scans/export${queryString({ format })}`
}

/**
 * Downloads an export route. A preflight reports what the server would answer
 * (forbidden, invalid filter) as an ApiError; the browser then fetches the
 * file itself, so it streams to disk and the session cookie applies.
 */
export async function downloadScanExport(url: string, format: ScanExportFormat): Promise<void> {
  const { filename } = await preflight(url)

  const link = document.createElement('a')
  link.href = url
  link.download = filename ?? `scans.${format}`
  document.body.appendChild(link)
  link.click()
  link.remove()
}

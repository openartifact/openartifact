// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type {
  GetApiV1RepositoriesByNameLicenseScansByIdResponses,
  GetApiV1RepositoriesByNameVexStatementsResponses,
  GetApiV1RepositoriesByNameVulnScansByIdResponses,
  GetApiV1RepositoriesByNameVulnScansResponses,
  GetApiV1SecurityExploitFeedsResponses,
  GetApiV1SecurityFindingsResponses,
  GetApiV1SecurityFindingsSummaryResponses,
  GetApiV1SecurityHealthResponses,
  GetApiV1VulnOsvMirrorResponses,
  SecurityFinding,
  VulnScanResult,
} from '../../api/generated/types.gen'
import type { SecretScan } from '../../api/security'
import type { DemoRepository } from '../fixtures/repos'
import { ago } from '../fixtures/time'
import { failure, type DemoRequest, type DemoRoute, type Ok } from '../http'
import { matches, repoFiles } from '../query'
import type { DemoFile, DemoState, DemoStoredFinding } from '../state'
import { withRepo } from './repos'

const SEVERITY_RANK = { critical: 0, high: 1, medium: 2, low: 3, none: 4 } as const
const PRIORITY_RANK = { kev: 0, epss: 1, critical: 2, high: 3, medium: 4, low: 5, none: 6 } as const

/** The findings inbox: filtered, worst first, one page (the demo has fewer findings than a page). */
function listFindings({ state, query }: DemoRequest): Ok<GetApiV1SecurityFindingsResponses> {
  const types = query.get('type')?.split(',').filter(Boolean) ?? []
  const epssMin = query.get('epss_min')
  const rows = state.findings.filter(
    (f) =>
      (types.length === 0 || types.includes(f.type)) &&
      (!query.get('repo') || f.repository === query.get('repo')) &&
      (!query.get('severity') || f.severity === query.get('severity')) &&
      (!query.get('status') || f.status === query.get('status')) &&
      (!query.get('priority') || f.priority === query.get('priority')) &&
      (query.get('kev') === null || (query.get('kev') === 'true') === f.kev_count > 0) &&
      (epssMin === null || (f.max_epss ?? 0) >= Number(epssMin)) &&
      (matches(f.package_name, query.get('q')) || matches(f.title, query.get('q'))),
  )
  const rank = query.get('sort') === 'priority' ? (f: SecurityFinding) => PRIORITY_RANK[f.priority] : (f: SecurityFinding) => SEVERITY_RANK[f.severity]
  const limit = Number(query.get('limit')) || 25
  const page = [...rows].sort((a, b) => rank(a) - rank(b) || b.scanned_at.localeCompare(a.scanned_at)).slice(0, limit)
  return { findings: page.map(({ advisories: _a, licenses: _l, secrets: _s, ...f }) => f), has_more: false }
}

function summary({ state, query }: DemoRequest): Ok<GetApiV1SecurityFindingsSummaryResponses> {
  const repo = query.get('repo')
  const findings = state.findings.filter((f) => !repo || f.repository === repo)
  const of = (type: SecurityFinding['type']) => {
    const rows = findings.filter((f) => f.type === type)
    const count = (s: SecurityFinding['severity']) => rows.filter((f) => f.severity === s).length
    return { total: rows.length, critical: count('critical'), high: count('high'), medium: count('medium'), low: count('low'), quarantined: rows.filter((f) => f.quarantined).length }
  }
  const hosted = state.repositories.filter((r) => r.repo_type !== 'group' && (!repo || r.name === repo))
  return {
    by_type: { vuln: of('vuln'), license: of('license'), secret: of('secret'), ai: of('ai') },
    scanned_repositories: hosted.filter((r) => repoFiles(state, r).some((f) => f.scanned_at)).length,
    total_repositories: hosted.length,
    last_scan_at: state.files.reduce<string | null>((at, f) => (f.scanned_at && (!at || f.scanned_at > at) ? f.scanned_at : at), null),
  }
}

function health({ state }: DemoRequest): Ok<GetApiV1SecurityHealthResponses> {
  return {
    scanners: [
      { name: 'osv', ready: true },
      { name: 'trivy', ready: true },
      { name: 'grype', ready: true },
      { name: 'secrets', ready: true },
      { name: 'license', ready: true },
    ],
    last_scan_at: ago(0.3),
    scans_pending: 0,
    scans_failed_24h: 0,
    dependency_track: { enabled: false },
    ai: { enabled: false },
    unscanned_repositories: state.repositories.filter((r) => r.repo_type !== 'group' && repoFiles(state, r).length === 0).length,
  }
}

const exploitFeeds = (): Ok<GetApiV1SecurityExploitFeedsResponses> => ({
  enabled: true,
  epss_threshold: 0.1,
  feeds: [
    { name: 'kev', source_url: 'https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json', version: ago(40).slice(0, 10).replaceAll('-', '.'), published_at: ago(40), entry_count: 1384, imported_at: ago(6), checked_at: ago(1), last_error: '' },
    { name: 'epss', source_url: 'https://epss.cyentia.com/epss_scores-current.csv.gz', version: 'v2025.03.14', published_at: ago(14), entry_count: 289_412, imported_at: ago(6), checked_at: ago(1), last_error: '' },
  ],
})

const osvMirror = (): Ok<GetApiV1VulnOsvMirrorResponses> => ({
  enabled: true,
  source_url: 'https://osv-vulnerabilities.storage.googleapis.com',
  interval_seconds: 3600,
  ecosystems: ['npm', 'Maven', 'PyPI', 'crates.io', 'Go', 'Debian'].map((ecosystem, i) => ({
    ecosystem,
    populated: true,
    advisories: [21_480, 5_912, 15_337, 1_904, 5_120, 48_660][i] ?? 0,
    high_water: ago(2),
    last_checked_at: ago(0.5),
    last_imported_at: ago(2),
    last_import_seconds: 14 + i * 3,
    last_rematch_seconds: 2,
    last_error: null,
    rematch_pending: false,
  })),
  attribution: { source: 'OSV', license: 'CC-BY-4.0', url: 'https://osv.dev' },
})

/** One scan per package version (its first file): the version's vulnerability finding, or a clean result. */
function vulnScan(state: DemoState, repo: DemoRepository, file: DemoFile, full: boolean): VulnScanResult {
  const f = state.findings.find((x) => x.type === 'vuln' && x.artifact_id === file.id)
  const vulns = f?.advisories ?? []
  return {
    id: f?.id ?? file.id,
    repo_id: repo.id,
    artifact_id: file.id,
    package_name: file.name,
    package_version: file.version,
    ecosystem: file.format,
    scanner: f?.scanner ?? 'osv',
    scan_status: 'completed',
    vulnerabilities: full ? vulns : undefined,
    vuln_count: vulns.length,
    critical_count: f?.counts.critical ?? 0,
    high_count: f?.counts.high ?? 0,
    medium_count: f?.counts.medium ?? 0,
    low_count: f?.counts.low ?? 0,
    kev_count: f?.kev_count ?? 0,
    max_epss: f?.max_epss ?? null,
    priority: f?.priority ?? 'none',
    quarantined: f?.quarantined ?? false,
    created_at: file.created_at,
    updated_at: file.scanned_at ?? file.created_at,
    scanned_at: file.scanned_at ?? null,
    scan_error: null,
    failed_scanners: [],
  }
}

function versionHeads(files: DemoFile[]): DemoFile[] {
  const seen = new Set<string>()
  return files.filter((f) => !seen.has(`${f.repository}/${f.name}/${f.version}`) && !!seen.add(`${f.repository}/${f.name}/${f.version}`))
}

function listVulnScans({ state }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameVulnScansResponses> {
  return { scans: versionHeads(repoFiles(state, repo)).map((f) => vulnScan(state, repo, f, false)) }
}

function getVulnScan({ state, params }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameVulnScansByIdResponses> | Response {
  const files = repoFiles(state, repo)
  const artifact = state.findings.find((f) => f.id === params.id)?.artifact_id ?? params.id
  const file = files.find((f) => f.id === artifact)
  return file ? vulnScan(state, repo, file, true) : failure(404, 'scan not found', 'not_found')
}

/** The finding of the given type behind a license or secret scan id. */
function scanFinding(state: DemoState, repo: DemoRepository, id: string | undefined, type: SecurityFinding['type']): DemoStoredFinding | undefined {
  return state.findings.find((f) => f.id === id && f.type === type && (f.repository === repo.name || repo.member_repos?.includes(f.repository)))
}

function getLicenseScan({ state, params }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameLicenseScansByIdResponses> | Response {
  const f = scanFinding(state, repo, params.id, 'license')
  if (!f) return failure(404, 'scan not found', 'not_found')
  return {
    id: f.id,
    repo_id: repo.id,
    artifact_id: f.artifact_id,
    licenses: f.licenses,
    spdx_expressions: f.licenses,
    license_source: 'artifact',
    scan_status: 'completed',
    scan_error: null,
    scanned_at: f.scanned_at,
    created_at: f.scanned_at,
    updated_at: f.scanned_at,
    quarantined: f.quarantined,
    quarantine_approved: false,
  }
}

function getSecretScan({ state, params }: DemoRequest, repo: DemoRepository): SecretScan | Response {
  const f = scanFinding(state, repo, params.id, 'secret')
  if (!f) return failure(404, 'scan not found', 'not_found')
  return {
    id: f.id,
    artifact_id: f.artifact_id,
    scan_status: 'completed',
    findings: f.secrets,
    finding_count: f.secrets?.length ?? 0,
    quarantined: f.quarantined,
    created_at: f.scanned_at,
    scanned_at: f.scanned_at,
  }
}

/** The recorded decisions: every advisory of a finding marked with a VEX status. */
function vexStatements({ state, query }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameVexStatementsResponses> {
  const artifact = query.get('artifact_id')
  return {
    statements: state.findings
      .filter((f) => f.repository === repo.name && f.vex_status && (!artifact || f.artifact_id === artifact))
      .flatMap((f) =>
        (f.advisories ?? []).map((a) => ({
          id: `${f.id}-${a.id}`,
          artifact_id: f.artifact_id,
          vuln_id: a.id,
          status: f.vex_status,
          justification: 'vulnerable_code_not_in_execute_path',
          impact_statement: 'The service never interpolates untrusted input.',
          created_by: 'carla.rossi',
          created_at: f.scanned_at,
          updated_at: f.scanned_at,
        })),
      ),
  }
}

export const securityRoutes: DemoRoute[] = [
  ['GET', '/api/v1/security/findings', listFindings],
  ['GET', '/api/v1/security/findings/summary', summary],
  ['GET', '/api/v1/security/health', health],
  ['GET', '/api/v1/security/exploit-feeds', exploitFeeds],
  ['GET', '/api/v1/vuln/osv-mirror', osvMirror],
  ['GET', '/api/v1/repositories/:name/vuln/scans', withRepo(listVulnScans)],
  ['GET', '/api/v1/repositories/:name/vuln/scans/:id', withRepo(getVulnScan)],
  ['GET', '/api/v1/repositories/:name/license/scans/:id', withRepo(getLicenseScan)],
  ['GET', '/api/v1/repositories/:name/secrets/scans/:id', withRepo(getSecretScan)],
  ['GET', '/api/v1/repositories/:name/vex/statements', withRepo(vexStatements)],
]

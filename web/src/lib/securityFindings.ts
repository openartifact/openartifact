// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  FINDING_TYPES,
  type FindingCounts,
  type FindingFilters,
  type FindingSeverity,
  type FindingType,
  type SecurityFinding,
  type SecuritySummary,
} from '../api/security'
import { boolParam, enumParam, strParam } from '../composables/useUrlState'
import { artifactPath } from './artifactPath'
import { EPSS_MIN_OPTIONS, PRIORITY_FILTERS, type EpssMin, type PriorityFilter } from './exploitPriority'
import { vulnScannerLabel } from './vulnScanners'
import type { BadgeVariant } from './scanStatus'

export const TYPE_FILTERS = ['all', ...FINDING_TYPES] as const
export type TypeFilter = (typeof TYPE_FILTERS)[number]

export const SEVERITY_FILTERS = ['critical', 'high', 'medium', 'low'] as const
export type SeverityFilter = (typeof SEVERITY_FILTERS)[number]

export const STATUS_FILTERS = ['open', 'quarantined', 'suppressed'] as const
export type StatusFilter = (typeof STATUS_FILTERS)[number]

export const SORTS = ['severity', 'priority'] as const
export type InboxSort = (typeof SORTS)[number]

/**
 * The inbox's URL state: `?type=vuln|license|secret|ai&severity=&status=&repo=&q=&sort=priority&kev=1&epss_min=0.1&priority=kev`.
 * An absent parameter means "no filter" (and the severity order).
 */
export const INBOX_FILTERS = {
  type: enumParam<TypeFilter>(TYPE_FILTERS, 'all'),
  severity: enumParam<SeverityFilter | ''>(['', ...SEVERITY_FILTERS], ''),
  status: enumParam<StatusFilter | ''>(['', ...STATUS_FILTERS], ''),
  repo: strParam(),
  q: strParam(),
  sort: enumParam<InboxSort>(SORTS, 'severity'),
  kev: boolParam(),
  epss_min: enumParam<EpssMin | ''>(['', ...EPSS_MIN_OPTIONS], ''),
  priority: enumParam<PriorityFilter | ''>(['', ...PRIORITY_FILTERS], ''),
}

export interface InboxFilters {
  type: TypeFilter
  severity: SeverityFilter | ''
  status: StatusFilter | ''
  repo: string
  q: string
  sort: InboxSort
  kev: boolean
  epss_min: EpssMin | ''
  priority: PriorityFilter | ''
}

/** Request parameters of the inbox filters: only the set ones, `all` becoming "every type". */
export function toFindingFilters(f: InboxFilters): FindingFilters {
  return {
    type: f.type === 'all' ? undefined : f.type,
    repo: f.repo || undefined,
    severity: f.severity || undefined,
    status: f.status || undefined,
    q: f.q.trim() || undefined,
    sort: f.sort === 'severity' ? undefined : f.sort,
    kev: f.kev || undefined,
    epss_min: f.epss_min ? Number(f.epss_min) : undefined,
    priority: f.priority || undefined,
  }
}

/** A filter beyond the type switch narrows the list: an empty result is then "no matches", not "all clear". The sort order does not. */
export function hasNarrowingFilters(f: InboxFilters): boolean {
  return f.severity !== '' || f.status !== '' || f.q.trim() !== '' || f.kev || f.epss_min !== '' || f.priority !== ''
}

const EMPTY_COUNTS = { total: 0, critical: 0, high: 0, medium: 0, low: 0, quarantined: 0 }

/** What the summary strip shows: the counts of the selected type, or the sum of every type. */
export function summaryTotals(summary: SecuritySummary, type: TypeFilter): typeof EMPTY_COUNTS {
  const types = type === 'all' ? FINDING_TYPES : [type]
  const sum = { ...EMPTY_COUNTS }
  for (const ty of types) {
    const part = summary.by_type[ty]
    if (!part) continue
    for (const key of Object.keys(sum) as (keyof typeof sum)[]) sum[key] += part[key] ?? 0
  }
  return sum
}

/** Findings of one type in the scope, for the type switch badge. */
export function typeTotal(summary: SecuritySummary | null, type: TypeFilter): number | undefined {
  return summary ? summaryTotals(summary, type).total : undefined
}

/**
 * The strip cell a click on `cell` toggles: severities set `severity`, the
 * quarantine cell sets `status=quarantined`; clicking the active cell clears it.
 */
export function stripPatch(
  filters: Pick<InboxFilters, 'severity' | 'status'>,
  cell: SeverityFilter | 'quarantined',
): Partial<Pick<InboxFilters, 'severity' | 'status'>> {
  if (cell === 'quarantined') return { status: filters.status === 'quarantined' ? '' : 'quarantined', severity: '' }
  return { severity: filters.severity === cell ? '' : cell, status: '' }
}

export function isCellActive(filters: Pick<InboxFilters, 'severity' | 'status'>, cell: SeverityFilter | 'quarantined'): boolean {
  return cell === 'quarantined' ? filters.status === 'quarantined' : filters.severity === cell
}

/** "2 critical, 1 high"; the counts that are zero are left out. */
export function countsLabel(counts: FindingCounts, label: (severity: SeverityFilter) => string): string {
  return SEVERITY_FILTERS.filter((s) => counts[s] > 0)
    .map((s) => `${counts[s]} ${label(s)}`)
    .join(', ')
}

export function statusVariant(status: SecurityFinding['status']): BadgeVariant {
  if (status === 'quarantined') return 'danger'
  if (status === 'suppressed') return 'neutral'
  return 'warning'
}

/** Scanner names of a vulnerability finding as displayed; the other types have no scanner. */
export function scannerLabels(f: Pick<SecurityFinding, 'type' | 'scanner'>): string[] {
  if (f.type !== 'vuln') return []
  return f.scanner
    .split(',')
    .filter(Boolean)
    .map(vulnScannerLabel)
}

export interface FindingRow {
  id: string
  type: FindingType
  repository: string
  format: string
  severity: FindingSeverity
  title: string
  /** `name@version`: names, never ids; '' when the package no longer exists. */
  artifact: string
  scanners: string[]
  status: SecurityFinding['status']
  vexStatus?: SecurityFinding['vex_status']
  scannedAt: string
  /** Vulnerabilities in the CISA KEV catalog. */
  kevCount: number
  /** Highest EPSS score (0..1), null when none is scored. */
  maxEpss: number | null
  priority: SecurityFinding['priority']
  /** The quarantine toggle exists for this type (AI analyses only recommend). */
  canQuarantine: boolean
  quarantined: boolean
  source: SecurityFinding
}

/** `name@version`, the bare name for formats without versions, '' when the package is gone (the API never sends an id as the name). */
export function findingArtifact(f: Pick<SecurityFinding, 'package_name' | 'package_version'>): string {
  return f.package_version ? `${f.package_name}@${f.package_version}` : f.package_name
}

/** Where a finding's package is reviewed: its Security tab, or the repository's when the package no longer exists. */
export function findingLink(f: Pick<SecurityFinding, 'repository' | 'package_name' | 'package_version'>): string {
  if (!f.package_name) return `/repos/${encodeURIComponent(f.repository)}?tab=security`
  return `${artifactPath(f.repository, f.package_name, f.package_version)}?tab=security`
}

/** One table row per finding; what differs per type is the title text, the scanner list and whether quarantine can be toggled. */
export function toFindingRow(f: SecurityFinding): FindingRow {
  return {
    id: f.id,
    type: f.type,
    repository: f.repository,
    format: f.format,
    severity: f.severity,
    title: f.title,
    artifact: findingArtifact(f),
    scanners: scannerLabels(f),
    status: f.status,
    vexStatus: f.vex_status,
    scannedAt: f.scanned_at,
    kevCount: f.kev_count ?? 0,
    maxEpss: f.max_epss ?? null,
    priority: f.priority,
    canQuarantine: f.type !== 'ai',
    quarantined: f.quarantined,
    source: f,
  }
}

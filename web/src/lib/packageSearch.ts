// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ChecksumHit, PackageGroup } from '../api/search'
import { artifactPath } from './artifactPath'
import { checksumAlgorithm, effectiveMode, normalizeChecksum, type SearchMode } from './checksum'

export interface SearchFilters {
  q: string
  format: string
  repo: string
  flagged: boolean
  /** Explicit `?mode=`; empty means "detect from the query". */
  mode: '' | SearchMode
}

/** What a set of URL filters asks the server for. */
export type SearchPlan =
  | { kind: 'idle' }
  | { kind: 'invalid-checksum' }
  | { kind: 'name'; q: string }
  | { kind: 'checksum'; checksum: string }

export function planSearch(f: SearchFilters): SearchPlan {
  const q = f.q.trim()
  if (!q) return { kind: 'idle' }
  if (effectiveMode(f.mode, q) === 'checksum') {
    return checksumAlgorithm(q) ? { kind: 'checksum', checksum: normalizeChecksum(q) } : { kind: 'invalid-checksum' }
  }
  return { kind: 'name', q }
}

/** True when the checksum mode was chosen by detection, not by the toggle. */
export function isAutoChecksum(f: Pick<SearchFilters, 'q' | 'mode'>): boolean {
  return f.mode === '' && checksumAlgorithm(f.q) !== null
}

/** One row of the grouped result table: a package across its versions. */
export interface PackageRow {
  id: string
  repository: string
  format: string
  name: string
  latestVersion: string
  versionCount: number
  href: string
  repositoryHref: string
}

export function groupRow(g: PackageGroup): PackageRow {
  return {
    id: `${g.repository}\u0001${g.name}`,
    repository: g.repository,
    format: g.format,
    name: g.name,
    latestVersion: g.latest_version,
    versionCount: g.version_count,
    href: artifactPath(g.repository, g.name),
    repositoryHref: `/repos/${encodeURIComponent(g.repository)}`,
  }
}

/** One row of the checksum result table: a stored file whose digest matched. */
export interface ChecksumRow {
  id: string
  repository: string
  format: string
  name: string
  version: string
  filename: string
  checksum: string
  algorithm: string
  href: string
  repositoryHref: string
}

export function checksumRow(h: ChecksumHit): ChecksumRow {
  return {
    id: `${h.repository}\u0001${h.name}\u0001${h.version}\u0001${h.filename}`,
    repository: h.repository,
    format: h.format,
    name: h.name,
    version: h.version,
    filename: h.filename,
    checksum: h.checksum,
    algorithm: h.algorithm,
    href: artifactPath(h.repository, h.name, h.version),
    repositoryHref: `/repos/${encodeURIComponent(h.repository)}`,
  }
}

/** One version of a package in the expanded row. */
export interface VersionRow {
  id: string
  version: string
  created_at?: string
}

/** A version can have several files (one row each in the listing): keep the newest row per version, newest first. */
export function distinctVersions(items: readonly { id: string; version: string; created_at?: string }[]): VersionRow[] {
  const byVersion = new Map<string, VersionRow>()
  for (const it of items) {
    const seen = byVersion.get(it.version)
    if (!seen || (it.created_at ?? '') > (seen.created_at ?? '')) {
      byVersion.set(it.version, { id: it.version, version: it.version, created_at: it.created_at })
    }
  }
  return [...byVersion.values()].sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''))
}

export type SearchRow = PackageRow | ChecksumRow

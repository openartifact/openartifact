// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { PackageItem } from '../api/repositories'

/** Version strings that mark a pre-release; matched anywhere after a separator. */
const PRERELEASE = /[-.](rc|alpha|beta|snapshot|preview|dev|pre)(?![a-z])/i

export function isPrerelease(version: string): boolean {
  return PRERELEASE.test(version)
}

interface SeverityCounts {
  critical: number
  high: number
  medium: number
  low: number
}

/** One version of a package: every listed row (file) that shares its version. */
export interface VersionEntry {
  version: string
  /** Files of the version, as listed (a deb/rpm per architecture, a maven jar + pom, ...). */
  files: PackageItem[]
  /** Sum of the file sizes. */
  size: number
  /** Publication time: the oldest file's creation time. */
  createdAt: string
  /** Summed vulnerability counts; null when the server did not report them. */
  security: SeverityCounts | null
  /** Latest completed scan of any file; null when never scanned or not reported. */
  scannedAt: string | null
  /** A critical/high vulnerability, a secret finding or a quarantined result on any file. */
  flagged: boolean
  latest: boolean
  prerelease: boolean
}

const time = (value: string | undefined): number => {
  const ms = value ? Date.parse(value) : NaN
  return Number.isNaN(ms) ? 0 : ms
}

/**
 * Groups the rows of one package into versions, newest first. The latest
 * version is the one published most recently; ties keep the listing order.
 */
export function groupVersions(rows: readonly PackageItem[]): VersionEntry[] {
  const byVersion = new Map<string, PackageItem[]>()
  for (const row of rows) {
    const list = byVersion.get(row.version)
    if (list) list.push(row)
    else byVersion.set(row.version, [row])
  }

  const entries: VersionEntry[] = []
  for (const [version, files] of byVersion) {
    const reported = files.filter((f) => f.security)
    const created = files.map((f) => time(f.created_at)).filter((ms) => ms > 0)
    const scanned = files.map((f) => f.scanned_at).filter((s): s is string => !!s)
    entries.push({
      version,
      files,
      size: files.reduce((sum, f) => sum + (f.size ?? 0), 0),
      createdAt: created.length ? new Date(Math.min(...created)).toISOString() : '',
      security: reported.length
        ? reported.reduce<SeverityCounts>(
            (acc, f) => ({
              critical: acc.critical + f.security!.critical,
              high: acc.high + f.security!.high,
              medium: acc.medium + f.security!.medium,
              low: acc.low + f.security!.low,
            }),
            { critical: 0, high: 0, medium: 0, low: 0 },
          )
        : null,
      scannedAt: scanned.length ? scanned.reduce((a, b) => (time(a) >= time(b) ? a : b)) : null,
      flagged: files.some((f) => f.flagged === true),
      latest: false,
      prerelease: isPrerelease(version),
    })
  }

  // Array.sort is stable: equal timestamps keep the listing order.
  entries.sort((a, b) => time(b.createdAt) - time(a.createdAt))
  if (entries[0]) entries[0].latest = true
  return entries
}

/** The entry for the requested version; the latest one when none is requested; null when it does not exist. */
export function selectVersion(entries: readonly VersionEntry[], requested: string | undefined): VersionEntry | null {
  if (!requested) return entries[0] ?? null
  return entries.find((e) => e.version === requested) ?? null
}

/** Total of the vulnerability counts. */
export function countTotal(counts: SeverityCounts | null | undefined): number {
  return counts ? counts.critical + counts.high + counts.medium + counts.low : 0
}

/** The file of a version a page-level action (download, SBOM, properties) targets: the first listed. */
export function primaryFile(entry: VersionEntry): PackageItem | undefined {
  return entry.files[0]
}

interface VersionComparison {
  /** Size change from `base` to `other`, bytes. */
  sizeDelta: number
  fileDelta: number
  /** Vulnerability count change per severity; null when either side has no counts. */
  securityDelta: SeverityCounts | null
  /** Days between the two publications (positive when `other` is newer); null when a date is missing. */
  days: number | null
}

/** What changed from `base` to `other`: size, files, vulnerability counts and age. */
export function compareVersions(base: VersionEntry, other: VersionEntry): VersionComparison {
  const a = base.security
  const b = other.security
  const ta = time(base.createdAt)
  const tb = time(other.createdAt)
  return {
    sizeDelta: other.size - base.size,
    fileDelta: other.files.length - base.files.length,
    securityDelta:
      a && b
        ? {
            critical: b.critical - a.critical,
            high: b.high - a.high,
            medium: b.medium - a.medium,
            low: b.low - a.low,
          }
        : null,
    days: ta && tb ? Math.round((tb - ta) / 86_400_000) : null,
  }
}

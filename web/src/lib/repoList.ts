// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// What the repository list shows per row, derived from the stats the API returns.

import type { RepositoryListItem, RepositorySecurity, RepositorySort } from '../api/repositories'

export const REPO_SORTS: readonly RepositorySort[] = ['name', 'updated', 'last_published', 'size']

export type RepoSecurityState =
  | { kind: 'unknown' }
  /** Nothing stored, so nothing to scan. */
  | { kind: 'empty' }
  | { kind: 'unscanned' }
  | { kind: 'clean'; scannedAt?: string }
  | { kind: 'findings'; security: RepositorySecurity }

/** The security state of a repository row, from the counts the API returns. */
export function securityState(item: Pick<RepositoryListItem, 'stats'>): RepoSecurityState {
  const stats = item.stats
  if (!stats) return { kind: 'unknown' }
  const s = stats.security
  const total = s.critical + s.high + s.medium + s.low + s.secrets + s.license_violations
  if (total > 0) return { kind: 'findings', security: s }
  if (stats.package_count === 0) return { kind: 'empty' }
  return s.scanned_at ? { kind: 'clean', scannedAt: s.scanned_at } : { kind: 'unscanned' }
}

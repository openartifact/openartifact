// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Repository } from '../api/repositories'
import type { SecuritySummary } from '../api/artifact'
import { tp } from '../i18n/en'

export interface ArtifactAccess {
  isAdmin: boolean
  /** `Repository.permissions` of the single-repository read; absent for a reader of a public repository. */
  permissions?: Repository['permissions']
}

/**
 * What the caller may do on the artifact page. The server is the gate; this
 * only decides which controls and tabs to offer.
 */
export interface ArtifactCapabilities {
  /** Activity feed and the security inbox links: global admins and project maintainers. */
  maintain: boolean
  /**
   * Scan results, VEX, quarantine and SBOMs: the server answers these routes to global
   * admins only (a project maintainer gets 403), so the controls and requests are gated by it.
   */
  admin: boolean
  /** Properties edit, SBOM generate. */
  write: boolean
  /** Delete the version (to the trash when the repository keeps one). */
  remove: boolean
}

export function artifactCapabilities(access: ArtifactAccess): ArtifactCapabilities {
  const p = access.permissions
  return {
    maintain: access.isAdmin || p?.admin === true,
    admin: access.isAdmin,
    write: access.isAdmin || p?.write === true,
    remove: access.isAdmin || p?.delete === true,
  }
}

export type SecurityLevel = 'critical' | 'high' | 'medium' | 'low' | 'clean' | 'unscanned'

/** The worst thing a summary reports; `unscanned` when no scan completed and nothing is flagged. */
export function securityLevel(summary: SecuritySummary | null): SecurityLevel {
  if (!summary) return 'unscanned'
  const { critical, high, medium, low } = summary.vuln
  if (critical > 0) return 'critical'
  if (high > 0) return 'high'
  if (medium > 0) return 'medium'
  if (low > 0) return 'low'
  if (summary.scanned_at === null && summary.license.violations === 0 && summary.secret.count === 0) return 'unscanned'
  return 'clean'
}

/** Chip text of the Security tab ("3 critical"): only critical and high are worth a flag. */
export function securityFlag(summary: SecuritySummary | null): string | undefined {
  if (!summary) return undefined
  if (summary.vuln.critical > 0) return tp('artifact.security.flag.critical', { count: String(summary.vuln.critical) })
  if (summary.vuln.high > 0) return tp('artifact.security.flag.high', { count: String(summary.vuln.high) })
  return undefined
}

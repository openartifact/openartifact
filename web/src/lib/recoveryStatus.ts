// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RecoveryStatus } from '../api/recovery'

/**
 * How the disaster-recovery journal is doing:
 * - `off`: the journal switch is off, so no sidecar is written and a rebuild would miss what changed since.
 * - `failing`: rows are in retry backoff after a failed sidecar write.
 * - `catching-up`: changed rows still wait for their sidecar (the lag is the recovery point of a rebuild).
 * - `healthy`: every change has its sidecar.
 */
export type RecoveryHealth = 'off' | 'failing' | 'catching-up' | 'healthy'

export function recoveryHealth(status: RecoveryStatus): RecoveryHealth {
  if (!status.enabled) return 'off'
  if ((status.retrying_rows ?? 0) > 0) return 'failing'
  if ((status.backlog ?? 0) > 0) return 'catching-up'
  return 'healthy'
}

/** True when the backlog hit its cap: the real number of waiting rows is at least the reported one. */
export function backlogCapped(status: RecoveryStatus): boolean {
  const cap = status.backlog_cap ?? 0
  return cap > 0 && (status.backlog ?? 0) >= cap
}

/** The commands of an offline rebuild (docs/operations/rebuild-from-storage.md): a dry run, then the real run. */
export const REBUILD_DRY_RUN = '/app/openartifact rebuild --dry-run'
export const REBUILD_RUN = '/app/openartifact rebuild --report /tmp/rebuild-report.json'

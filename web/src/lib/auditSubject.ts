// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AuditEntry } from '../api/audit'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/**
 * What an audit entry acted on, in words a person can read. The server puts a
 * description in `details.message` (a string such as `moved to trash: repo/img
 * v1`, or an object holding the request `path`); `target` is the resource id,
 * which stays out of the summary when it is a bare UUID.
 */
export function auditSubject(entry: AuditEntry): string {
  const details = entry.details
  const message = details?.message
  if (typeof message === 'string' && message) return message
  if (message && typeof message === 'object') {
    const path = (message as Record<string, unknown>).path
    if (typeof path === 'string' && path) return path
  }
  if (typeof details?.path === 'string' && details.path) return details.path
  const target = entry.target ?? ''
  return UUID.test(target) ? '' : target
}

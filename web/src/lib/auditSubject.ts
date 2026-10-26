// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AuditEntry, AuditSubject } from '../api/audit'
import { artifactPath } from './artifactPath'
import { t, tDynamic } from '../i18n/en'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/** The per-request audit rows record the API route pattern (`/api/v1/repositories/{name}`), which says nothing about what was acted on. */
function isRouteTemplate(path: string): boolean {
  return path.startsWith('/api/')
}

/**
 * What an audit entry acted on, in words a person can read. The server puts a
 * description in `details.message` (a string such as `moved to trash: repo/img
 * v1`, or an object holding the request `path`, which is ignored when it is only a route pattern); `target` is the resource id,
 * which stays out of the summary when it is a bare UUID.
 */
export function auditSubject(entry: AuditEntry): string {
  const details = entry.details
  const message = details?.message
  if (typeof message === 'string' && message) return message
  if (message && typeof message === 'object') {
    const path = (message as Record<string, unknown>).path
    if (typeof path === 'string' && path && !isRouteTemplate(path)) return path
  }
  if (typeof details?.path === 'string' && details.path && !isRouteTemplate(details.path)) return details.path
  const target = entry.target ?? ''
  return UUID.test(target) ? '' : target
}

export interface SubjectView {
  /** Human name of the subject ("web-npm", "left-pad 1.3.0"). */
  label: string
  /** Kind in words ("Repository"). */
  kind: string
  /** In-app page of the subject; null when it has none (or is gone). */
  to: string | null
}

const enc = encodeURIComponent

/** Page that shows a subject of `kind`, or null when no page can show it. */
function subjectPath(subject: AuditSubject): string | null {
  const name = subject.name
  if (!name) return null
  switch (subject.kind) {
    case 'repository':
      return `/repos/${enc(name)}`
    case 'package':
      return subject.repository ? artifactPath(subject.repository, name, subject.version) : null
    case 'project':
      return `/projects/${enc(name)}`
    case 'user':
      return `/settings/users?q=${enc(name)}`
    case 'group':
      return `/settings/groups?q=${enc(name)}`
    case 'token':
      return `/settings/tokens?type=all&q=${enc(name)}`
    case 'permission_target':
      return '/settings/permissions'
    case 'gpg_key':
      return '/settings/signing'
    case 'sso_provider':
      return '/settings/sso'
    case 'storage_tier':
      return '/settings/tiers'
    default:
      return null
  }
}

function kindLabel(kind: string): string {
  const key = `sx.audit.kind.${kind}`
  const label = tDynamic(key)
  return label === key ? kind.replaceAll('_', ' ') : label
}

/** The resolved subject of an entry for display; null when the server resolved none. */
export function auditSubjectView(entry: AuditEntry): SubjectView | null {
  const subject = entry.subject
  if (!subject) return null
  const name = subject.name ? (subject.version ? `${subject.name} ${subject.version}` : subject.name) : t('sx.audit.subject.gone')
  return { label: name, kind: kindLabel(subject.kind), to: subjectPath(subject) }
}

/** True for the per-request rows the server records for every API call. */
export function isHttpNoise(entry: AuditEntry): boolean {
  return entry.action === HTTP_REQUEST_ACTION
}

export const HTTP_REQUEST_ACTION = 'http.request'

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AuditEntry } from '../api/audit'
import type { RecentPackage } from '../api/dashboard'
import type { ProjectActivityEntry } from '../api/projects'
import { artifactPath } from './artifactPath'

/** What an audit entry acted on, resolved by the server from ids (never a UUID). */
export interface AuditSubject {
  kind: string
  name: string | null
  repository?: string
  project?: string
}

/** An audit entry as the server returns it once subjects are resolved. */
export type ResolvedAuditEntry = AuditEntry & { actor_name?: string; subject?: AuditSubject | null }

export interface ActivityLink {
  label: string
  to?: string
}

/** One line of the Home activity feed: "<actor> <verb> <subject> in <where>". */
export interface ActivityItem {
  id: string
  at: string
  actor: string
  verb: string
  subject?: ActivityLink
  where?: ActivityLink
}

/** Audit actions that describe traffic, not a decision or a change; they never reach the feed. */
const NOISE = new Set(['http.request', 'artifact.downloaded', 'auth.token_validate'])

export function isFeedAction(action: string | undefined): boolean {
  return !!action && !NOISE.has(action)
}

/** `project.member_added` -> `member added`, `artifact.uploaded` -> `uploaded`. */
export function verbOf(action: string | undefined): string {
  const tail = (action ?? '').split('.').pop() ?? ''
  return tail.replaceAll('_', ' ')
}

const repoPath = (name: string): string => `/repos/${encodeURIComponent(name)}`

/** The page a subject opens, or undefined when it has no page of its own. */
export function subjectPath(subject: AuditSubject): string | undefined {
  const name = subject.name
  if (!name) return undefined
  switch (subject.kind) {
    case 'repository':
      return repoPath(name)
    case 'package':
      return subject.repository ? artifactPath(subject.repository, name) : undefined
    case 'project':
      return `/projects/${encodeURIComponent(name)}`
    case 'user':
      return '/settings/users'
    case 'group':
      return '/settings/groups'
    case 'token':
      return '/settings/tokens'
    default:
      return undefined
  }
}

export function auditItem(entry: ResolvedAuditEntry): ActivityItem {
  const subject = entry.subject?.name ? entry.subject : null
  const where = subject?.repository && subject.kind !== 'repository' ? subject.repository : subject?.project
  const whereKind = subject?.repository && subject.kind !== 'repository' ? 'repository' : 'project'
  return {
    id: entry.id ?? `${entry.timestamp}-${entry.action}`,
    at: entry.timestamp ?? '',
    actor: entry.actor_name || entry.actor || '',
    verb: verbOf(entry.action),
    subject: subject ? { label: subject.name ?? '', to: subjectPath(subject) } : undefined,
    where:
      where && where !== subject?.name
        ? { label: where, to: whereKind === 'repository' ? repoPath(where) : `/projects/${encodeURIComponent(where)}` }
        : undefined,
  }
}

/** Project feeds carry a readable message, not resolved ids; the project is the link. */
export function projectItem(entry: ProjectActivityEntry, project: string): ActivityItem {
  return {
    id: `${project}:${entry.id}`,
    at: entry.timestamp,
    actor: entry.actor,
    verb: verbOf(entry.action),
    subject: entry.message ? { label: entry.message } : undefined,
    where: { label: project, to: `/projects/${encodeURIComponent(project)}` },
  }
}

export function publishedItem(pkg: RecentPackage): ActivityItem {
  return {
    id: `pkg:${pkg.repository}:${pkg.name}:${pkg.version}`,
    at: pkg.created_at,
    actor: pkg.created_by_name ?? '',
    verb: 'published',
    subject: { label: `${pkg.name} ${pkg.version}`, to: artifactPath(pkg.repository, pkg.name, pkg.version) },
    where: { label: pkg.repository, to: repoPath(pkg.repository) },
  }
}

/** Newest first, de-duplicated by id, capped. Entries without a valid timestamp sort last. */
export function mergeActivity(lists: ActivityItem[][], limit: number): ActivityItem[] {
  const seen = new Set<string>()
  const all: ActivityItem[] = []
  for (const item of lists.flat()) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    all.push(item)
  }
  const time = (i: ActivityItem): number => {
    const n = Date.parse(i.at)
    return Number.isNaN(n) ? 0 : n
  }
  return all.sort((a, b) => time(b) - time(a)).slice(0, limit)
}

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AuditEntry, PackageFile, SecurityFinding, UnifiedToken, User } from '../api/generated/types.gen'
import { GROUPS, PROJECTS, TOKENS, USERS, type DemoGroup, type DemoProject } from './fixtures/people'
import { PACKAGES, type DemoPackage } from './fixtures/packages'
import { REPOSITORIES, type DemoRepository } from './fixtures/repos'
import { FINDINGS, type DemoFinding } from './fixtures/security'
import { ago, digest, uid } from './fixtures/time'

/** One stored file of a package version. */
export interface DemoFile extends PackageFile {
  repository: string
  /** Username of the publisher. */
  created_by: string
}

/** A finding as the API lists it, with what its scan detail shows. */
export type DemoStoredFinding = SecurityFinding & Pick<DemoFinding, 'advisories' | 'licenses' | 'secrets'>

/** Everything the demo backend serves; handlers read and change it in place. */
export interface DemoState {
  signedIn: boolean
  users: User[]
  groups: DemoGroup[]
  projects: DemoProject[]
  tokens: UnifiedToken[]
  repositories: DemoRepository[]
  packages: DemoPackage[]
  files: DemoFile[]
  findings: DemoStoredFinding[]
  audit: AuditEntry[]
}

const ZERO = { critical: 0, high: 0, medium: 0, low: 0 }

function seedFiles(): DemoFile[] {
  const files: DemoFile[] = []
  for (const pkg of PACKAGES) {
    const format = REPOSITORIES.find((r) => r.name === pkg.repository)?.format ?? 'raw'
    for (const v of pkg.versions) {
      const vuln = FINDINGS.find(
        (f) => f.type === 'vuln' && f.repository === pkg.repository && f.package_name === pkg.name && f.package_version === v.version,
      )
      const flagged = FINDINGS.some(
        (f) => f.repository === pkg.repository && f.package_name === pkg.name && f.package_version === v.version && (f.severity === 'critical' || f.severity === 'high'),
      )
      for (const file of v.files) {
        files.push({
          id: uid('file', files.length + 1),
          repository: pkg.repository,
          name: pkg.name,
          version: v.version,
          format,
          arch: file.arch ?? '',
          size: Math.round(file.size),
          sha256: digest(`${pkg.repository}/${pkg.name}/${v.version}/${file.filename}`),
          filename: file.filename,
          created_at: ago(v.hoursAgo),
          created_by: v.by,
          security: vuln?.counts ?? ZERO,
          flagged,
          scanned_at: ago(v.hoursAgo - 0.05),
        })
      }
    }
  }
  return files
}

function seedFindings(files: DemoFile[]): DemoStoredFinding[] {
  return FINDINGS.map((f, i) => {
    const file = files.find((x) => x.repository === f.repository && x.name === f.package_name && x.version === f.package_version)
    return {
      ...f,
      id: uid('finding', i + 1),
      artifact_id: file?.id ?? '',
      format: file?.format ?? 'raw',
      scanned_at: file?.scanned_at ?? ago(1),
      quarantined: f.status === 'quarantined',
    }
  })
}

function userId(users: User[], username: string): string {
  return users.find((u) => u.username === username)?.id ?? ''
}

/** Publishes of every version (one entry per version) plus a few administrative events, newest first. */
function seedAudit(files: DemoFile[], users: User[]): AuditEntry[] {
  const entries: Omit<AuditEntry, 'id'>[] = []
  const seen = new Set<string>()
  for (const f of files) {
    const key = `${f.repository}/${f.name}/${f.version}`
    if (seen.has(key) || f.created_by === 'system') continue
    seen.add(key)
    entries.push({
      timestamp: f.created_at,
      action: 'artifact.uploaded',
      actor: userId(users, f.created_by),
      actor_name: f.created_by,
      subject: { kind: 'artifact', name: f.name, version: f.version || undefined, repository: f.repository },
      repository_id: REPOSITORIES.find((r) => r.name === f.repository)?.id ?? null,
      resource_type: 'artifact',
      resource_id: f.id,
    })
  }
  const admin = (hours: number, action: string, subject: AuditEntry['subject'], actorName = 'admin'): void => {
    entries.push({ timestamp: ago(hours), action, actor: userId(users, actorName), actor_name: actorName, subject, ip: '10.20.0.14' })
  }
  admin(0.1, 'auth.login', null)
  admin(2, 'artifact.quarantined', { kind: 'artifact', name: 'payments/api', version: '2.3.0', repository: 'containers' }, 'erik.nilsson')
  admin(9, 'artifact.quarantined', { kind: 'artifact', name: 'acme-features', version: '0.9.1', repository: 'pypi-ml' }, 'erik.nilsson')
  admin(24 * 4, 'repository.updated', { kind: 'repository', name: 'npmjs', repository: 'npmjs' })
  admin(24 * 7, 'token.created', { kind: 'token', name: 'github-actions', project: 'payments' }, 'bob.martin')
  admin(24 * 30, 'user.created', { kind: 'user', name: 'kenji.sato' })
  admin(24 * 31, 'group.member_added', { kind: 'group', name: 'payments' })
  admin(24 * 120, 'repository.created', { kind: 'repository', name: 'charts', repository: 'charts' })
  return entries
    .sort((a, b) => b.timestamp.localeCompare(a.timestamp))
    .map((e, i) => ({ ...e, id: uid('audit', i + 1) }))
}

/** A fresh copy of the seeded data: every page load starts from it. */
export function seedState(): DemoState {
  const users = structuredClone(USERS)
  const files = seedFiles()
  return {
    signedIn: true,
    users,
    groups: structuredClone(GROUPS),
    projects: structuredClone(PROJECTS),
    tokens: structuredClone(TOKENS),
    repositories: structuredClone(REPOSITORIES),
    packages: structuredClone(PACKAGES),
    files,
    findings: seedFindings(files),
    audit: seedAudit(files, users),
  }
}

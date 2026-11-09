// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type {
  GetApiV1RepositoriesByNameActivityResponses,
  GetApiV1RepositoriesByNameArtifactsByArtifactIdPropertiesResponses,
  GetApiV1RepositoriesByNameAvailabilityResponses,
  GetApiV1RepositoriesByNameCleanupPoliciesResponses,
  GetApiV1RepositoriesByNameCurationRulesResponses,
  GetApiV1RepositoriesByNameProxyVersionsResponses,
  GetApiV1RepositoriesByNameMembersResponses,
  GetApiV1RepositoriesByNameSecuritySummaryResponses,
  GetApiV1RepositoriesByNameUsageResponses,
  GetApiV1RepositoriesResponses,
  GetApiV1RepositoryByNameResponses,
  Repository,
} from '../../api/generated/types.gen'
import type { RepoQuota } from '../../api/quota'
import type { DemoRepository } from '../fixtures/repos'
import { ago, uid } from '../fixtures/time'
import { failure, type DemoRequest, type DemoRoute, type Ok } from '../http'
import { findRepo, matches, paginate, repoFiles, repoStats, repoView } from '../query'
import type { DemoState } from '../state'

const QUOTA_WARN_PERCENT = 80

/** The named repository, or the server's 404. */
export function withRepo(handler: (req: DemoRequest, repo: DemoRepository) => unknown): (req: DemoRequest) => unknown {
  return (req) => {
    const repo = findRepo(req.state, req.params.name ?? '')
    return repo ? handler(req, repo) : failure(404, 'repository not found', 'not_found')
  }
}

function sortRepos(state: DemoState, repos: DemoRepository[], sort: string | null): DemoRepository[] {
  const stats = new Map(repos.map((r) => [r.name, repoStats(state, r)]))
  const by: Record<string, (a: DemoRepository, b: DemoRepository) => number> = {
    updated: (a, b) => (b.updated_at ?? '').localeCompare(a.updated_at ?? ''),
    last_published: (a, b) => (stats.get(b.name)?.last_published_at ?? '').localeCompare(stats.get(a.name)?.last_published_at ?? ''),
    size: (a, b) => (stats.get(b.name)?.size_bytes ?? 0) - (stats.get(a.name)?.size_bytes ?? 0),
  }
  return [...repos].sort(by[sort ?? ''] ?? ((a, b) => a.name.localeCompare(b.name)))
}

function listRepositories({ state, query }: DemoRequest): Ok<GetApiV1RepositoriesResponses> {
  const flagged = (r: DemoRepository) => {
    const s = repoStats(state, r).security
    return s.critical + s.high + s.secrets > 0 || state.findings.some((f) => f.repository === r.name && f.quarantined)
  }
  const nearQuota = (r: DemoRepository) => {
    const q = repoStats(state, r).quota
    return !!q.limit_bytes && (q.used_bytes * 100) / q.limit_bytes >= QUOTA_WARN_PERCENT
  }
  const packageName = query.get('package_name')
  const matched = state.repositories.filter(
    (r) =>
      matches(r.name, query.get('q')) &&
      (!query.get('format') || r.format === query.get('format')) &&
      (!query.get('type') || r.repo_type === query.get('type')) &&
      (!query.get('project') || r.project === query.get('project')) &&
      (!packageName || repoFiles(state, r).some((f) => f.name === packageName)) &&
      (query.get('flagged') !== 'true' || flagged(r)) &&
      (query.get('near_quota') !== 'true' || nearQuota(r)) &&
      (query.get('unscanned') !== 'true' || repoFiles(state, r).some((f) => !f.scanned_at)),
  )
  const sorted = sortRepos(state, matched, query.get('sort'))
  return { total: sorted.length, repositories: paginate(sorted, query).map((r) => repoView(state, r)) }
}

function createRepository({ state, body }: DemoRequest): Ok<GetApiV1RepositoryByNameResponses> | Response {
  const input = (body ?? {}) as Partial<Repository>
  if (!input.name || !input.format) return failure(400, 'name and format are required', 'validation_failed')
  if (findRepo(state, input.name)) return failure(409, 'repository already exists', 'conflict')
  const now = new Date().toISOString()
  const repo: DemoRepository = {
    repo_type: 'hosted',
    project: 'system',
    public: false,
    ...input,
    name: input.name,
    format: input.format,
    id: uid('repo', state.repositories.length + 100),
    created_at: now,
    updated_at: now,
  }
  state.repositories.push(repo)
  return repoView(state, repo)
}

function updateRepository(req: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoryByNameResponses> {
  const { name: _name, id: _id, format: _format, ...changes } = (req.body ?? {}) as Partial<Repository>
  Object.assign(repo, changes, { updated_at: new Date().toISOString() })
  return repoView(req.state, repo)
}

function deleteRepository({ state }: DemoRequest, repo: DemoRepository): void {
  state.repositories = state.repositories.filter((r) => r !== repo)
  state.files = state.files.filter((f) => f.repository !== repo.name)
  for (const g of state.repositories) g.member_repos = g.member_repos?.filter((m) => m !== repo.name)
}

function members({ state }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameMembersResponses> {
  return {
    members: (repo.member_repos ?? []).flatMap((name, i) => {
      const m = findRepo(state, name)
      return m && m.repo_type !== 'group' ? [{ id: m.id, name: m.name, format: m.format, repo_type: m.repo_type ?? 'hosted', priority: i + 1 }] : []
    }),
  }
}

function usage({ state }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameUsageResponses> {
  const stats = repoStats(state, repo)
  const files = repoFiles(state, repo)
  const limit = repo.quota_limit_bytes
  const percent = limit ? Math.round((stats.size_bytes * 100) / limit) : 0
  return {
    package_count: stats.package_count,
    version_count: new Set(files.map((f) => `${f.name}@${f.version}`)).size,
    size_bytes: stats.size_bytes,
    last_published_at: stats.last_published_at,
    quota: {
      limit_bytes: limit,
      used_bytes: stats.size_bytes,
      warn_threshold_percent: QUOTA_WARN_PERCENT,
      artifact_count: files.length,
      usage_percent: percent,
      exceeded: percent >= 100,
    },
  }
}

function quota({ state }: DemoRequest, repo: DemoRepository): RepoQuota {
  const files = repoFiles(state, repo)
  const size = files.reduce((n, f) => n + f.size, 0)
  return {
    repo_id: repo.id,
    max_size_bytes: repo.quota_limit_bytes ?? 0,
    current_size_bytes: size,
    current_artifact_count: files.length,
    is_exceeded: !!repo.quota_limit_bytes && size > repo.quota_limit_bytes,
  }
}

/** Counts of the repository, or of one package version with `package_name` and `version`. */
function securitySummary({ state, query }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameSecuritySummaryResponses> {
  const pkg = query.get('package_name')
  const version = query.get('version')
  const findings = state.findings.filter(
    (f) => (f.repository === repo.name || repo.member_repos?.includes(f.repository)) && (pkg === null || (f.package_name === pkg && f.package_version === (version ?? ''))),
  )
  const vuln = { critical: 0, high: 0, medium: 0, low: 0 }
  for (const f of findings.filter((x) => x.type === 'vuln')) {
    vuln.critical += f.counts.critical
    vuln.high += f.counts.high
    vuln.medium += f.counts.medium
    vuln.low += f.counts.low
  }
  const files = repoFiles(state, repo).filter((f) => pkg === null || (f.name === pkg && f.version === version))
  return {
    scanned_at: files.reduce<string | null>((at, f) => (f.scanned_at && (!at || f.scanned_at > at) ? f.scanned_at : at), null),
    vuln,
    license: { violations: findings.filter((f) => f.type === 'license').length },
    secret: { count: findings.filter((f) => f.type === 'secret').length },
    ai: { quarantine_recommended: 0, review_required: 0 },
    quarantined: findings.some((f) => f.quarantined),
  }
}

/** Audit entries of the repository (optionally one package version), newest first. */
function activity({ state, query }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameActivityResponses> {
  const pkg = query.get('package_name')
  const version = query.get('version')
  const limit = Number(query.get('limit')) || 50
  const rows = state.audit.filter(
    (e) =>
      e.subject?.repository === repo.name &&
      (pkg === null || e.subject.name === pkg) &&
      (version === null || (e.subject.version ?? '') === version) &&
      (!query.get('action') || e.action === query.get('action')),
  )
  return {
    data: rows.slice(0, limit).map((e) => ({
      id: e.id,
      timestamp: e.timestamp,
      action: e.action,
      actor: e.actor,
      actor_name: e.actor_name,
      actor_type: 'user',
      message: '',
      subject: e.subject,
      resource_type: e.resource_type,
      resource_id: e.resource_id,
      repository_id: repo.id,
    })),
    per_page: limit,
    has_more: false,
  }
}

/** A version is held back only by a quarantine in the demo (no curation rules, no minimum age). */
function availability({ state, query }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameAvailabilityResponses> {
  const pkg = query.get('package')
  const version = query.get('version')
  const held = state.findings.filter((f) => f.repository === repo.name && f.package_name === pkg && f.package_version === version && f.quarantined)
  return {
    available: held.length === 0,
    reasons: held.map((f) => ({ source: 'quarantine', detail: f.title })),
    published_at: repoFiles(state, repo).find((f) => f.name === pkg && f.version === version)?.created_at,
    min_age: 'off',
  }
}

/** Build provenance properties CI set on the file. */
function properties({ state, params }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameArtifactsByArtifactIdPropertiesResponses> | Response {
  const file = repoFiles(state, repo).find((f) => f.id === params.id)
  if (!file) return failure(404, 'artifact not found', 'not_found')
  if (file.created_by === 'system') return {}
  return { 'build.number': String(1000 + Number.parseInt(file.sha256.slice(0, 4), 16) % 9000), 'vcs.revision': file.sha256.slice(10, 22), 'ci.pipeline': 'release' }
}

/** The versions of a proxied package in the cache, newest first; quarantined ones are held. */
function proxyVersions({ state, query }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameProxyVersionsResponses> {
  const pkg = query.get('package')
  const seen = new Set<string>()
  const files = repoFiles(state, repo)
    .filter((f) => f.name === pkg && !seen.has(f.version) && !!seen.add(f.version))
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
  return {
    supported: repo.repo_type === 'proxy',
    cache_known: true,
    versions: files.map((f) => {
      const held = state.findings.filter((x) => x.artifact_id === f.id && x.quarantined)
      return {
        version: f.version,
        status: held.length ? 'held' : 'available',
        published_at: f.created_at,
        cached: true,
        reasons: held.map((x) => ({ source: 'quarantine', detail: x.title })),
      }
    }),
  }
}

/** Known-bad npm releases, the usual start of a denylist. */
function curationRules(_req: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNameCurationRulesResponses> {
  if (!repo.curation_mode || repo.curation_mode === 'off') return { rules: [] }
  const rule = (n: number, package_pattern: string, version_pattern: string, reason: string) => ({
    id: uid('repo', 900 + n),
    action: 'block' as const,
    package_pattern,
    version_pattern,
    reason,
    created_by: 'carla.rossi',
    created_at: ago(24 * (40 - n * 7)),
  })
  return {
    rules: [
      rule(1, 'event-stream', '3.3.6', 'Malicious release (flatmap-stream)'),
      rule(2, 'ua-parser-js', '0.7.29 || 0.8.0 || 1.0.0', 'Hijacked releases with a cryptominer'),
      rule(3, 'colors', '>=1.4.1', 'Sabotaged by its maintainer'),
      rule(4, 'node-ipc', '>=10.1.1 <10.1.3', 'Protestware that overwrites files'),
    ],
  }
}

const cleanupPolicies = (): Ok<GetApiV1RepositoriesByNameCleanupPoliciesResponses> => ({ policies: [] })

export const repoRoutes: DemoRoute[] = [
  ['GET', '/api/v1/repositories', listRepositories],
  ['POST', '/api/v1/repositories', createRepository],
  ['GET', '/api/v1/repository/:name', withRepo((req, repo) => repoView(req.state, repo))],
  ['PATCH', '/api/v1/repository/:name', withRepo(updateRepository)],
  ['DELETE', '/api/v1/repository/:name', withRepo(deleteRepository)],
  ['GET', '/api/v1/repositories/:name/members', withRepo(members)],
  ['GET', '/api/v1/repositories/:name/usage', withRepo(usage)],
  ['GET', '/api/v1/repositories/:name/quota', withRepo(quota)],
  ['GET', '/api/v1/repositories/:name/security/summary', withRepo(securitySummary)],
  ['GET', '/api/v1/repositories/:name/activity', withRepo(activity)],
  ['GET', '/api/v1/repositories/:name/availability', withRepo(availability)],
  ['GET', '/api/v1/repositories/:name/artifacts/:id/properties', withRepo(properties)],
  ['GET', '/api/v1/repositories/:name/cleanup-policies', withRepo(cleanupPolicies)],
  ['GET', '/api/v1/repositories/:name/proxy-versions', withRepo(proxyVersions)],
  ['GET', '/api/v1/repositories/:name/curation-rules', withRepo(curationRules)],
]

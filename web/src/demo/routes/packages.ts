// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type {
  ChecksumHit,
  GetApiV1PackagesRecentResponses,
  GetApiV1RepositoriesByNamePackagesByIdResponses,
  GetApiV1RepositoriesByNamePackagesResponses,
  GetApiV1SearchResponses,
  SearchPackageHit,
} from '../../api/generated/types.gen'
import type { DemoRepository } from '../fixtures/repos'
import { failure, type DemoRequest, type DemoRoute, type Ok } from '../http'
import { matches, packageGroups, paginate, repoFiles } from '../query'
import type { DemoFile } from '../state'
import { withRepo } from './repos'

const byNewest = (a: DemoFile, b: DemoFile): number => b.created_at.localeCompare(a.created_at)

/** The per-file listing, or one row per package with `group=package`. */
function listPackages({ state, query }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNamePackagesResponses> {
  const name = query.get('package_name')
  let files = repoFiles(state, repo).filter(
    (f) =>
      (!name || f.name === name) &&
      (!query.get('format') || f.format === query.get('format')) &&
      (matches(f.name, query.get('q')) || matches(f.version, query.get('q'))) &&
      (query.get('flagged') !== 'true' || f.flagged),
  )
  const byName = query.get('sort') === 'name'
  if (query.get('group') === 'package') {
    const groups = packageGroups(files).sort(byName ? (a, b) => a.name.localeCompare(b.name) : (a, b) => b.updated_at.localeCompare(a.updated_at))
    return { total: groups.length, total_exact: true, packages: paginate(groups, query) }
  }
  files = [...files].sort(byName ? (a, b) => a.name.localeCompare(b.name) || b.created_at.localeCompare(a.created_at) : byNewest)
  const page = paginate(files, query)
  const limit = Math.max(1, Number(query.get('limit')) || 20)
  const pageNo = Math.max(1, Number(query.get('page')) || 1)
  return { total: files.length, total_exact: true, has_more: pageNo * limit < files.length, packages: page.map(({ repository: _r, created_by: _b, ...row }) => row) }
}

/** One version of a package, looked up by the listing row id of any of its files. */
function packageDetail({ state, params }: DemoRequest, repo: DemoRepository): Ok<GetApiV1RepositoriesByNamePackagesByIdResponses> | Response {
  const files = repoFiles(state, repo)
  const row = files.find((f) => f.id === params.id)
  if (!row) return failure(404, 'package not found', 'not_found')
  const versionFiles = files.filter((f) => f.repository === row.repository && f.name === row.name && f.version === row.version)
  const all = files.filter((f) => f.repository === row.repository && f.name === row.name).sort(byNewest)
  const pkg = state.packages.find((p) => p.repository === row.repository && p.name === row.name)
  return {
    id: row.id,
    name: row.name,
    version: row.version,
    format: row.format,
    arch: row.arch,
    filename: row.filename,
    size: row.size,
    sha256: row.sha256,
    created_at: row.created_at,
    updated_at: row.created_at,
    summary: pkg?.summary,
    description: pkg?.description,
    license: pkg?.license,
    homepage: pkg?.homepage,
    metadata: pkg?.metadata ?? [],
    dependencies: pkg?.dependencies ?? [],
    files: versionFiles.map((f) => ({ id: f.id, filename: f.filename, arch: f.arch, size: f.size, sha256: f.sha256 })),
    version_count: new Set(all.map((f) => f.version)).size,
    latest_version: all[0]?.version ?? row.version,
  }
}

function deletePackage({ state, params }: DemoRequest, repo: DemoRepository): Response | void {
  const row = state.files.find((f) => f.id === params.id && f.repository === repo.name)
  if (!row) return failure(404, 'package not found', 'not_found')
  state.files = state.files.filter((f) => f !== row)
}

function recent({ state, query }: DemoRequest): Ok<GetApiV1PackagesRecentResponses> {
  const seen = new Set<string>()
  const latest = [...state.files].sort(byNewest).filter((f) => {
    const key = `${f.repository}/${f.name}/${f.version}`
    if (seen.has(key) || f.created_by === 'system' || (query.get('mine') === 'true' && f.created_by !== 'admin')) return false
    seen.add(key)
    return true
  })
  return {
    packages: latest.slice(0, Number(query.get('limit')) || 8).map((f) => ({
      repository: f.repository,
      format: f.format,
      name: f.name,
      version: f.version,
      created_at: f.created_at,
      created_by_name: f.created_by,
    })),
  }
}

/** Package (per version, or `group=package` per package) and repository search, or a checksum lookup. */
function search({ state, query }: DemoRequest): Ok<GetApiV1SearchResponses> {
  const q = query.get('q')
  const repos = state.repositories.filter((r) => r.repo_type !== 'group' && (!query.get('repo') || r.name === query.get('repo')))
  const files = repos
    .flatMap((r) => repoFiles(state, r))
    .filter((f) => (!query.get('format') || f.format === query.get('format')) && (query.get('flagged') !== 'true' || f.flagged))
  const limit = Number(query.get('limit')) || 25
  const offset = Number(query.get('offset')) || 0

  const checksum = query.get('checksum')?.toLowerCase()
  if (checksum) {
    const hits: ChecksumHit[] = files
      .filter((f) => f.sha256 === checksum)
      .map((f) => ({ repository: f.repository, format: f.format, name: f.name, version: f.version, filename: f.filename, checksum: f.sha256, algorithm: 'sha256' }))
    return { total: hits.length, packages: hits }
  }

  const hitOf = (f: DemoFile, versionCount?: number): SearchPackageHit => ({
    repository: f.repository,
    name: f.name,
    version: f.version,
    format: f.format,
    path: f.filename,
    sha256: f.sha256,
    size: f.size,
    created_at: f.created_at,
    version_count: versionCount,
  })
  const matched = files.filter((f) => matches(f.name, q)).sort(byNewest)
  let hits: SearchPackageHit[]
  if (query.get('group') === 'package') {
    const latest = new Map<string, DemoFile[]>()
    for (const f of matched) latest.set(`${f.repository}/${f.name}`, [...(latest.get(`${f.repository}/${f.name}`) ?? []), f])
    hits = [...latest.values()].map((rows) => hitOf(rows[0] as DemoFile, new Set(rows.map((r) => r.version)).size))
  } else {
    const seen = new Set<string>()
    hits = matched.filter((f) => !seen.has(`${f.repository}/${f.name}/${f.version}`) && seen.add(`${f.repository}/${f.name}/${f.version}`)).map((f) => hitOf(f))
  }
  return {
    total: hits.length,
    packages: hits.slice(offset, offset + limit),
    repositories: state.repositories.filter((r) => matches(r.name, q)).map((r) => ({ name: r.name, format: r.format, description: r.description ?? '' })),
  }
}

export const packageRoutes: DemoRoute[] = [
  ['GET', '/api/v1/repositories/:name/packages', withRepo(listPackages)],
  ['GET', '/api/v1/repositories/:name/packages/:id', withRepo(packageDetail)],
  ['DELETE', '/api/v1/repositories/:name/packages/:id', withRepo(deletePackage)],
  ['GET', '/api/v1/packages/recent', recent],
  ['GET', '/api/v1/search', search],
]

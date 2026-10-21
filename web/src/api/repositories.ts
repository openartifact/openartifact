// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, requestBlob } from './client'
import type { Repository, RepositoryMember } from './generated/types.gen'

export type { Repository, RepositoryMember }

export interface PackageItem {
  id: string
  name: string
  version: string
  size?: number
  sha256?: string
  format: string
  created_at?: string
}

interface ListRepositoriesResult {
  total: number
  repositories: Repository[]
}

interface ListPackagesResult {
  total: number
  packages: PackageItem[]
}

export function listRepositories(params?: { page?: number; limit?: number; format?: string }): Promise<ListRepositoriesResult> {
  const qs = new URLSearchParams()
  if (params?.page != null) qs.set('page', String(params.page))
  if (params?.limit != null) qs.set('limit', String(params.limit))
  if (params?.format) qs.set('format', params.format)
  const query = qs.toString() ? `?${qs}` : ''
  return request<ListRepositoriesResult>(`/api/v1/repositories${query}`)
}

/** Walks the paginated list endpoint (page size is clamped server-side). */
export async function listAllRepositories(): Promise<Repository[]> {
  const all: Repository[] = []
  for (let page = 1; page <= 50; page++) {
    const res = await listRepositories({ page, limit: 100 })
    const batch = res.repositories || []
    all.push(...batch)
    if (batch.length === 0 || all.length >= res.total) break
  }
  return all
}

export function getRepository(name: string): Promise<Repository> {
  return request<Repository>(`/api/v1/repository/${encodeURIComponent(name)}`)
}

/** Members of a group repository, ordered by resolution priority (first wins). */
export async function listRepoMembers(name: string): Promise<RepositoryMember[]> {
  const res = await request<{ members?: RepositoryMember[] }>(`/api/v1/repositories/${encodeURIComponent(name)}/members`)
  return res.members ?? []
}

export function createRepository(body: Partial<Repository> & { upstream_password?: string }): Promise<Repository> {
  return request<Repository>('/api/v1/repositories', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export type PresignMode = 'inherit' | 'always' | 'never'

/** Maps the tri-state presign_downloads field (null/absent = inherit) to a form value. */
export function presignModeOf(repo: Pick<Repository, 'presign_downloads'>): PresignMode {
  if (repo.presign_downloads === true) return 'always'
  if (repo.presign_downloads === false) return 'never'
  return 'inherit'
}

/** The presign_downloads value to PATCH for a form value (null clears the override). */
export function presignValueOf(mode: PresignMode): boolean | null {
  if (mode === 'always') return true
  if (mode === 'never') return false
  return null
}

export function updateRepository(name: string, body: Partial<Repository>): Promise<Repository> {
  return request<Repository>(`/api/v1/repository/${encodeURIComponent(name)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export function deleteRepository(name: string): Promise<void> {
  return request<void>(`/api/v1/repository/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
}

export function listPackages(name: string, params?: { page?: number; limit?: number; format?: string }): Promise<ListPackagesResult> {
  const qs = new URLSearchParams()
  if (params?.page != null) qs.set('page', String(params.page))
  if (params?.limit != null) qs.set('limit', String(params.limit))
  if (params?.format) qs.set('format', params.format)
  const query = qs.toString() ? `?${qs}` : ''
  return request<ListPackagesResult>(`/api/v1/repositories/${encodeURIComponent(name)}/packages${query}`)
}

/**
 * Downloads a listed package (any format) through the authenticated API and
 * hands it to the browser as a file. Rejects with ApiError on 4xx/5xx.
 */
export async function downloadPackage(repoName: string, pkg: Pick<PackageItem, 'id' | 'name' | 'version'>): Promise<void> {
  const { blob, filename } = await requestBlob(
    `/api/v1/repositories/${encodeURIComponent(repoName)}/packages/${encodeURIComponent(pkg.id)}/download`,
  )

  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename ?? (pkg.version ? `${pkg.name}-${pkg.version}` : pkg.name)
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

export interface ComponentItem {
  id: string
  name: string
  version: string
  format: string
  size?: number
  sha256?: string
  created_at?: string
  path?: string
}

// ── SBOM ──────────────────────────────────────────────────────────────────────

export interface SbomListItem {
  id: string
  artifact_name?: string
  artifact_version?: string
  created_at?: string
  format?: string
}

export interface SbomComponent {
  'bom-ref'?: string
  type?: string
  name: string
  version?: string
  purl?: string
  licenses?: Array<{ license?: { id?: string; name?: string } }>
  hashes?: Array<{ alg?: string; content?: string }>
}

export interface SbomData {
  id?: string
  bomFormat?: string
  specVersion?: string
  metadata?: {
    component?: {
      'bom-ref'?: string
      name: string
      version?: string
      type?: string
    }
  }
  components?: SbomComponent[]
  dependencies?: Array<{ ref: string; dependsOn?: string[] }>
}

export function listSboms(repoName: string): Promise<SbomListItem[]> {
  return request<SbomListItem[]>(`/api/v1/repositories/${encodeURIComponent(repoName)}/sbom`)
}

export function getSbom(repoName: string, id: string): Promise<SbomData> {
  return request<SbomData>(`/api/v1/repositories/${encodeURIComponent(repoName)}/sbom/${encodeURIComponent(id)}`)
}

// Deletes a package by its component id (the backend route is
// DELETE /repositories/{name}/packages/{id}).
export function deletePackage(repoName: string, id: string): Promise<void> {
  return request<void>(
    `/api/v1/repositories/${encodeURIComponent(repoName)}/packages/${encodeURIComponent(id)}`,
    { method: 'DELETE' },
  )
}

// ── Components ────────────────────────────────────────────────────────────────

const COMPONENT_PAGE_SIZE = 100 // backend clamps `limit` to 100
const COMPONENT_MAX_PAGES = 20

// Collects every package of a repository by walking the paginated
// /packages endpoint (there is no separate components route).
export async function listComponents(name: string): Promise<{ total: number; items: ComponentItem[] }> {
  const items: ComponentItem[] = []
  let total = 0
  for (let page = 1; page <= COMPONENT_MAX_PAGES; page++) {
    const res = await listPackages(name, { page, limit: COMPONENT_PAGE_SIZE })
    total = res.total
    items.push(...res.packages)
    if (items.length >= total || res.packages.length === 0) break
  }
  return { total, items }
}

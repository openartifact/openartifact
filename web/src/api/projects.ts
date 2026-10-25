// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { downloadAttachment, request, JSON_HEADERS } from './client'
import type { ExportFormat } from './audit'
import type { Repository } from './repositories'
import type { ProjectCleanupPreview } from './generated'
import type { ApiCleanupPolicy } from '../lib/cleanupPolicy'
import type {
  Project as GeneratedProject,
  ProjectActivityEntry,
  ProjectActivityPage,
  ProjectQuota as GeneratedProjectQuota,
  ProjectUsage as GeneratedProjectUsage,
  Quota as QuotaLimits,
  ProjectCreateRequest,
  ProjectGroup,
  ProjectGroupUpdateRequest,
  ProjectMember as GeneratedProjectMember,
  ProjectToken as GeneratedProjectToken,
  ProjectTokenCreateRequest as GeneratedProjectTokenCreateRequest,
  ProjectTokenCreateResponse as GeneratedProjectTokenCreateResponse,
  ProjectUpdateRequest,
} from './generated/types.gen'

export type { ProjectActivityEntry, ProjectCreateRequest, ProjectGroup, ProjectGroupUpdateRequest, ProjectUpdateRequest }

/** Name of the built-in system project: the default, whose membership grants nothing. */
export const SYSTEM_PROJECT_NAME = 'system'

/** Roles a user can hold in a project, highest first. */
export const PROJECT_MEMBER_ROLES = ['owner', 'maintainer', 'developer', 'viewer'] as const
export type ProjectMemberRole = (typeof PROJECT_MEMBER_ROLES)[number]

/** Roles a group can be attached with (groups never own a project). */
export const PROJECT_GROUP_ROLES = ['maintainer', 'developer', 'viewer'] as const
export type ProjectGroupRole = (typeof PROJECT_GROUP_ROLES)[number]

/** Repository types a project may allow. */
export const PROJECT_REPO_TYPES = ['hosted', 'proxy', 'group'] as const
export type ProjectRepoType = (typeof PROJECT_REPO_TYPES)[number]

/** A project as the API returns it: the stored record plus the caller's view of it. */
export type Project = GeneratedProject & {
  /** The caller's effective role; global admins who hold no role are reported as owner. */
  my_role?: ProjectMemberRole
  /** Live repositories owned by the project. */
  repository_count?: number
  /** The built-in system project. */
  is_system?: boolean
}

export type ProjectMember = GeneratedProjectMember & { username?: string }

/** One `/auth/me` entry: a project the caller holds a role in. */
export interface ProjectMembership {
  name: string
  role: ProjectMemberRole
}

interface ProjectList {
  projects: Project[]
  /** The caller may create projects (global admin, or self-service creation is enabled). */
  can_create: boolean
}

interface ProjectRepositoryPage {
  repositories: Repository[]
  total: number
  page: number
  limit: number
}

/** Add by `user_id` or `username`; the role defaults to viewer server-side. */
interface ProjectMemberCreateRequest {
  user_id?: string
  username?: string
  role?: ProjectMemberRole
}

interface ProjectMemberUpdateRequest {
  role: ProjectMemberRole
}

interface ProjectSettings {
  /** Any user may create projects, not only global admins. */
  self_create: boolean
}

const enc = encodeURIComponent
const base = (project: string): string => `/api/v1/projects/${enc(project)}`

function send<T>(path: string, method: string, body: unknown): Promise<T> {
  return request<T>(path, { method, headers: JSON_HEADERS, body: JSON.stringify(body) })
}

/** Projects the caller belongs to; `all` lists every project (global admins only, others get 403). */
export async function listProjects(opts: { all?: boolean } = {}): Promise<ProjectList> {
  const res = await request<{ projects?: Project[]; can_create?: boolean }>(
    `/api/v1/projects${opts.all ? '?all=true' : ''}`,
  )
  return { projects: res?.projects ?? [], can_create: res?.can_create === true }
}

export const getProject = (project: string) => request<Project>(base(project))

export const createProject = (body: ProjectCreateRequest) => send<Project>('/api/v1/projects', 'POST', body)

/** Partial update. Policy fields are accepted from global admins only (403 for owners). */
export const updateProject = (project: string, body: ProjectUpdateRequest) =>
  send<Project>(base(project), 'PATCH', body)

/** Owners only; 409 while the project owns repositories and for the system project. */
export const deleteProject = (project: string) => request<void>(base(project), { method: 'DELETE' })

/**
 * Creates a repository owned by the project (maintainers and owners). The
 * project policy applies: name prefix, formats, types, public, repository cap.
 */
export const createProjectRepository = (project: string, body: Partial<Repository> & { upstream_password?: string }) =>
  send<Repository>(`${base(project)}/repositories`, 'POST', body)

/** Live repositories of the project, newest first. */
export async function listProjectRepositories(
  project: string,
  params: { page?: number; limit?: number } = {},
): Promise<ProjectRepositoryPage> {
  const qs = new URLSearchParams()
  if (params.page != null) qs.set('page', String(params.page))
  if (params.limit != null) qs.set('limit', String(params.limit))
  const query = qs.toString() ? `?${qs}` : ''
  const res = await request<Partial<ProjectRepositoryPage>>(`${base(project)}/repositories${query}`)
  return {
    repositories: res?.repositories ?? [],
    total: res?.total ?? 0,
    page: res?.page ?? params.page ?? 1,
    limit: res?.limit ?? params.limit ?? 0,
  }
}

export async function listProjectMembers(project: string): Promise<ProjectMember[]> {
  const res = await request<{ members?: ProjectMember[] }>(`${base(project)}/members`)
  return res?.members ?? []
}

export const addProjectMember = (project: string, body: ProjectMemberCreateRequest) =>
  send<ProjectMember>(`${base(project)}/members`, 'POST', body)

export const updateProjectMember = (project: string, userId: string, body: ProjectMemberUpdateRequest) =>
  send<ProjectMember>(`${base(project)}/members/${enc(userId)}`, 'PATCH', body)

export const removeProjectMember = (project: string, userId: string) =>
  request<void>(`${base(project)}/members/${enc(userId)}`, { method: 'DELETE' })

export async function listProjectGroups(project: string): Promise<ProjectGroup[]> {
  const res = await request<{ groups?: ProjectGroup[] }>(`${base(project)}/groups`)
  return res?.groups ?? []
}

/**
 * Attaches a group to the project, or changes the role of an already attached
 * group. `group` is the group id or its exact name.
 */
export const setProjectGroup = (project: string, group: string, body: ProjectGroupUpdateRequest) =>
  send<ProjectGroup>(`${base(project)}/groups/${enc(group)}`, 'PUT', body)

/** `group` is the group id or its exact name. */
export const removeProjectGroup = (project: string, group: string) =>
  request<void>(`${base(project)}/groups/${enc(group)}`, { method: 'DELETE' })

/** Global admin: whether non-admin users may create projects. */
export const getProjectSettings = () => request<ProjectSettings>('/api/v1/admin/projects/settings')

export const updateProjectSettings = (body: ProjectSettings) =>
  send<ProjectSettings>('/api/v1/admin/projects/settings', 'PUT', body)

/** Roles a project token can carry, lowest first. */
export const PROJECT_TOKEN_ROLES = ['viewer', 'developer'] as const
export type ProjectTokenRole = (typeof PROJECT_TOKEN_ROLES)[number]

export type ProjectToken = GeneratedProjectToken
type ProjectTokenCreateRequest = GeneratedProjectTokenCreateRequest
type ProjectTokenCreated = GeneratedProjectTokenCreateResponse

/** Maintainers and owners see every token of the project, other members only their own. */
export async function listProjectTokens(project: string): Promise<ProjectToken[]> {
  const res = await request<{ tokens?: ProjectToken[] }>(`${base(project)}/tokens`)
  return res?.tokens ?? []
}

/** Session only (API tokens get 403). The clear-text secret is in the response, once. */
export const createProjectToken = (project: string, body: ProjectTokenCreateRequest) =>
  send<ProjectTokenCreated>(`${base(project)}/tokens`, 'POST', body)

export const revokeProjectToken = (project: string, tokenId: string) =>
  request<void>(`${base(project)}/tokens/${enc(tokenId)}`, { method: 'DELETE' })

export type ProjectQuota = GeneratedProjectQuota
export type ProjectUsage = GeneratedProjectUsage
/** One repository row of {@link ProjectUsage}. */
export type ProjectUsageRepository = NonNullable<GeneratedProjectUsage['repositories']>[number]
/** Limits accepted by {@link setProjectQuota}; 0 means unlimited. */
type ProjectQuotaUpdate = QuotaLimits

/** Totals, the project quota and the per-repository breakdown; any member. */
export const getProjectUsage = (project: string) => request<ProjectUsage>(`${base(project)}/usage`)

/** Any member may read it; `enforced` is false while no limit is set. */
export const getProjectQuota = (project: string) => request<ProjectQuota>(`${base(project)}/quota`)

/** Global admins only (owners get 403). A limit of 0 is unlimited; counters are kept only while a limit is set. */
export const setProjectQuota = (project: string, body: ProjectQuotaUpdate) =>
  send<ProjectQuota>(`${base(project)}/quota`, 'PUT', body)

/** Rule fields of a project default cleanup policy; same shape as a repository policy. */
interface ProjectCleanupPolicyRequest {
  name?: string
  enabled?: boolean
  keep_last_n?: number
  keep_since_days?: number
  name_pattern?: string
  schedule_cron?: string
}

/** Default policies of the project, applied to hosted repositories without an enabled policy of their own. */
export async function listProjectCleanupPolicies(project: string): Promise<ApiCleanupPolicy[]> {
  const res = await request<{ policies?: ApiCleanupPolicy[] }>(`${base(project)}/cleanup-policies`)
  return res?.policies ?? []
}

/** Owners and global admins. */
export const createProjectCleanupPolicy = (project: string, body: ProjectCleanupPolicyRequest) =>
  send<ApiCleanupPolicy>(`${base(project)}/cleanup-policies`, 'POST', body)

/** Owners and global admins. */
export const updateProjectCleanupPolicy = (project: string, id: string, body: ProjectCleanupPolicyRequest) =>
  send<ApiCleanupPolicy>(`${base(project)}/cleanup-policies/${enc(id)}`, 'PUT', body)

/** Owners and global admins. */
export const deleteProjectCleanupPolicy = (project: string, id: string) =>
  request<void>(`${base(project)}/cleanup-policies/${enc(id)}`, { method: 'DELETE' })

/** Owners and global admins. What a run would remove per repository; removes nothing. */
export const dryRunProjectCleanupPolicy = (project: string, id: string) =>
  send<ProjectCleanupPreview>(`${base(project)}/cleanup-policies/${enc(id)}/dry-run`, 'POST', {})

/** One page of project activity, newest first. */
interface ProjectActivity {
  items: ProjectActivityEntry[]
  hasMore: boolean
  /** Pass as `cursor` to load the following page; null on the last page. */
  nextCursor: string | null
}

/**
 * Audit events of the project and of its repositories, newest first, for any
 * member. Keyset paging: pass the previous `nextCursor` as `cursor`, leave it
 * out for the newest entries. There is deliberately no total.
 */
export async function listProjectActivity(
  project: string,
  params: { cursor?: string; limit?: number } = {},
): Promise<ProjectActivity> {
  const qs = new URLSearchParams()
  if (params.cursor) qs.set('cursor', params.cursor)
  if (params.limit != null) qs.set('per_page', String(params.limit))
  const query = qs.toString() ? `?${qs}` : ''
  const res = await request<Partial<ProjectActivityPage>>(`${base(project)}/activity${query}`)
  return { items: res?.data ?? [], hasMore: res?.has_more ?? false, nextCursor: res?.next_cursor ?? null }
}

/** Downloads the project's activity (any member) as JSONL or CSV, newest first. */
export const downloadProjectActivityExport = (project: string, format: ExportFormat) =>
  downloadAttachment(`${base(project)}/activity/export?format=${format}`, `openartifact-${project}-activity.${format}`)

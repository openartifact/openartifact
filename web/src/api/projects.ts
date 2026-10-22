// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { Repository } from './repositories'
import type {
  Project as GeneratedProject,
  ProjectCreateRequest,
  ProjectGroup,
  ProjectGroupUpdateRequest,
  ProjectMember as GeneratedProjectMember,
  ProjectUpdateRequest,
} from './generated/types.gen'

export type { ProjectCreateRequest, ProjectGroup, ProjectGroupUpdateRequest, ProjectUpdateRequest }

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

export interface ProjectList {
  projects: Project[]
  /** The caller may create projects (global admin, or self-service creation is enabled). */
  can_create: boolean
}

export interface ProjectRepositoryPage {
  repositories: Repository[]
  total: number
  page: number
  limit: number
}

/** Add by `user_id` or `username`; the role defaults to viewer server-side. */
export interface ProjectMemberCreateRequest {
  user_id?: string
  username?: string
  role?: ProjectMemberRole
}

export interface ProjectMemberUpdateRequest {
  role: ProjectMemberRole
}

export interface ProjectSettings {
  /** Any user may create projects, not only global admins. */
  self_create: boolean
}

const JSON_HEADERS = { 'Content-Type': 'application/json' }
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

/** Moves a repository to another project; the caller must own both. */
export const transferProjectRepository = (project: string, name: string, toProject: string) =>
  send<Repository>(`${base(project)}/repositories/${enc(name)}/transfer`, 'POST', { to_project: toProject })

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

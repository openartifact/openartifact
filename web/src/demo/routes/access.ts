// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type {
  GetApiV1AdminAuditResponses,
  GetApiV1AdminProjectsSettingsResponses,
  GetApiV1AdminStorageResponses,
  GetApiV1GroupsByIdMembersResponses,
  GetApiV1GroupsResponses,
  GetApiV1ProjectsByProjectGroupsResponses,
  GetApiV1ProjectsByProjectMembersResponses,
  GetApiV1ProjectsByProjectRepositoriesResponses,
  GetApiV1ProjectsByProjectResponses,
  GetApiV1ProjectsResponses,
  GetApiV1TokensResponses,
  GetApiV1UsersByIdGroupsResponses,
  GetApiV1UsersResponses,
  Group,
  PatchApiV1UsersByIdResponses,
  PostApiV1GroupsResponses,
  PostApiV1UsersResponses,
  User,
} from '../../api/generated/types.gen'
import type { DemoGroup, DemoProject } from '../fixtures/people'
import { ago, uid } from '../fixtures/time'
import { failure, type DemoRequest, type DemoRoute, type Ok } from '../http'
import { matches, paginate, repoStats, repoView } from '../query'
import type { DemoState } from '../state'

function listUsers({ state, query }: DemoRequest): Ok<GetApiV1UsersResponses> {
  const rows = state.users.filter((u) => matches(`${u.username} ${u.full_name ?? ''} ${u.email ?? ''}`, query.get('q')))
  const perPage = Number(query.get('per_page')) || 20
  return { data: paginate(rows, query, 'per_page'), page: Number(query.get('page')) || 1, per_page: perPage, total: rows.length }
}

function createUser({ state, body }: DemoRequest): PostApiV1UsersResponses[201] | Response {
  const input = (body ?? {}) as User
  if (!input.username) return failure(400, 'username is required', 'validation_failed')
  if (state.users.some((u) => u.username === input.username)) return failure(409, 'user already exists', 'conflict')
  const now = new Date().toISOString()
  const user: User = {
    id: uid('user', state.users.length + 100),
    username: input.username,
    email: input.email ?? null,
    full_name: input.full_name ?? null,
    disabled: false,
    must_change_password: true,
    last_login_at: null,
    created_at: now,
    updated_at: now,
  }
  state.users.push(user)
  return user
}

function withUser(handler: (req: DemoRequest, user: User) => unknown): (req: DemoRequest) => unknown {
  return (req) => {
    const user = req.state.users.find((u) => u.id === req.params.id)
    return user ? handler(req, user) : failure(404, 'user not found', 'not_found')
  }
}

function updateUser(req: DemoRequest, user: User): Ok<PatchApiV1UsersByIdResponses> {
  const { disabled, must_change_password, email, full_name } = (req.body ?? {}) as User
  Object.assign(
    user,
    Object.fromEntries(Object.entries({ disabled, must_change_password, email, full_name }).filter(([, v]) => v !== undefined)),
    { updated_at: new Date().toISOString() },
  )
  return user
}

function userGroups({ state }: DemoRequest, user: User): Ok<GetApiV1UsersByIdGroupsResponses> {
  return { data: state.groups.filter((g) => g.members.includes(user.username ?? '')).map(groupView) }
}

const groupView = ({ members: _members, ...group }: DemoGroup): Group => group

function withGroup(handler: (req: DemoRequest, group: DemoGroup) => unknown): (req: DemoRequest) => unknown {
  return (req) => {
    const group = req.state.groups.find((g) => g.id === req.params.id)
    return group ? handler(req, group) : failure(404, 'group not found', 'not_found')
  }
}

function createGroup({ state, body }: DemoRequest): PostApiV1GroupsResponses[201] | Response {
  const input = (body ?? {}) as Group
  if (!input.name) return failure(400, 'name is required', 'validation_failed')
  if (state.groups.some((g) => g.name === input.name)) return failure(409, 'group already exists', 'conflict')
  const now = new Date().toISOString()
  const group: DemoGroup = { id: uid('group', state.groups.length + 100), name: input.name, description: input.description ?? null, created_at: now, updated_at: now, members: [] }
  state.groups.push(group)
  return groupView(group)
}

function groupMembers({ state }: DemoRequest, group: DemoGroup): Ok<GetApiV1GroupsByIdMembersResponses> {
  return {
    members: group.members.map((username) => ({ user_id: state.users.find((u) => u.username === username)?.id, username, role: 'member' })),
  }
}

function projectView(state: DemoState, p: DemoProject): Ok<GetApiV1ProjectsByProjectResponses> {
  const { members: _members, groups: _groups, ...project } = p
  return {
    ...project,
    owner_id: state.users.find((u) => u.username === p.members.find((m) => m.role === 'owner')?.username)?.id ?? null,
    my_role: p.members.find((m) => m.username === 'admin')?.role,
    repository_count: state.repositories.filter((r) => r.project === p.name).length,
  }
}

function listProjects({ state, query }: DemoRequest): Ok<GetApiV1ProjectsResponses> {
  const visible = query.get('all') === 'true' ? state.projects : state.projects.filter((p) => p.members.some((m) => m.username === 'admin'))
  const size = (p: DemoProject) =>
    state.repositories.filter((r) => r.project === p.name && r.repo_type !== 'group').reduce((n, r) => n + repoStats(state, r).size_bytes, 0)
  return {
    can_create: true,
    projects: visible.map((p) => ({ ...projectView(state, p), member_count: p.members.length, usage: { size_bytes: size(p) } })),
  }
}

function withProject(handler: (req: DemoRequest, project: DemoProject) => unknown): (req: DemoRequest) => unknown {
  return (req) => {
    const project = req.state.projects.find((p) => p.name === req.params.project)
    return project ? handler(req, project) : failure(404, 'project not found', 'not_found')
  }
}

function projectRepositories({ state, query }: DemoRequest, project: DemoProject): Ok<GetApiV1ProjectsByProjectRepositoriesResponses> {
  const repos = state.repositories.filter((r) => r.project === project.name)
  return { total: repos.length, repositories: paginate(repos, query).map((r) => repoView(state, r)) }
}

function projectMembers({ state }: DemoRequest, project: DemoProject): Ok<GetApiV1ProjectsByProjectMembersResponses> {
  return {
    members: project.members.map((m, i) => ({
      id: uid('project', 1000 + i),
      project_id: project.id,
      user_id: state.users.find((u) => u.username === m.username)?.id,
      username: m.username,
      role: m.role,
      created_at: project.created_at,
    })),
  }
}

function projectGroups({ state }: DemoRequest, project: DemoProject): Ok<GetApiV1ProjectsByProjectGroupsResponses> {
  return {
    groups: project.groups.map((g) => ({
      project_id: project.id,
      group_id: state.groups.find((x) => x.name === g.name)?.id,
      group_name: g.name,
      role: g.role,
      created_at: project.created_at,
    })),
  }
}

function listTokens({ state, query }: DemoRequest): Ok<GetApiV1TokensResponses> {
  const before = query.get('expires_before')
  const rows = state.tokens.filter(
    (tok) =>
      (!query.get('type') || tok.type === query.get('type')) &&
      (!query.get('project') || tok.project === query.get('project')) &&
      (!before || (tok.expires_at !== undefined && tok.expires_at < before)) &&
      matches(`${tok.name} ${tok.owner}`, query.get('q')),
  )
  const limit = Number(query.get('limit')) || 20
  return { tokens: paginate(rows, query), total: rows.length, page: Number(query.get('page')) || 1, limit }
}

function listAudit({ state, query }: DemoRequest): Ok<GetApiV1AdminAuditResponses> {
  const rows = state.audit.filter(
    (e) => (!query.get('actor') || e.actor_name === query.get('actor')) && (!query.get('action') || e.action === query.get('action')),
  )
  const perPage = Number(query.get('per_page')) || 50
  return { data: rows.slice(0, perPage), per_page: perPage, has_more: false, total: rows.length, total_exact: true }
}

function storage({ state }: DemoRequest): Ok<GetApiV1AdminStorageResponses> {
  return {
    total: 0,
    items: [],
    primary: {
      name: 'primary',
      kind: 's3',
      source: 'config',
      location: { bucket: 'acme-artifacts', region: 'eu-central-1' },
      status: 'healthy',
      last_checked_at: ago(0.2),
      usage: { blob_count: state.files.length, size_bytes: state.files.reduce((n, f) => n + f.size, 0) },
    },
  }
}

export const accessRoutes: DemoRoute[] = [
  ['GET', '/api/v1/users', listUsers],
  ['POST', '/api/v1/users', createUser],
  ['PATCH', '/api/v1/users/:id', withUser(updateUser)],
  [
    'DELETE',
    '/api/v1/users/:id',
    withUser(({ state }, user) => {
      state.users = state.users.filter((u) => u !== user)
    }),
  ],
  ['GET', '/api/v1/users/:id/groups', withUser(userGroups)],
  ['GET', '/api/v1/groups', ({ state }): Ok<GetApiV1GroupsResponses> => ({ data: state.groups.map(groupView) })],
  ['POST', '/api/v1/groups', createGroup],
  [
    'DELETE',
    '/api/v1/groups/:id',
    withGroup(({ state }, group) => {
      state.groups = state.groups.filter((g) => g !== group)
    }),
  ],
  ['GET', '/api/v1/groups/:id/members', withGroup(groupMembers)],
  ['GET', '/api/v1/projects', listProjects],
  ['GET', '/api/v1/projects/:project', withProject((req, p) => projectView(req.state, p))],
  ['GET', '/api/v1/projects/:project/repositories', withProject(projectRepositories)],
  ['GET', '/api/v1/projects/:project/members', withProject(projectMembers)],
  ['GET', '/api/v1/projects/:project/groups', withProject(projectGroups)],
  ['GET', '/api/v1/admin/projects/settings', (): Ok<GetApiV1AdminProjectsSettingsResponses> => ({ self_create: false })],
  ['GET', '/api/v1/tokens', listTokens],
  ['GET', '/api/v1/admin/audit', listAudit],
  ['GET', '/api/v1/admin/storage', storage],
]

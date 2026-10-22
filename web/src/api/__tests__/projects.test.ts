// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import {
  addProjectMember,
  createProject,
  deleteProject,
  getProject,
  getProjectSettings,
  listProjectGroups,
  listProjectMembers,
  listProjectRepositories,
  listProjects,
  removeProjectGroup,
  removeProjectMember,
  setProjectGroup,
  updateProject,
  updateProjectMember,
  updateProjectSettings,
} from '../projects'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

function lastCall(): { url: string; init: RequestInit } {
  const call = spy.mock.calls.at(-1)!
  return { url: String(call[0]), init: (call[1] ?? {}) as RequestInit }
}

function body(): unknown {
  return JSON.parse(lastCall().init.body as string)
}

describe('projects api', () => {
  it('lists the caller\'s projects with can_create, or every project with all=true', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () =>
      json({ projects: [{ name: 'a', my_role: 'owner', repository_count: 2 }], can_create: true }),
    )
    expect(await listProjects()).toEqual({
      projects: [{ name: 'a', my_role: 'owner', repository_count: 2 }],
      can_create: true,
    })
    expect(lastCall().url).toBe('/api/v1/projects')
    await listProjects({ all: true })
    expect(lastCall().url).toBe('/api/v1/projects?all=true')
  })

  it('treats a missing can_create as false', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({}))
    expect(await listProjects()).toEqual({ projects: [], can_create: false })
  })

  it('gets one project by encoded name', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ name: 'a/b' }))
    await getProject('a/b')
    expect(lastCall().url).toBe('/api/v1/projects/a%2Fb')
  })

  it('creates with POST', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({}, 201))
    await createProject({ name: 'p', display_name: 'P', description: 'd' })
    expect(lastCall().url).toBe('/api/v1/projects')
    expect(lastCall().init.method).toBe('POST')
    expect(body()).toEqual({ name: 'p', display_name: 'P', description: 'd' })
  })

  it('sends a partial update with PATCH and encodes the project name', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({}))
    await updateProject('a/b', { max_repositories: 3, allowed_formats: [] })
    const { url, init } = lastCall()
    expect(url).toBe('/api/v1/projects/a%2Fb')
    expect(init.method).toBe('PATCH')
    expect(body()).toEqual({ max_repositories: 3, allowed_formats: [] })
  })

  it('deletes with DELETE', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(null, { status: 204 }))
    await deleteProject('p')
    expect(lastCall().url).toBe('/api/v1/projects/p')
    expect(lastCall().init.method).toBe('DELETE')
  })

  it('pages the repositories of a project', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () =>
      json({ repositories: [{ name: 'r' }], total: 41, page: 2, limit: 20 }),
    )
    const res = await listProjectRepositories('p', { page: 2, limit: 20 })
    expect(lastCall().url).toBe('/api/v1/projects/p/repositories?page=2&limit=20')
    expect(res).toEqual({ repositories: [{ name: 'r' }], total: 41, page: 2, limit: 20 })
    await listProjectRepositories('p')
    expect(lastCall().url).toBe('/api/v1/projects/p/repositories')
  })

  it('lists members and adds one by username', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () =>
      json({ members: [{ user_id: 'u1', username: 'ann', role: 'viewer' }] }),
    )
    expect(await listProjectMembers('p')).toEqual([{ user_id: 'u1', username: 'ann', role: 'viewer' }])
    expect(lastCall().url).toBe('/api/v1/projects/p/members')
    await addProjectMember('p', { username: 'bob', role: 'developer' })
    expect(lastCall().url).toBe('/api/v1/projects/p/members')
    expect(lastCall().init.method).toBe('POST')
    expect(body()).toEqual({ username: 'bob', role: 'developer' })
    await addProjectMember('p', { user_id: 'u2' })
    expect(body()).toEqual({ user_id: 'u2' })
  })

  it('changes a member role with PATCH and removes with DELETE', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({}))
    await updateProjectMember('p', 'u1', { role: 'maintainer' })
    let call = lastCall()
    expect(call.url).toBe('/api/v1/projects/p/members/u1')
    expect(call.init.method).toBe('PATCH')
    expect(body()).toEqual({ role: 'maintainer' })
    spy.mockRestore()
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(null, { status: 204 }))
    await removeProjectMember('p', 'u1')
    call = lastCall()
    expect(call.url).toBe('/api/v1/projects/p/members/u1')
    expect(call.init.method).toBe('DELETE')
  })

  it('attaches (by id or name), lists and detaches groups', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ groups: [{ group_id: 'g1' }] }))
    await setProjectGroup('p', 'g1', { role: 'developer' })
    let call = lastCall()
    expect(call.url).toBe('/api/v1/projects/p/groups/g1')
    expect(call.init.method).toBe('PUT')
    expect(body()).toEqual({ role: 'developer' })
    await setProjectGroup('p', 'release team', { role: 'viewer' })
    expect(lastCall().url).toBe('/api/v1/projects/p/groups/release%20team')
    expect(await listProjectGroups('p')).toEqual([{ group_id: 'g1' }])
    expect(lastCall().url).toBe('/api/v1/projects/p/groups')
    spy.mockRestore()
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(null, { status: 204 }))
    await removeProjectGroup('p', 'g1')
    call = lastCall()
    expect(call.url).toBe('/api/v1/projects/p/groups/g1')
    expect(call.init.method).toBe('DELETE')
  })

  it('reads and writes the admin self_create setting', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ self_create: true }))
    expect(await getProjectSettings()).toEqual({ self_create: true })
    expect(lastCall().url).toBe('/api/v1/admin/projects/settings')
    await updateProjectSettings({ self_create: false })
    expect(lastCall().url).toBe('/api/v1/admin/projects/settings')
    expect(lastCall().init.method).toBe('PUT')
    expect(body()).toEqual({ self_create: false })
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import {
  forceUserPasswordReset,
  addGroupMember,
  removeGroupMember,
  assignPermissionTarget,
  deleteUser,
} from '../admin'

const ok = (): Response => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })

let spy: ReturnType<typeof spyOn<typeof globalThis, 'fetch'>>
afterEach(() => spy?.mockRestore())

function lastCall(): { url: string; init: RequestInit } {
  const call = spy.mock.calls.at(-1)!
  return { url: String(call[0]), init: (call[1] ?? {}) as RequestInit }
}

describe('admin api', () => {
  it('forces a password reset with the must_change_password key the backend reads', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => ok())
    await forceUserPasswordReset('u1')
    const { url, init } = lastCall()
    expect(url).toBe('/api/v1/users/u1')
    expect(JSON.parse(init.body as string)).toEqual({ must_change_password: true })
  })

  it('encodes path segments', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => ok())
    await deleteUser('a/b c')
    expect(lastCall().url).toBe('/api/v1/users/a%2Fb%20c')
  })

  it('adds and removes a single group member without replacing the membership', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => ok())
    await addGroupMember('g1', 'u2')
    expect(lastCall().url).toBe('/api/v1/groups/g1/members/u2')
    expect(lastCall().init.method).toBe('POST')
    await removeGroupMember('g1', 'u2')
    expect(lastCall().init.method).toBe('DELETE')
  })

  it('assigns a permission target using principal_type / principal_id', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => ok())
    await assignPermissionTarget('p1', { principal_type: 'group', principal_id: 'g1' })
    const { url, init } = lastCall()
    expect(url).toContain('/api/v1/permission-targets/p1')
    expect(JSON.parse(init.body as string)).toMatchObject({ principal_type: 'group', principal_id: 'g1' })
  })

  it('goes through request(): mutating calls carry X-Requested-With and credentials', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => ok())
    await deleteUser('u1')
    const { init } = lastCall()
    expect(new Headers(init.headers).get('X-Requested-With')).toBe('XMLHttpRequest')
    expect(init.credentials).toBe('include')
  })
})

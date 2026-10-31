// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { listUserGroups, listPermissionAssignments, unassignPermissionTarget } from '../admin'
import { createUserToken, listUserTokens, revokeUserToken } from '../tokens'
import {
  createOidcPolicy,
  createServiceAccount,
  deleteOidcPolicy,
  deleteServiceAccount,
  getServiceAccount,
  listAllServiceAccounts,
  listOidcPolicies,
  updateOidcPolicy,
  updateServiceAccount,
} from '../serviceAccounts'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

function stub(...bodies: unknown[]): void {
  let i = 0
  spy = spyOn(globalThis, 'fetch').mockImplementation(async () => {
    const body = bodies[Math.min(i++, bodies.length - 1)]
    return body === undefined ? new Response(null, { status: 204 }) : json(body)
  })
}

function call(n = -1): { url: string; method: string; body: unknown } {
  const [url, init] = spy.mock.calls.at(n)!
  return { url: String(url), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined }
}

const account = (n: number) => ({ id: `sa${n}`, name: `bot-${n}`, description: '', disabled: false })

describe('service accounts api', () => {
  it('lists one page when it is not full', async () => {
    stub({ data: [account(1)], page: 1, per_page: 200, total: 1 })
    const res = await listAllServiceAccounts()
    expect(res.items).toHaveLength(1)
    expect(res.total).toBe(1)
    expect(call()).toMatchObject({ url: '/api/v1/service-accounts?page=1&per_page=200', method: 'GET' })
  })

  it('walks every page of a long list', async () => {
    const first = Array.from({ length: 200 }, (_, i) => account(i))
    stub({ data: first, total: 201 }, { data: [account(200)], total: 201 })
    const res = await listAllServiceAccounts()
    expect(res.items).toHaveLength(201)
    expect(spy.mock.calls).toHaveLength(2)
    expect(call().url).toBe('/api/v1/service-accounts?page=2&per_page=200')
  })

  it('creates, reads, patches and deletes with the id encoded', async () => {
    stub(account(1))
    await createServiceAccount({ name: 'ci-bot', description: 'CI' })
    expect(call()).toEqual({ url: '/api/v1/service-accounts', method: 'POST', body: { name: 'ci-bot', description: 'CI' } })
    await getServiceAccount('a/b')
    expect(call()).toMatchObject({ url: '/api/v1/service-accounts/a%2Fb', method: 'GET' })
    await updateServiceAccount('sa1', { disabled: true })
    expect(call()).toEqual({ url: '/api/v1/service-accounts/sa1', method: 'PATCH', body: { disabled: true } })
    spy.mockRestore()
    stub(undefined)
    await deleteServiceAccount('sa1')
    expect(call()).toMatchObject({ url: '/api/v1/service-accounts/sa1', method: 'DELETE' })
  })
})

describe('oidc trust policies api', () => {
  const input = {
    name: 'ci',
    issuer: 'https://token.actions.githubusercontent.com',
    audience: 'https://oa.example',
    conditions: [{ claim: 'repository', patterns: ['acme/shop'] }],
    service_account_id: 'sa1',
    token_ttl_seconds: 900,
    enabled: true,
  }

  it('lists the unpaged data array and tolerates its absence', async () => {
    stub({ data: [{ id: 'p1' }] })
    expect(await listOidcPolicies()).toHaveLength(1)
    expect(call()).toMatchObject({ url: '/api/v1/oidc-trust-policies', method: 'GET' })
    spy.mockRestore()
    stub({})
    expect(await listOidcPolicies()).toEqual([])
  })

  it('creates, patches and deletes', async () => {
    stub({ id: 'p1' })
    await createOidcPolicy(input)
    expect(call()).toEqual({ url: '/api/v1/oidc-trust-policies', method: 'POST', body: input })
    await updateOidcPolicy('p1', { enabled: false })
    expect(call()).toEqual({ url: '/api/v1/oidc-trust-policies/p1', method: 'PATCH', body: { enabled: false } })
    spy.mockRestore()
    stub(undefined)
    await deleteOidcPolicy('p1')
    expect(call()).toMatchObject({ url: '/api/v1/oidc-trust-policies/p1', method: 'DELETE' })
  })

  it('surfaces the server message of a rejected policy', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ error: 'pin the repository' }, 400))
    await expect(createOidcPolicy(input)).rejects.toThrow('pin the repository')
  })
})

describe('apis a service account reuses', () => {
  it('lists, creates and revokes tokens of an account', async () => {
    stub({ data: [{ id: 't1', name: 'x', kind: 'deploy' }] })
    expect(await listUserTokens('sa1')).toHaveLength(1)
    expect(call()).toMatchObject({ url: '/api/v1/users/sa1/tokens', method: 'GET' })
    stub({ token: 'oa_secret' })
    const created = await createUserToken('sa1', { name: 'ci', kind: 'deploy' })
    expect(created.token).toBe('oa_secret')
    expect(call()).toEqual({ url: '/api/v1/users/sa1/tokens', method: 'POST', body: { name: 'ci', kind: 'deploy' } })
    spy.mockRestore()
    stub(undefined)
    await revokeUserToken('sa1', 't1')
    expect(call()).toMatchObject({ url: '/api/v1/users/sa1/tokens/t1', method: 'DELETE' })
  })

  it('reads and removes permission assignments of a principal', async () => {
    stub({ data: [{ principal_type: 'user', principal_id: 'sa1', principal_name: 'bot' }] })
    expect(await listPermissionAssignments(7)).toHaveLength(1)
    expect(call().url).toBe('/api/v1/permission-targets/7/assignments')
    spy.mockRestore()
    stub(undefined)
    await unassignPermissionTarget(7, { principal_type: 'user', principal_id: 'sa1' })
    expect(call()).toMatchObject({ url: '/api/v1/permission-targets/7/assignments?principal_type=user&principal_id=sa1', method: 'DELETE' })
  })

  it('lists the groups of a user', async () => {
    stub({ data: [{ id: 'g1', name: 'ci' }], total: 1, page: 1, per_page: 200 })
    expect(await listUserGroups('sa1')).toHaveLength(1)
    expect(call().url).toContain('/api/v1/users/sa1/groups')
  })
})

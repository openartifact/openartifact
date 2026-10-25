// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import {
  createCleanupPolicy,
  deleteCleanupPolicy,
  dryRunCleanupPolicy,
  listCleanupPolicies,
  runCleanupPolicy,
  updateCleanupPolicy,
} from '../cleanup'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

function stub(body: unknown = {}, status = 200): void {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async () => (status === 204 ? new Response(null, { status }) : json(body, status)))
}

function lastCall(): { url: string; method: string; body: unknown } {
  const [url, init] = spy.mock.calls.at(-1)!
  return { url: String(url), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined }
}

const BASE = '/api/v1/repositories/my%20repo/cleanup-policies'

describe('repository cleanup api', () => {
  it('lists policies and tolerates a missing array', async () => {
    stub({ policies: [{ id: 'p1', name: 'n', enabled: true, created_at: 'x' }] })
    expect(await listCleanupPolicies('my repo')).toHaveLength(1)
    expect(lastCall()).toMatchObject({ url: BASE, method: 'GET' })
    spy.mockRestore()
    stub({})
    expect(await listCleanupPolicies('my repo')).toEqual([])
  })

  it('creates and updates with the payload as JSON', async () => {
    stub({}, 201)
    await createCleanupPolicy('my repo', { name: 'n', enabled: true, keep_last_n: 5 })
    expect(lastCall()).toEqual({ url: BASE, method: 'POST', body: { name: 'n', enabled: true, keep_last_n: 5 } })
    await updateCleanupPolicy('my repo', 'p/1', { enabled: false })
    expect(lastCall()).toEqual({ url: `${BASE}/p%2F1`, method: 'PUT', body: { enabled: false } })
  })

  it('deletes, runs and dry-runs a policy by encoded id', async () => {
    stub({ policy_name: 'n' })
    await deleteCleanupPolicy('my repo', 'p/1')
    expect(lastCall()).toMatchObject({ url: `${BASE}/p%2F1`, method: 'DELETE' })
    await runCleanupPolicy('my repo', 'p/1')
    expect(lastCall()).toMatchObject({ url: `${BASE}/p%2F1/run`, method: 'POST' })
    expect(await dryRunCleanupPolicy('my repo', 'p/1')).toEqual({ policy_name: 'n' })
    expect(lastCall()).toMatchObject({ url: `${BASE}/p%2F1/dry-run`, method: 'POST' })
  })
})

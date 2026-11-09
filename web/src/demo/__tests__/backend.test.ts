// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { createDemoBackend, DEMO_UNAVAILABLE } from '../backend'

const json = { 'Content-Type': 'application/json' }

describe('createDemoBackend', () => {
  it('answers a call it has no route for with the demo error and records it', async () => {
    const backend = createDemoBackend()
    const res = await backend.transport('/api/v1/admin/webhooks?page=1', { method: 'GET' })
    expect(res.status).toBe(501)
    expect((await res.json()).code).toBe(DEMO_UNAVAILABLE)
    expect(backend.unhandled).toEqual(['GET /api/v1/admin/webhooks'])
  })

  it('keeps writes in memory, per backend', async () => {
    const backend = createDemoBackend()
    const created = await backend.transport('/api/v1/groups', { method: 'POST', headers: json, body: JSON.stringify({ name: 'sre' }) })
    expect(created.status).toBe(200)
    const names = async (b: typeof backend) =>
      ((await (await b.transport('/api/v1/groups', { method: 'GET' })).json()).data as { name: string }[]).map((g) => g.name)
    expect(await names(backend)).toContain('sre')
    expect(await names(createDemoBackend())).not.toContain('sre')
  })

  it('decodes path parameters and answers unknown names with 404', async () => {
    const backend = createDemoBackend()
    const scoped = await backend.transport('/api/v1/repositories/npm-internal/packages?package_name=%40acme%2Fui-kit&limit=100', { method: 'GET' })
    expect(((await scoped.json()).packages as { name: string }[]).every((p) => p.name === '@acme/ui-kit')).toBe(true)
    expect((await backend.transport('/api/v1/repository/nope', { method: 'GET' })).status).toBe(404)
    expect(backend.unhandled).toEqual([])
  })
})

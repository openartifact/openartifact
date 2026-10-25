// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { deleteSSOProvider, listAdminSSOProviders, saveSSOProvider, testSSOProvider, updateSSOProvider } from '../sso'

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

describe('sso api', () => {
  it('lists providers and tolerates a missing array', async () => {
    stub({ providers: [{ slug: 'okta' }] })
    expect(await listAdminSSOProviders()).toEqual([expect.objectContaining({ slug: 'okta' })])
    expect(lastCall()).toMatchObject({ url: '/api/v1/admin/sso-providers', method: 'GET' })
    spy.mockRestore()
    stub({})
    expect(await listAdminSSOProviders()).toEqual([])
  })

  it('creates a provider with POST', async () => {
    stub({}, 201)
    const input = { slug: 'okta', kind: 'oidc' as const, display_name: 'Okta', oidc_config: { issuer: 'https://i' } }
    await saveSSOProvider(input)
    expect(lastCall()).toEqual({ url: '/api/v1/admin/sso-providers', method: 'POST', body: input })
  })

  it('patches and deletes by slug, encoded', async () => {
    stub({ slug: 'a b' })
    await updateSSOProvider('a b', { enabled: false })
    expect(lastCall()).toEqual({ url: '/api/v1/admin/sso-providers/a%20b', method: 'PATCH', body: { enabled: false } })
    await updateSSOProvider('a b', { display_name: 'X', link_by_email: true })
    expect(lastCall().body).toEqual({ display_name: 'X', link_by_email: true })
    spy.mockRestore()
    stub(undefined, 204)
    await deleteSSOProvider('a b')
    expect(lastCall()).toMatchObject({ url: '/api/v1/admin/sso-providers/a%20b', method: 'DELETE' })
  })

  it('reports test outcomes, mapping API errors to ok:false', async () => {
    stub({ ok: true, message: 'fine' })
    expect(await testSSOProvider('okta')).toEqual({ ok: true, message: 'fine' })
    expect(lastCall()).toMatchObject({ url: '/api/v1/admin/sso-providers/okta/test', method: 'POST' })
    spy.mockRestore()
    stub({ message: 'bad' })
    expect((await testSSOProvider('okta')).ok).toBe(false)
    spy.mockRestore()
    stub({ error: 'nope' }, 500)
    expect((await testSSOProvider('okta')).ok).toBe(false)
  })
})

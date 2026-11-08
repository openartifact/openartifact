// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import {
  addBundleItems,
  listBundleChecks,
  listBundleSigningKeys,
  listBundles,
  manifestUrl,
  promoteBundle,
  removeBundleItem,
  sealBundle,
  updatePipeline,
  searchItemCandidates,
  signatureUrl,
} from '../bundles'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: ReturnType<typeof spyOn<typeof globalThis, 'fetch'>>
afterEach(() => spy?.mockRestore())

const call = (i = 0) => ({
  url: String(spy.mock.calls[i]![0]),
  init: (spy.mock.calls[i]![1] ?? {}) as RequestInit,
})

describe('bundles api', () => {
  it('lists with only the filters that are set', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ total: 0, page: 1, limit: 25, bundles: [] }))
    await listBundles({ name: 'shop', state: '', page: 2, limit: 25 })
    expect(call().url).toBe('/api/v1/bundles?name=shop&page=2&limit=25')
  })

  it('escapes name and version in every path', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await removeBundleItem('my shop', '1.0+rc?1', 'ab')
    expect(call().url).toBe('/api/v1/bundles/my%20shop/1.0%2Brc%3F1/items/ab')
    expect(manifestUrl('a b', '1')).toBe('/api/v1/bundles/a%20b/1/manifest')
    expect(signatureUrl('a', '1')).toBe('/api/v1/bundles/a/1/manifest.asc')
  })

  it('seals without a body, or with the chosen key', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({}))
    await sealBundle('shop', '1')
    expect(call(0).init.method).toBe('POST')
    expect(call(0).init.body).toBeUndefined()

    await sealBundle('shop', '1', 'ABCD')
    expect(JSON.parse(call(1).init.body as string)).toEqual({ gpg_key_id: 'ABCD' })
  })

  it('lists the keys the user may seal with, empty when the answer has none', async () => {
    const key = { id: 'k-1', name: 'r', fingerprint: 'AB', key_id: 'AB', algorithm: 'ed25519', user_id: 'u', active_global: true }
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ keys: [key] }))
    expect(await listBundleSigningKeys()).toEqual([key])
    expect(call().url).toBe('/api/v1/bundles/signing-keys')

    spy.mockResolvedValue(json({}))
    expect(await listBundleSigningKeys()).toEqual([])
  })

  it('wraps items and sends the promote contract', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ items: [] }, 201))
    await addBundleItems('shop', '1', [{ source_repo_id: 'r', sha256: 'a'.repeat(64), artifact_path: 'x' }])
    expect(JSON.parse(call(0).init.body as string).items).toHaveLength(1)

    spy.mockImplementation(async () => json({}))
    await promoteBundle('shop', '1', { to_stage: 'prod', target_repo: 'releases', notes: 'go' })
    expect(call(1).url).toBe('/api/v1/bundles/shop/1/promote')
    expect(JSON.parse(call(1).init.body as string)).toEqual({ to_stage: 'prod', target_repo: 'releases', notes: 'go' })
  })

  it('searches stored files by free text or digest, ungrouped', async () => {
    const hit = { repository: 'r', format: 'maven', name: 'shop', version: '1', path: 'a.jar', sha256: 'ab', size: 1 }
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ packages: [hit], total: 1 }))
    expect(await searchItemCandidates('sha256:ab cd')).toEqual([hit])
    expect(call().url).toBe('/api/v1/search?q=sha256%3Aab+cd&limit=25')

    spy.mockResolvedValue(json({}))
    expect(await searchItemCandidates('x')).toEqual([])
  })

  it('lists the checks of a bundle, empty when the answer has none', async () => {
    const check = { name: 'scan', status: 'success', summary: '', details_url: '', reported_by: 'ci', updated_at: '2026-10-03T10:00:00Z', expires_at: null }
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ checks: [check] }))
    expect(await listBundleChecks('my shop', '1+2')).toEqual([check])
    expect(call().url).toBe('/api/v1/bundles/my%20shop/1%2B2/checks')

    spy.mockResolvedValue(json({}))
    expect(await listBundleChecks('shop', '1')).toEqual([])
  })

  it('patches a pipeline by escaped name, carrying the required checks of each stage', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({}))
    await updatePipeline('my pipe', { stages: [{ name: 'prod', required_checks: ['scan'] }] })
    expect(call().url).toBe('/api/v1/pipelines/my%20pipe')
    expect(call().init.method).toBe('PATCH')
    expect(JSON.parse(call().init.body as string)).toEqual({ stages: [{ name: 'prod', required_checks: ['scan'] }] })
  })
})

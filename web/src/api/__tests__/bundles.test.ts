// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import {
  addBundleItems,
  listBundles,
  manifestUrl,
  promoteBundle,
  removeBundleItem,
  sealBundle,
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

  it('wraps items and sends the promote contract', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ items: [] }, 201))
    await addBundleItems('shop', '1', [{ source_repo_id: 'r', sha256: 'a'.repeat(64), artifact_path: 'x' }])
    expect(JSON.parse(call(0).init.body as string).items).toHaveLength(1)

    spy.mockImplementation(async () => json({}))
    await promoteBundle('shop', '1', { to_stage: 'prod', target_repo: 'releases', notes: 'go' })
    expect(call(1).url).toBe('/api/v1/bundles/shop/1/promote')
    expect(JSON.parse(call(1).init.body as string)).toEqual({ to_stage: 'prod', target_repo: 'releases', notes: 'go' })
  })
})

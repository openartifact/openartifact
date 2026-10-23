// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { downloadAuditExport, listAudit } from '../audit'
import { ApiError } from '../client'

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function requestedUrl(fetchSpy: { mock: { calls: unknown[][] } }): URL {
  return new URL(String(fetchSpy.mock.calls[0]![0]), 'http://localhost')
}

describe('listAudit', () => {
  it('asks for keyset paging from the newest entry and sends filters', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ data: [] }))
    await listAudit({ limit: 50, actor: 'alice', action: 'auth' })
    const url = requestedUrl(fetchSpy)
    expect(url.pathname).toBe('/api/v1/admin/audit')
    expect(url.searchParams.get('cursor')).toBe('')
    expect(url.searchParams.get('per_page')).toBe('50')
    expect(url.searchParams.get('actor')).toBe('alice')
    expect(url.searchParams.get('action')).toBe('auth')
    expect(url.searchParams.has('page')).toBe(false)
  })

  it('omits empty filters and forwards the cursor', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ data: [] }))
    await listAudit({ cursor: 'abc_-', actor: '' })
    const url = requestedUrl(fetchSpy)
    expect(url.searchParams.get('cursor')).toBe('abc_-')
    expect(url.searchParams.has('actor')).toBe(false)
    expect(url.searchParams.has('per_page')).toBe(false)
  })

  it('maps has_more and next_cursor', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValueOnce(json({ data: [{ id: '1' }], has_more: true, next_cursor: 'c2' }))
    expect(await listAudit()).toEqual({ items: [{ id: '1' }], hasMore: true, nextCursor: 'c2' })
  })

  it('reports the last page without a cursor', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValueOnce(json({ data: [{ id: '9' }], has_more: false }))
    expect(await listAudit()).toEqual({ items: [{ id: '9' }], hasMore: false, nextCursor: null })
  })
})

describe('downloadAuditExport', () => {
  it('preflights the export with the filters, then lets the browser stream it', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('x', { status: 200, headers: { 'Content-Disposition': 'attachment; filename="openartifact-audit-20260101T000000Z.csv"' } }),
    )
    const clicked: { href: string; download: string }[] = []
    spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.getAttribute('href') ?? '', download: this.download })
    })

    await downloadAuditExport('csv', { actor: 'alice', action: '' })

    const url = requestedUrl(fetchSpy)
    expect(url.pathname).toBe('/api/v1/admin/audit/export')
    expect(url.searchParams.get('format')).toBe('csv')
    expect(url.searchParams.get('actor')).toBe('alice')
    expect(url.searchParams.has('action')).toBe(false)
    expect(clicked).toEqual([{ href: '/api/v1/admin/audit/export?format=csv&actor=alice', download: 'openartifact-audit-20260101T000000Z.csv' }])
  })

  it('rejects with the server error and starts no download', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ error: 'admin access required', code: 'forbidden' }), { status: 403, headers: { 'Content-Type': 'application/json' } }),
    )
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    const err = await downloadAuditExport('jsonl').catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect(click).not.toHaveBeenCalled()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { backupDownloadUrl, createBackup, listBackups, restoreBackup } from '../backup'

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

describe('backup api', () => {
  it('lists backups and tolerates a missing array', async () => {
    stub({ backups: [{ id: 'b1', status: 'completed', type: 'full', created_at: '2026-01-01T00:00:00Z' }] })
    expect(await listBackups()).toHaveLength(1)
    expect(lastCall()).toMatchObject({ url: '/api/v1/admin/backup', method: 'GET' })
    spy.mockRestore()
    stub({})
    expect(await listBackups()).toEqual([])
  })

  it('starts a backup of the chosen type', async () => {
    stub({}, 202)
    await createBackup('metadata-only')
    expect(lastCall()).toEqual({ url: '/api/v1/admin/backup', method: 'POST', body: { type: 'metadata-only' } })
  })

  it('builds the download URL with the id encoded', () => {
    expect(backupDownloadUrl('b 1')).toBe('/api/v1/admin/backup/b%201/download')
  })

  it('uploads the archive as multipart field "file"', async () => {
    stub({}, 202)
    const file = new File(['x'], 'backup.tar.gz')
    await restoreBackup(file)
    const [url, init] = spy.mock.calls.at(-1)!
    expect(String(url)).toBe('/api/v1/admin/restore')
    expect(init?.method).toBe('POST')
    expect(init?.body).toBeInstanceOf(FormData)
    expect((init?.body as FormData).get('file')).toBeInstanceOf(File)
  })
})

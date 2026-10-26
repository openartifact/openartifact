// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { loadCleanupOverview } from '../cleanupOverview'

let spy: Mock<typeof fetch> | undefined
afterEach(() => spy?.mockRestore())

describe('loadCleanupOverview', () => {
  it('gathers policies from hosted repositories, tagging each with its repository and format, and lists the ones that failed', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
      if (url.pathname === '/api/v1/repositories') {
        return json({
          repositories: [
            { id: '1', name: 'a', format: 'npm', repo_type: 'hosted' },
            { id: '2', name: 'p', format: 'npm', repo_type: 'proxy' },
            { id: '3', name: 'bad', format: 'raw', repo_type: 'hosted' },
          ],
          total: 3,
        })
      }
      if (url.pathname.includes('/bad/')) return json({ error: 'no' }, 403)
      return json({ policies: [{ id: 'p1', name: 'keep', enabled: true, keep_last_n: 2, created_at: '2026-01-01T00:00:00Z' }] })
    }) as never)

    const overview = await loadCleanupOverview()
    expect(overview.rows.map((r) => [r.repository, r.format, r.name])).toEqual([['a', 'npm', 'keep']])
    expect(overview.failed).toEqual(['bad'])
  })
})

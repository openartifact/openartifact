// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, spyOn } from 'bun:test'
import { flushPromises } from '@vue/test-utils'
import { useAiAvailable } from '../../composables/useAppVersion'
import type { AdminSystemInfo } from '../../api/admin'
import type { SecurityHealth } from '../../api/security'
import { healthRows, healthSummary } from '../systemHealth'

const INFO = { db_status: 'ok', storage_kind: 's3', storage_status: 'ok' } as AdminSystemInfo

const HEALTH: SecurityHealth = {
  scanners: [{ name: 'osv', ready: true }],
  scans_pending: 0,
  scans_failed_24h: 0,
  dependency_track: { enabled: false },
  ai: { enabled: false },
  unscanned_repositories: 0,
}

const byId = (rows: ReturnType<typeof healthRows>, id: string) => rows.find((r) => r.id === id)!

describe('healthRows', () => {
  it('marks the database and storage by the status the server reports', () => {
    const rows = healthRows({ ...INFO, db_status: 'error' } as AdminSystemInfo, { health: HEALTH, error: '' })
    expect(byId(rows, 'database').status).toBe('bad')
    expect(byId(rows, 'storage').status).toBe('ok')
    expect(byId(rows, 'storage').sub).toContain('s3')
  })

  it('stands one unavailable row in for the scanning side when its health failed', () => {
    const rows = healthRows(INFO, { health: null, error: 'HTTP 500' })
    expect(rows.map((r) => r.id)).toEqual(['database', 'storage', 'security'])
    expect(byId(rows, 'security')).toMatchObject({ status: 'bad', detail: 'HTTP 500' })
  })

  it('keeps an unready scanner a warning with its message, and only https guides', () => {
    const rows = healthRows(INFO, {
      health: {
        ...HEALTH,
        scanners: [
          { name: 'trivy', ready: false, message: 'no db', docs_url: 'https://x.test/g' },
          { name: 'grype', ready: false, docs_url: 'javascript:alert(1)' },
        ],
      },
      error: '',
    })
    expect(byId(rows, 'scanner-trivy')).toMatchObject({ status: 'warn', detail: 'no db', docs: 'https://x.test/g' })
    expect(byId(rows, 'scanner-grype').docs).toBeUndefined()
  })

  it('reads Dependency-Track as off, healthy or a failed push', () => {
    const dt = (d: SecurityHealth['dependency_track']) => byId(healthRows(INFO, { health: { ...HEALTH, dependency_track: d }, error: '' }), 'dependency-track')
    expect(dt({ enabled: false }).status).toBe('none')
    expect(dt({ enabled: true, last_push_ok: true, last_push_at: '2026-03-05T09:00:00Z' }).detail).toContain('2026-03-05 09:00:00 UTC')
    expect(dt({ enabled: true, last_push_ok: false }).status).toBe('warn')
    expect(dt({ enabled: true }).status).toBe('ok')
  })

  it('warns about failed scan jobs and repositories never scanned', () => {
    const rows = healthRows(INFO, { health: { ...HEALTH, scans_failed_24h: 2, unscanned_repositories: 1 }, error: '' })
    expect(byId(rows, 'jobs').status).toBe('warn')
    expect(byId(rows, 'unscanned').status).toBe('warn')
    expect(byId(healthRows(INFO, { health: HEALTH, error: '' }), 'jobs').detail).toContain('Nothing queued')
  })
})

describe('healthRows on a build without AI analysis', () => {
  it('has the AI row while the server has AI', () => {
    expect(healthRows(INFO, { health: HEALTH, error: '' }).some((r) => r.id === 'ai')).toBe(true)
  })

  it('drops the AI row: nothing to report or set up there', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () =>
      new Response(JSON.stringify({ version: '1', ai_available: false }), { status: 200, headers: { 'Content-Type': 'application/json' } })) as never)
    useAiAvailable()
    await flushPromises()
    expect(healthRows(INFO, { health: HEALTH, error: '' }).some((r) => r.id === 'ai')).toBe(false)
  })
})

describe('healthSummary', () => {
  it('lets unavailable beat warnings beat healthy', () => {
    expect(healthSummary([{ status: 'ok' }, { status: 'none' }])).toEqual({ status: 'ok', text: 'All components healthy' })
    expect(healthSummary([{ status: 'warn' }, { status: 'ok' }])).toMatchObject({ status: 'warn', text: '1 warning' })
    expect(healthSummary([{ status: 'warn' }, { status: 'bad' }, { status: 'bad' }])).toMatchObject({ status: 'bad', text: '2 components unavailable' })
  })
})

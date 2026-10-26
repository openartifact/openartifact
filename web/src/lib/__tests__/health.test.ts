// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { deriveHealth, unhealthyCount } from '../health'

describe('deriveHealth', () => {
  it('yields no cells when nothing is readable', () => {
    expect(deriveHealth({})).toEqual([])
  })

  it('builds only the cells the sources can supply', () => {
    const cells = deriveHealth({
      system: { db_status: 'ok', version: '0.6.0', uptime: '3 days' },
      storage: [{ name: 'fs', status: 'healthy' }, { name: 's3', status: 'unreachable' }],
      security: { scanners: [{ name: 'osv', ready: true }, { name: 'trivy', ready: false }] },
      nearQuota: 2,
    })
    expect(cells.map((c) => [c.id, c.status])).toEqual([
      ['database', 'ok'],
      ['storage', 'bad'],
      ['scanners', 'warn'],
      ['quota', 'warn'],
      ['version', 'info'],
    ])
    expect(unhealthyCount(cells)).toBe(3)
    expect(cells.every((c) => c.to.startsWith('/settings/'))).toBe(true)
  })

  it('reports ready scanners by name and a down database', () => {
    const cells = deriveHealth({ system: { db_status: 'error' }, security: { scanners: [{ name: 'osv', ready: true }] } })
    expect(cells[0]).toMatchObject({ id: 'database', status: 'bad', value: 'down' })
    expect(cells[1]).toMatchObject({ id: 'scanners', status: 'ok', params: { names: 'osv' } })
  })

  it('falls back to the system storage status without a backend list', () => {
    expect(deriveHealth({ system: { storage_status: 'ok' } })[0]).toMatchObject({ id: 'storage', status: 'ok' })
  })
})

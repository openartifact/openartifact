// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AdminSystemInfo } from '../api/admin'
import type { SecurityHealth } from '../api/dashboard'
import type { StorageBackend } from '../api/storage'

export type HealthStatus = 'ok' | 'warn' | 'bad' | 'info'

/** One cell of the health strip; texts live under `dashboard.health.<id>.<detail>` in the catalog. */
export interface HealthCell {
  id: 'database' | 'storage' | 'scanners' | 'quota' | 'version'
  status: HealthStatus
  /** Catalog key suffix of the one-line status (`dashboard.health.<id>.<value>`). */
  value: string
  params: Record<string, string>
  to: string
}

/** Sources are absent (null/undefined) when unavailable to the caller; their cells are left out. */
export interface HealthInput {
  system?: AdminSystemInfo | null
  storage?: StorageBackend[] | null
  security?: SecurityHealth | null
  /** Repositories above the quota threshold. */
  nearQuota?: number | null
}

export function deriveHealth(input: HealthInput): HealthCell[] {
  const cells: HealthCell[] = []
  const { system, storage, security, nearQuota } = input

  if (system?.db_status) {
    const ok = system.db_status === 'ok'
    cells.push({ id: 'database', status: ok ? 'ok' : 'bad', value: ok ? 'ok' : 'down', params: { uptime: system.uptime ?? '' }, to: '/settings/health' })
  }

  if (storage && storage.length > 0) {
    const down = storage.filter((b) => b.status === 'unreachable').length
    cells.push({
      id: 'storage',
      status: down > 0 ? 'bad' : 'ok',
      value: down > 0 ? 'down' : 'ok',
      params: { count: String(storage.length), down: String(down), up: String(storage.length - down) },
      to: '/settings/backends',
    })
  } else if (system?.storage_status) {
    const ok = system.storage_status === 'ok'
    cells.push({ id: 'storage', status: ok ? 'ok' : 'bad', value: ok ? 'okSingle' : 'downSingle', params: {}, to: '/settings/backends' })
  }

  if (security) {
    const ready = security.scanners.filter((s) => s.ready)
    const names = ready.map((s) => s.name).join(', ')
    const status: HealthStatus = security.scanners.length === 0 || ready.length === 0 ? 'warn' : ready.length < security.scanners.length ? 'warn' : 'ok'
    const value = security.scanners.length === 0 ? 'none' : ready.length === 0 ? 'down' : ready.length < security.scanners.length ? 'partial' : 'ok'
    cells.push({
      id: 'scanners',
      status,
      value,
      params: { names, ready: String(ready.length), count: String(security.scanners.length) },
      to: '/settings/scanners',
    })
  }

  if (typeof nearQuota === 'number') {
    cells.push({ id: 'quota', status: nearQuota > 0 ? 'warn' : 'ok', value: nearQuota > 0 ? 'near' : 'ok', params: { count: String(nearQuota) }, to: '/settings/quotas' })
  }

  if (system?.version) {
    cells.push({ id: 'version', status: 'info', value: 'version', params: { version: system.version, uptime: system.uptime ?? '' }, to: '/settings/health' })
  }
  return cells
}

/** Cells that are not healthy (the version cell is informational). */
export function unhealthyCount(cells: readonly HealthCell[]): number {
  return cells.filter((c) => c.status === 'warn' || c.status === 'bad').length
}

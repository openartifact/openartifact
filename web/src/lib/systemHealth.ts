// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AdminSystemInfo } from '../api/admin'
import type { SecurityHealth } from '../api/security'
import { formatUtc } from './datetime'
import { safeDocsUrl, vulnScannerLabel } from './vulnScanners'
import { t, tp } from '../i18n/en'

export type HealthStatus = 'ok' | 'warn' | 'bad' | 'none'

export interface HealthRow {
  id: string
  name: string
  sub: string
  status: HealthStatus
  detail: string
  /** In-app page that fixes or configures the component. */
  to?: string
  toLabel?: string
  /** External guide (https only). */
  docs?: string
}

/** Security health failed to load: one row stands for the whole scanning side. */
export interface SecurityHealthState {
  health: SecurityHealth | null
  error: string
}

const utc = (value: string | null | undefined): string => `${formatUtc(value)} UTC`

function okOrBad(status: string | undefined): HealthStatus {
  return status === 'ok' ? 'ok' : 'bad'
}

/** Rows of the components table: the server's own checks first, then the scanning pipeline and its integrations. */
export function healthRows(info: AdminSystemInfo, security: SecurityHealthState): HealthRow[] {
  const rows: HealthRow[] = [
    {
      id: 'database',
      name: t('sx.health.database'),
      sub: t('sx.health.database.sub'),
      status: okOrBad(info.db_status),
      detail: tp('sx.health.database.detail', { status: info.db_status || '—' }),
    },
    {
      id: 'storage',
      name: t('sx.health.storage'),
      sub: tp('sx.health.storage.sub', { kind: info.storage_kind || '—' }),
      status: okOrBad(info.storage_status),
      detail: tp('sx.health.storage.detail', { status: info.storage_status || '—' }),
      to: '/settings/backends',
      toLabel: t('sx.health.details'),
    },
  ]

  const h = security.health
  if (!h) {
    rows.push({
      id: 'security',
      name: t('sx.health.scanners'),
      sub: t('sx.health.scanners.sub'),
      status: 'bad',
      detail: security.error,
    })
    return rows
  }

  for (const s of h.scanners) {
    rows.push({
      id: `scanner-${s.name}`,
      name: vulnScannerLabel(s.name),
      sub: t('sx.health.scanner.sub'),
      status: s.ready ? 'ok' : 'warn',
      detail: s.message || (s.ready ? t('sx.health.scanner.ready') : t('sx.scanners.notReady')),
      to: s.ready ? undefined : '/settings/scanners',
      toLabel: s.ready ? undefined : t('sx.health.details'),
      docs: safeDocsUrl(s.docs_url),
    })
  }

  const dt = h.dependency_track
  rows.push({
    id: 'dependency-track',
    name: t('sx.health.dt'),
    sub: t('sx.health.dt.sub'),
    status: !dt.enabled ? 'none' : dt.last_push_ok === false ? 'warn' : 'ok',
    detail: !dt.enabled
      ? t('sx.health.dt.off')
      : dt.last_push_ok === undefined
        ? t('sx.health.dt.noPush')
        : tp(dt.last_push_ok ? 'sx.health.dt.pushOk' : 'sx.health.dt.pushFailed', { time: utc(dt.last_push_at) }),
    to: '/settings/dependency-track',
    toLabel: dt.enabled ? t('sx.health.details') : t('sx.health.setUp'),
  })

  rows.push({
    id: 'ai',
    name: t('sx.health.ai'),
    sub: t('sx.health.ai.sub'),
    status: h.ai.enabled ? 'ok' : 'none',
    detail: h.ai.enabled ? t('sx.health.ai.on') : t('sx.health.ai.off'),
    to: '/settings/ai',
    toLabel: h.ai.enabled ? t('sx.health.details') : t('sx.health.setUp'),
  })

  const idle = h.scans_pending === 0 && h.scans_failed_24h === 0
  rows.push({
    id: 'jobs',
    name: t('sx.health.jobs'),
    sub: t('sx.health.jobs.sub'),
    status: h.scans_failed_24h > 0 ? 'warn' : 'ok',
    detail:
      (idle ? t('sx.health.jobs.idle') : tp('sx.health.jobs.detail', { pending: String(h.scans_pending), failed: String(h.scans_failed_24h) })) +
      (h.last_scan_at ? ` ${tp('sx.health.jobs.lastScan', { time: utc(h.last_scan_at) })}` : ''),
  })

  rows.push({
    id: 'unscanned',
    name: t('sx.health.unscanned'),
    sub: t('sx.health.unscanned.sub'),
    status: h.unscanned_repositories > 0 ? 'warn' : 'ok',
    detail:
      h.unscanned_repositories > 0
        ? tp('sx.health.unscanned.detail', { n: String(h.unscanned_repositories) })
        : t('sx.health.unscanned.none'),
    to: '/security',
    toLabel: t('sx.health.details'),
  })

  return rows
}

export interface HealthSummary {
  status: HealthStatus
  text: string
}

/** One line for the whole page: unavailable beats warnings beats healthy. */
export function healthSummary(rows: readonly Pick<HealthRow, 'status'>[]): HealthSummary {
  const bad = rows.filter((r) => r.status === 'bad').length
  if (bad > 0) return { status: 'bad', text: bad === 1 ? t('sx.health.unavailableOne') : tp('sx.health.unavailable', { n: String(bad) }) }
  const warn = rows.filter((r) => r.status === 'warn').length
  if (warn > 0) return { status: 'warn', text: warn === 1 ? t('sx.health.warning') : tp('sx.health.warnings', { n: String(warn) }) }
  return { status: 'ok', text: t('sx.health.allHealthy') }
}

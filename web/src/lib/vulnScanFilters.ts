// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { LocationQuery, LocationQueryRaw } from 'vue-router'
import {
  VULN_SCANNERS,
  type VulnScanFilters,
  type VulnScanner,
  type VulnScanStatusFilter,
  type VulnSeverity,
} from '../api/security'

export const VULN_SEVERITY_FILTERS: readonly VulnSeverity[] = ['critical', 'high', 'medium', 'low']
export const VULN_STATUS_FILTERS: readonly VulnScanStatusFilter[] = ['completed', 'partial', 'failed', 'scanning']

/** Filter state of the scan overview; an empty string means "not filtered". */
export interface VulnFilterState {
  repo: string
  severity: VulnSeverity | ''
  status: VulnScanStatusFilter | ''
  scanner: VulnScanner | ''
  quarantined: 'true' | 'false' | ''
}

export const EMPTY_VULN_FILTERS: VulnFilterState = {
  repo: '',
  severity: '',
  status: '',
  scanner: '',
  quarantined: '',
}

const FILTER_KEYS = Object.keys(EMPTY_VULN_FILTERS) as (keyof VulnFilterState)[]

function single(value: LocationQuery[string] | undefined): string {
  if (typeof value === 'string') return value
  return Array.isArray(value) && typeof value[0] === 'string' ? value[0] : ''
}

function oneOf<T extends string>(allowed: readonly T[], raw: string): T | '' {
  return (allowed as readonly string[]).includes(raw) ? (raw as T) : ''
}

/** Reads the filters from a URL query; unknown or malformed values are ignored. */
export function parseVulnFilters(query: LocationQuery): VulnFilterState {
  return {
    repo: single(query.repo).trim(),
    severity: oneOf(VULN_SEVERITY_FILTERS, single(query.severity)),
    status: oneOf(VULN_STATUS_FILTERS, single(query.status)),
    scanner: oneOf(VULN_SCANNERS, single(query.scanner)),
    quarantined: oneOf(['true', 'false'] as const, single(query.quarantined)),
  }
}

/** Merges the filters into a URL query, keeping every unrelated parameter and omitting empty filters. */
export function buildVulnQuery(base: LocationQuery, filters: VulnFilterState): LocationQueryRaw {
  const query: LocationQueryRaw = { ...base }
  for (const key of FILTER_KEYS) {
    if (filters[key]) query[key] = filters[key]
    else delete query[key]
  }
  return query
}

export function hasVulnFilters(filters: VulnFilterState): boolean {
  return FILTER_KEYS.some((key) => filters[key] !== '')
}

/** Request parameters of the filters. */
export function toVulnScanFilters(filters: VulnFilterState): VulnScanFilters {
  return {
    repo: filters.repo || undefined,
    severity: filters.severity || undefined,
    status: filters.status || undefined,
    scanner: filters.scanner || undefined,
    quarantined: filters.quarantined ? filters.quarantined === 'true' : undefined,
  }
}

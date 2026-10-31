// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Form model of the proxy settings of a repository: validation that mirrors the server
// (internal/api/repositories.go), the PATCH body of what changed, and the mapping of a
// server error back to the field it names.

import type { Repository } from '../api/repositories'
import { formatDuration } from './duration'
import { formatNumber } from './intl'
import { t, tp } from '../i18n'

/** Server defaults and ceilings of the numeric fields (seconds, hours for the minimum age). */
export const PROXY_LIMITS = {
  cacheTtl: { def: 3600, max: 30 * 24 * 3600 },
  negativeCacheTtl: { def: 60, max: 24 * 3600 },
  maxStale: { def: 7 * 24 * 3600, max: 365 * 24 * 3600 },
  minAgeHours: { max: 10 * 365 * 24 },
} as const

/** What happens to the stored upstream password on save. */
export type PasswordMode = 'keep' | 'replace' | 'clear'

export interface ProxyForm {
  upstreamUrl: string
  upstreamUsername: string
  passwordMode: PasswordMode
  /** Write-only; only read while `passwordMode` is 'replace'. */
  upstreamPassword: string
  /** Seconds as typed; '' is the server default. */
  cacheTtl: string
  negativeCacheTtl: string
  /** Seconds as typed; '' is the server default, an explicit 0 never serves a stale copy. */
  maxStale: string
  /** Hours as typed; '' is off. */
  minAgeHours: string
  blocked: boolean
}

export type ProxyField =
  | 'upstream_url'
  | 'upstream_username'
  | 'upstream_password'
  | 'cache_ttl_seconds'
  | 'negative_cache_ttl_seconds'
  | 'max_stale_seconds'
  | 'min_age_hours'
  | 'blocked'

export type ProxyErrors = Partial<Record<ProxyField, string>>

export function proxyFormFrom(repo: Repository): ProxyForm {
  const num = (v: number | undefined): string => (v == null ? '' : String(v))
  return {
    upstreamUrl: repo.upstream_url ?? '',
    upstreamUsername: repo.upstream_username ?? '',
    passwordMode: 'keep',
    upstreamPassword: '',
    cacheTtl: num(repo.cache_ttl_seconds),
    negativeCacheTtl: num(repo.negative_cache_ttl_seconds),
    maxStale: num(repo.max_stale_seconds),
    minAgeHours: repo.min_age_hours ? String(repo.min_age_hours) : '',
    blocked: repo.blocked ?? false,
  }
}

/** The whole number a field holds; undefined for '' and null for something that is not a non-negative integer. */
function wholeNumber(raw: string): number | null | undefined {
  const s = raw.trim()
  if (s === '') return undefined
  if (!/^\d+$/.test(s)) return null
  const n = Number(s)
  return Number.isSafeInteger(n) ? n : null
}

/** The number of seconds a field means once the server default is applied for an empty value and a 0 TTL. */
export function effectiveSeconds(raw: string, field: 'cacheTtl' | 'negativeCacheTtl' | 'maxStale'): number | null {
  const n = wholeNumber(raw)
  if (n === null) return null
  const limits = PROXY_LIMITS[field]
  if (n === undefined) return limits.def
  if (n === 0 && field !== 'maxStale') return limits.def
  return n
}

function secondsError(raw: string, field: 'cacheTtl' | 'negativeCacheTtl' | 'maxStale'): string {
  const n = wholeNumber(raw)
  if (n === undefined) return ''
  const max = PROXY_LIMITS[field].max
  if (n === null) return t('repoDetail.proxy.error.whole')
  if (n > max) return tp('repoDetail.proxy.error.atMost', { max: formatNumber(max), duration: formatDuration(max, { maxUnits: 1 }) })
  return ''
}

/** Why the upstream URL is unacceptable ('' when it is fine). The server also refuses private and metadata hosts. */
export function upstreamUrlError(raw: string): string {
  const s = raw.trim()
  if (s === '') return t('repoDetail.proxy.error.urlRequired')
  let u: URL
  try {
    u = new URL(s)
  } catch {
    return t('repoDetail.proxy.error.urlInvalid')
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return t('repoDetail.proxy.error.urlScheme')
  if (u.username !== '' || u.password !== '' || u.search !== '' || u.hash !== '' || /[?#]/.test(s)) return t('repoDetail.proxy.error.urlShape')
  return ''
}

/** Client-side errors per field; empty when the form can be sent. */
export function validateProxyForm(form: ProxyForm): ProxyErrors {
  const errors: ProxyErrors = {}
  const url = upstreamUrlError(form.upstreamUrl)
  if (url) errors.upstream_url = url
  const cache = secondsError(form.cacheTtl, 'cacheTtl')
  if (cache) errors.cache_ttl_seconds = cache
  const negative = secondsError(form.negativeCacheTtl, 'negativeCacheTtl')
  if (negative) errors.negative_cache_ttl_seconds = negative
  const stale = secondsError(form.maxStale, 'maxStale')
  if (stale) errors.max_stale_seconds = stale
  const minAge = wholeNumber(form.minAgeHours)
  if (minAge === null) errors.min_age_hours = t('repoDetail.proxy.error.whole')
  else if (minAge !== undefined && minAge > PROXY_LIMITS.minAgeHours.max) {
    errors.min_age_hours = tp('repoDetail.proxy.error.hoursAtMost', { max: formatNumber(PROXY_LIMITS.minAgeHours.max) })
  }
  if (form.passwordMode === 'replace' && form.upstreamPassword === '') errors.upstream_password = t('repoDetail.proxy.error.passwordEmpty')
  return errors
}

function originOf(raw: string): string {
  try {
    const u = new URL(raw.trim())
    return `${u.protocol}//${u.hostname}:${u.port || (u.protocol === 'https:' ? '443' : '80')}`.toLowerCase()
  } catch {
    return ''
  }
}

/**
 * True when saving would make the server drop the stored password: a password is stored, the form
 * keeps it, and the upstream origin or the username changes (the server never sends it to a new target).
 */
export function dropsStoredPassword(form: ProxyForm, repo: Repository): boolean {
  if (!repo.upstream_password_set || form.passwordMode !== 'keep') return false
  return originOf(form.upstreamUrl) !== originOf(repo.upstream_url ?? '') || form.upstreamUsername !== (repo.upstream_username ?? '')
}

export type ProxyPatch = Partial<Pick<Repository, 'upstream_url' | 'upstream_username' | 'cache_ttl_seconds' | 'negative_cache_ttl_seconds' | 'max_stale_seconds' | 'min_age_hours' | 'blocked'>> & {
  upstream_password?: string
}

/**
 * Body of the PATCH: only the fields that differ from the repository, so an untouched field is never
 * rewritten (and a maintainer who does not touch `blocked` never sends it). An empty TTL is sent as the
 * server default, an empty minimum age as 0; `blocked` only when `canBlock`.
 */
export function buildProxyPatch(form: ProxyForm, repo: Repository, canBlock: boolean): ProxyPatch {
  const patch: ProxyPatch = {}
  const url = form.upstreamUrl.trim()
  if (url !== (repo.upstream_url ?? '')) patch.upstream_url = url
  if (form.upstreamUsername !== (repo.upstream_username ?? '')) patch.upstream_username = form.upstreamUsername

  const cache = effectiveSeconds(form.cacheTtl, 'cacheTtl') ?? 0
  if (cache !== (repo.cache_ttl_seconds ?? PROXY_LIMITS.cacheTtl.def)) patch.cache_ttl_seconds = cache
  const negative = effectiveSeconds(form.negativeCacheTtl, 'negativeCacheTtl') ?? 0
  if (negative !== (repo.negative_cache_ttl_seconds ?? PROXY_LIMITS.negativeCacheTtl.def)) patch.negative_cache_ttl_seconds = negative
  const stale = effectiveSeconds(form.maxStale, 'maxStale') ?? 0
  if (stale !== (repo.max_stale_seconds ?? PROXY_LIMITS.maxStale.def)) patch.max_stale_seconds = stale
  const minAge = wholeNumber(form.minAgeHours) ?? 0
  if (minAge !== (repo.min_age_hours ?? 0)) patch.min_age_hours = minAge

  if (canBlock && form.blocked !== (repo.blocked ?? false)) patch.blocked = form.blocked

  if (form.passwordMode === 'replace') patch.upstream_password = form.upstreamPassword
  else if (form.passwordMode === 'clear' && repo.upstream_password_set) patch.upstream_password = ''
  return patch
}

const FIELDS: readonly ProxyField[] = [
  'upstream_url',
  'upstream_username',
  'upstream_password',
  'cache_ttl_seconds',
  'negative_cache_ttl_seconds',
  'max_stale_seconds',
  'min_age_hours',
  'blocked',
]

/**
 * The field a server validation message names ("min_age_hours must be between ..."): the API prefixes its
 * messages with the JSON field, so the leading word decides; undefined for a message about the request as a whole.
 */
export function fieldOfServerError(message: string): ProxyField | undefined {
  const word = /^[a-z_]+/.exec(message)?.[0]
  return FIELDS.find((f) => f === word)
}

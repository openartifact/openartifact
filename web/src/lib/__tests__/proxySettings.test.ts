// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { Repository } from '../../api/repositories'
import { buildProxyPatch, dropsStoredPassword, effectiveSeconds, fieldOfServerError, proxyFormFrom, validateProxyForm } from '../proxySettings'

const repo = (over: Partial<Repository> = {}): Repository =>
  ({
    id: 'r1',
    name: 'npm-proxy',
    format: 'npm',
    repo_type: 'proxy',
    upstream_url: 'https://registry.npmjs.org',
    upstream_username: '',
    upstream_password_set: false,
    cache_ttl_seconds: 3600,
    negative_cache_ttl_seconds: 60,
    max_stale_seconds: 604800,
    min_age_hours: 0,
    blocked: false,
    ...over,
  }) as Repository

describe('validateProxyForm', () => {
  it('accepts the form seeded from a repository', () => {
    expect(validateProxyForm(proxyFormFrom(repo()))).toEqual({})
  })

  it.each([
    ['', 'upstream_url'],
    ['not a url', 'upstream_url'],
    ['ftp://mirror.example.org', 'upstream_url'],
    ['https://user:pw@registry.example.org', 'upstream_url'],
    ['https://registry.example.org/?x=1', 'upstream_url'],
    ['https://registry.example.org/#frag', 'upstream_url'],
  ])('rejects the upstream URL %j', (url) => {
    expect(validateProxyForm({ ...proxyFormFrom(repo()), upstreamUrl: url }).upstream_url).toBeTruthy()
  })

  it('keeps the numeric ranges of the server', () => {
    const form = proxyFormFrom(repo())
    expect(validateProxyForm({ ...form, cacheTtl: String(30 * 24 * 3600) })).toEqual({})
    expect(validateProxyForm({ ...form, cacheTtl: String(30 * 24 * 3600 + 1) }).cache_ttl_seconds).toBeTruthy()
    expect(validateProxyForm({ ...form, negativeCacheTtl: '86401' }).negative_cache_ttl_seconds).toBeTruthy()
    expect(validateProxyForm({ ...form, maxStale: String(365 * 24 * 3600) })).toEqual({})
    expect(validateProxyForm({ ...form, maxStale: String(365 * 24 * 3600 + 1) }).max_stale_seconds).toBeTruthy()
    expect(validateProxyForm({ ...form, minAgeHours: '87600' })).toEqual({})
    expect(validateProxyForm({ ...form, minAgeHours: '87601' }).min_age_hours).toBeTruthy()
  })

  it.each(['-1', '1.5', '1e3', 'abc', '12 h'])('rejects %j as a number of seconds or hours', (raw) => {
    const errors = validateProxyForm({ ...proxyFormFrom(repo()), cacheTtl: raw, negativeCacheTtl: raw, maxStale: raw, minAgeHours: raw })
    expect(Object.keys(errors).sort()).toEqual(['cache_ttl_seconds', 'max_stale_seconds', 'min_age_hours', 'negative_cache_ttl_seconds'])
  })

  it('treats empty as "default" for the numbers and an explicit 0 as a value for max stale', () => {
    const form = { ...proxyFormFrom(repo()), cacheTtl: '', negativeCacheTtl: '', maxStale: '', minAgeHours: '' }
    expect(validateProxyForm(form)).toEqual({})
    expect(effectiveSeconds('', 'maxStale')).toBe(604800)
    expect(effectiveSeconds('0', 'maxStale')).toBe(0)
    expect(effectiveSeconds('0', 'cacheTtl')).toBe(3600)
  })

  it('asks for the new password only when replacing', () => {
    const form = proxyFormFrom(repo({ upstream_password_set: true }))
    expect(validateProxyForm({ ...form, passwordMode: 'replace' }).upstream_password).toBeTruthy()
    expect(validateProxyForm({ ...form, passwordMode: 'replace', upstreamPassword: 'x' })).toEqual({})
    expect(validateProxyForm({ ...form, passwordMode: 'clear' })).toEqual({})
  })
})

describe('buildProxyPatch', () => {
  it('is empty for an untouched form', () => {
    expect(buildProxyPatch(proxyFormFrom(repo()), repo(), true)).toEqual({})
  })

  it('sends only what changed, with the URL trimmed', () => {
    const form = { ...proxyFormFrom(repo()), upstreamUrl: '  https://mirror.example.org  ', cacheTtl: '120', minAgeHours: '48' }
    expect(buildProxyPatch(form, repo(), true)).toEqual({ upstream_url: 'https://mirror.example.org', cache_ttl_seconds: 120, min_age_hours: 48 })
  })

  it('sends an emptied TTL as 0 (server default) and an emptied max stale as the default, but an explicit 0 as 0', () => {
    const stored = repo({ cache_ttl_seconds: 120, negative_cache_ttl_seconds: 5, max_stale_seconds: 99 })
    const form = { ...proxyFormFrom(stored), cacheTtl: '', negativeCacheTtl: '', maxStale: '' }
    expect(buildProxyPatch(form, stored, true)).toEqual({ cache_ttl_seconds: 3600, negative_cache_ttl_seconds: 60, max_stale_seconds: 604800 })
    expect(buildProxyPatch({ ...form, maxStale: '0' }, stored, true).max_stale_seconds).toBe(0)
  })

  it('sends blocked only for a caller who may block, and only when it changed', () => {
    const form = { ...proxyFormFrom(repo()), blocked: true }
    expect(buildProxyPatch(form, repo(), true)).toEqual({ blocked: true })
    expect(buildProxyPatch(form, repo(), false)).toEqual({})
  })

  it('maps the password choice: keep sends nothing, replace sends it, remove sends an empty string', () => {
    const stored = repo({ upstream_password_set: true })
    const form = proxyFormFrom(stored)
    expect('upstream_password' in buildProxyPatch(form, stored, true)).toBe(false)
    expect(buildProxyPatch({ ...form, passwordMode: 'replace', upstreamPassword: 's3cret' }, stored, true)).toEqual({ upstream_password: 's3cret' })
    expect(buildProxyPatch({ ...form, passwordMode: 'clear' }, stored, true)).toEqual({ upstream_password: '' })
  })

  it('does not send a removal when nothing is stored', () => {
    expect(buildProxyPatch({ ...proxyFormFrom(repo()), passwordMode: 'clear' }, repo(), true)).toEqual({})
  })
})

describe('dropsStoredPassword', () => {
  const stored = repo({ upstream_password_set: true, upstream_username: 'svc' })

  it('warns when a kept password would go to another host, port, scheme or user', () => {
    const form = proxyFormFrom(stored)
    expect(dropsStoredPassword(form, stored)).toBe(false)
    expect(dropsStoredPassword({ ...form, upstreamUrl: 'https://registry.npmjs.org/other-path' }, stored)).toBe(false)
    expect(dropsStoredPassword({ ...form, upstreamUrl: 'https://mirror.example.org' }, stored)).toBe(true)
    expect(dropsStoredPassword({ ...form, upstreamUrl: 'http://registry.npmjs.org' }, stored)).toBe(true)
    expect(dropsStoredPassword({ ...form, upstreamUrl: 'https://registry.npmjs.org:8443' }, stored)).toBe(true)
    expect(dropsStoredPassword({ ...form, upstreamUsername: 'other' }, stored)).toBe(true)
  })

  it('does not warn when re-entering or removing the password, or when none is stored', () => {
    const form = { ...proxyFormFrom(stored), upstreamUrl: 'https://mirror.example.org' }
    expect(dropsStoredPassword({ ...form, passwordMode: 'replace' }, stored)).toBe(false)
    expect(dropsStoredPassword({ ...form, passwordMode: 'clear' }, stored)).toBe(false)
    expect(dropsStoredPassword(form, repo())).toBe(false)
  })
})

describe('fieldOfServerError', () => {
  it.each([
    ['upstream_url is not allowed: private address', 'upstream_url'],
    ['upstream_url cannot be empty for a proxy repository', 'upstream_url'],
    ['cache_ttl_seconds must not be negative', 'cache_ttl_seconds'],
    ['max_stale_seconds must not be negative', 'max_stale_seconds'],
    ['min_age_hours must be between 0 and 87600', 'min_age_hours'],
    ['upstream_password must be the plaintext password', 'upstream_password'],
  ])('reads the field from %j', (message, field) => {
    expect(fieldOfServerError(message)).toBe(field)
  })

  it('leaves a message about the request as a whole without a field', () => {
    expect(fieldOfServerError('only an administrator can block a repository')).toBeUndefined()
    expect(fieldOfServerError('HTTP 500')).toBeUndefined()
  })
})

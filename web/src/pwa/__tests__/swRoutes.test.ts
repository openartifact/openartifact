// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { classifyRequest, isBypassPath, type RequestInfo } from '../swRoutes'

const ORIGIN = 'https://oa.example.com'
const PRECACHED = new Set(['/assets/index-AbCd1234.js', '/offline.html', '/icons/icon-192.png'])

function req(path: string, over: Partial<RequestInfo> = {}): RequestInfo {
  return { method: 'GET', url: ORIGIN + path, mode: 'cors', accept: '*/*', ...over }
}

function nav(path: string, over: Partial<RequestInfo> = {}): RequestInfo {
  return req(path, { mode: 'navigate', accept: 'text/html,application/xhtml+xml', ...over })
}

describe('isBypassPath', () => {
  it.each([
    '/api/v1/auth/me',
    '/api/v1/auth/sso/oidc/corp/callback',
    '/api',
    '/repos/npm-local/lodash/-/lodash-1.0.0.tgz',
    '/repos/demo/packages',
    '/v2/',
    '/v2/repo/app/manifests/1',
    '/metrics',
    '/health',
    '/ready',
    '/mcp',
    '/auth/callback',
    '/sso/start',
    '/manifest.webmanifest',
  ])('%s is never handled by the worker', (p) => {
    expect(isBypassPath(p)).toBe(true)
  })

  it.each(['/', '/login', '/search', '/projects/demo', '/admin/users', '/apiary', '/reposition', '/v22', '/healthy', '/manifest.webmanifest.bak', '/assets/x.js'])(
    '%s is not excluded',
    (p) => {
      expect(isBypassPath(p)).toBe(false)
    },
  )
})

describe('classifyRequest', () => {
  it('serves precached files from the precache', () => {
    expect(classifyRequest(req('/assets/index-AbCd1234.js'), ORIGIN, PRECACHED)).toBe('precache')
    expect(classifyRequest(req('/offline.html'), ORIGIN, PRECACHED)).toBe('precache')
    expect(classifyRequest(req('/icons/icon-192.png', { mode: 'no-cors' }), ORIGIN, PRECACHED)).toBe('precache')
  })

  it('does not match a precached path carrying a query string', () => {
    expect(classifyRequest(req('/assets/index-AbCd1234.js?x=1'), ORIGIN, PRECACHED)).toBe('bypass')
  })

  it('never answers the dynamic manifest, even if it were precached', () => {
    const withManifest = new Set([...PRECACHED, '/manifest.webmanifest'])
    expect(classifyRequest(req('/manifest.webmanifest'), ORIGIN, withManifest)).toBe('bypass')
    expect(classifyRequest(req('/manifest.webmanifest'), ORIGIN, PRECACHED)).toBe('bypass')
  })

  it('falls back to the offline page for SPA navigations', () => {
    for (const p of ['/', '/login', '/search', '/projects/demo', '/admin/system', '/settings/profile']) {
      expect(classifyRequest(nav(p), ORIGIN, PRECACHED)).toBe('navigate')
    }
  })

  it('leaves API, repository, registry and operational traffic to the network', () => {
    for (const p of ['/api/v1/auth/me', '/api/v1/repos', '/repos/demo/packages', '/v2/', '/metrics', '/health']) {
      expect(classifyRequest(req(p), ORIGIN, PRECACHED)).toBe('bypass')
    }
  })

  it('never offers the offline page for navigations under excluded prefixes', () => {
    for (const p of ['/repos/demo/packages', '/repos/demo/browse', '/api/v1/auth/sso/oidc/x/login', '/health', '/v2/']) {
      expect(classifyRequest(nav(p), ORIGIN, PRECACHED)).toBe('bypass')
    }
  })

  it('lets an excluded prefix win over the precache list', () => {
    const listed = new Set(['/api/v1/auth/me', '/repos/x/y'])
    expect(classifyRequest(req('/api/v1/auth/me'), ORIGIN, listed)).toBe('bypass')
    expect(classifyRequest(req('/repos/x/y'), ORIGIN, listed)).toBe('bypass')
  })

  it('ignores everything but GET', () => {
    for (const method of ['POST', 'PUT', 'DELETE', 'PATCH', 'HEAD']) {
      expect(classifyRequest(nav('/', { method }), ORIGIN, PRECACHED)).toBe('bypass')
      expect(classifyRequest(req('/offline.html', { method }), ORIGIN, PRECACHED)).toBe('bypass')
    }
  })

  it('ignores cross-origin requests and invalid URLs', () => {
    expect(classifyRequest({ ...req('/'), url: 'https://cdn.example.net/assets/index-AbCd1234.js' }, ORIGIN, PRECACHED)).toBe('bypass')
    expect(classifyRequest({ ...req('/'), url: 'not a url' }, ORIGIN, PRECACHED)).toBe('bypass')
  })

  it('only answers navigations that ask for HTML', () => {
    expect(classifyRequest(nav('/search', { accept: 'application/json' }), ORIGIN, PRECACHED)).toBe('bypass')
    expect(classifyRequest(nav('/search', { accept: null }), ORIGIN, PRECACHED)).toBe('bypass')
    expect(classifyRequest(req('/search', { accept: 'text/html' }), ORIGIN, PRECACHED)).toBe('bypass')
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { existsSync } from 'node:fs'
import { resolve } from 'node:path'
import router from '../../router'
import { ROUTE_DOCS, docsUrlFor } from '../docs'

const DOCS_ROOT = resolve(import.meta.dir, '../../../../docs')
const BASE = 'https://openartifact.github.io/latest/'

describe('ROUTE_DOCS', () => {
  it('has an entry (a page or an explicit null) for every named route', () => {
    const names = router.getRoutes().map((r) => r.name).filter((n): n is string => typeof n === 'string')
    expect(names.length).toBeGreaterThan(30)
    const missing = names.filter((n) => !Object.hasOwn(ROUTE_DOCS, n))
    expect(missing).toEqual([])
  })

  it('has no entry for a route that does not exist', () => {
    const names = new Set(router.getRoutes().map((r) => r.name))
    expect(Object.keys(ROUTE_DOCS).filter((n) => !names.has(n))).toEqual([])
  })

  it('maps only to pages that exist under docs/', () => {
    const paths = new Set(Object.values(ROUTE_DOCS).filter((p): p is string => p !== null))
    expect(paths.size).toBeGreaterThan(10)
    for (const path of paths) {
      expect(path.startsWith('/')).toBe(false)
      expect(path.endsWith('.md')).toBe(false)
      const file = path === '' || path.endsWith('/') ? resolve(DOCS_ROOT, path, 'README.md') : resolve(DOCS_ROOT, `${path}.md`)
      expect({ path, exists: existsSync(file) }).toEqual({ path, exists: true })
    }
  })

  it('never links into the generated reference, which the site renders differently', () => {
    for (const path of Object.values(ROUTE_DOCS)) {
      expect(path?.startsWith('reference/') ?? false).toBe(false)
    }
  })
})

describe('docsUrlFor', () => {
  it('joins the docs base and the page path', () => {
    expect(docsUrlFor(BASE, 'admin-sso')).toBe(`${BASE}security/sso`)
    expect(docsUrlFor(BASE, 'repo-browse')).toBe(`${BASE}formats/`)
  })

  it('tolerates a base without a trailing slash', () => {
    expect(docsUrlFor('https://docs.example.com/v1.2', 'admin-sso')).toBe('https://docs.example.com/v1.2/security/sso')
  })

  it('is null when the base is empty or missing (docs links disabled)', () => {
    expect(docsUrlFor('', 'admin-sso')).toBeNull()
    expect(docsUrlFor(null, 'admin-sso')).toBeNull()
    expect(docsUrlFor(undefined, 'admin-sso')).toBeNull()
  })

  it('is null for routes mapped to null, unknown or unnamed routes', () => {
    expect(docsUrlFor(BASE, 'dashboard')).toBeNull()
    expect(docsUrlFor(BASE, 'no-such-route')).toBeNull()
    expect(docsUrlFor(BASE, 'toString')).toBeNull()
    expect(docsUrlFor(BASE, undefined)).toBeNull()
    expect(docsUrlFor(BASE, Symbol('x'))).toBeNull()
  })
})

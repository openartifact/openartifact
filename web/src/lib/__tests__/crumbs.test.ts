// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import router from '../../router'
import { resolveCrumbs } from '../crumbs'

// router.resolve() matches records without running guards or loading pages, so
// the declared trails can be checked against real URLs.
function trail(path: string) {
  return resolveCrumbs(router.resolve(path))
}

describe('route crumb trails', () => {
  it('has none on the dashboard', () => {
    expect(trail('/')).toEqual([])
  })

  it('lists repositories', () => {
    expect(trail('/repos')).toEqual([{ label: 'Repositories', to: '/repos' }])
    expect(trail('/repos/new').map((c) => c.label)).toEqual(['Repositories', 'New repository'])
  })

  it('resolves the repo name param, encoded in links, raw in labels', () => {
    expect(trail('/repos/my%20repo')).toEqual([
      { label: 'Repositories', to: '/repos' },
      { label: 'my repo', to: '/repos/my%20repo' },
    ])
  })

  it.each([
    ['/repos/demo/edit', ['Repositories', 'demo', 'Edit']],
    ['/repos/demo/browse', ['Repositories', 'demo', 'Browse']],
    ['/repos/demo/graph', ['Repositories', 'demo', 'Dependency Graph']],
    ['/repos/demo/trash', ['Repositories', 'demo', 'Trash']],
    ['/repos/demo/packages/npm/left-pad', ['Repositories', 'demo', 'left-pad']],
    ['/search', ['Search']],
    ['/admin/users', ['Administration', 'Users']],
    ['/admin/orgs', ['Administration', 'Organizations']],
    ['/admin/trash', ['Administration', 'Trash']],
    ['/admin/ai', ['Administration', 'AI settings']],
    ['/security/vulns', ['Security', 'Vulnerability scan']],
    ['/security/ai-analysis', ['Security', 'AI analysis']],
    ['/settings/profile', ['Settings', 'Profile']],
    ['/settings/password', ['Settings', 'Password']],
    ['/settings/tokens', ['Settings', 'Personal access tokens']],
  ])('%s -> %j', (path, labels) => {
    expect(trail(path).map((c) => c.label)).toEqual(labels)
  })

  it('links every crumb except the current page to a real route', () => {
    for (const path of ['/repos/demo/trash', '/admin/system', '/security/secrets', '/settings/tokens']) {
      const crumbs = trail(path)
      for (const c of crumbs.slice(0, -1)) {
        expect(c.to).toBeDefined()
        expect(router.resolve(c.to!).name).not.toBe('not-found')
      }
    }
  })

  it('declares a trail for every page route except the dashboard, login, 404 and redirects', () => {
    const blank = new Set(['dashboard', 'login', 'change-password', 'not-found'])
    const missing = router
      .getRoutes()
      .filter((r) => r.components && r.name && !blank.has(String(r.name)) && !r.children.length)
      .filter((r) => !r.meta.crumbs?.length)
      .map((r) => String(r.name))
    expect(missing).toEqual([])
  })

  it('keeps /repos/:name/settings redirecting to the configuration tab', () => {
    const record = router.getRoutes().find((r) => r.name === 'repo-settings')!
    const redirect = record.redirect as (to: { params: Record<string, string> }) => unknown
    expect(redirect({ params: { name: 'demo' } })).toEqual({
      name: 'repo-detail',
      params: { name: 'demo' },
      query: { tab: 'configuration' },
    })
  })
})

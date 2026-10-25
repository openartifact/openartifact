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
  })

  it('resolves the repo name param, encoded in links, raw in labels', () => {
    expect(trail('/repos/my%20repo')).toEqual([
      { label: 'Repositories', to: '/repos' },
      { label: 'my repo', to: '/repos/my%20repo' },
    ])
  })

  it('lists projects and resolves the project param, encoded in links, raw in labels', () => {
    expect(trail('/projects')).toEqual([{ label: 'Projects', to: '/projects' }])
    expect(trail('/projects/my%20project')).toEqual([
      { label: 'Projects', to: '/projects' },
      { label: 'my project', to: '/projects/my%20project' },
    ])
  })

  it.each([
    ['/repos/demo/packages/left-pad', ['Repositories', 'demo', 'left-pad']],
    ['/repos/demo/packages/@scope/left-pad', ['Repositories', 'demo', '@scope/left-pad']],
    ['/repos/demo/packages/left-pad/versions/1.0.0', ['Repositories', 'demo', 'left-pad']],
    ['/packages', ['Search']],
    ['/security', ['Security']],
    ['/settings', ['Settings']],
    ['/settings/profile', ['Settings', 'Profile']],
    ['/settings/password', ['Settings', 'Password']],
    ['/settings/tokens', ['Settings', 'Access tokens']],
    ['/settings/users', ['Settings', 'Users']],
    ['/settings/health', ['Settings', 'System health']],
    ['/settings/dependency-track', ['Settings', 'Dependency-Track']],
  ])('%s -> %j', (path, labels) => {
    expect(trail(path).map((c) => c.label)).toEqual(labels)
  })

  it('links every crumb except the current page to a real route', () => {
    for (const path of ['/repos/demo/packages/left-pad', '/settings/users', '/settings/tokens', '/projects/demo']) {
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
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Every legacy URL must keep working. The redirects are driven through a real
// router built from the same route records as the app, with pages stubbed.
import { describe, it, expect, beforeEach } from 'bun:test'
import { createMemoryHistory, createRouter, type RouteRecordRaw, type Router } from 'vue-router'
import { homeRoutes } from '../routes/home'
import { repoRoutes } from '../routes/repos'
import { packageRoutes } from '../routes/packages'
import { projectRoutes } from '../routes/projects'
import { bundleRoutes } from '../routes/bundles'
import { securityRoutes } from '../routes/security'
import { settingsRoutes } from '../routes/settings'
import { installLegacyArtifactRedirect, legacyRedirects } from '../redirects'
import { artifactPath } from '../../lib/artifactPath'

const stub = { render: () => null }

function stubbed(records: RouteRecordRaw[]): RouteRecordRaw[] {
  return records.map((r) => (r.component ? ({ ...r, component: stub } as RouteRecordRaw) : r))
}

let router: Router

beforeEach(() => {
  router = createRouter({
    history: createMemoryHistory(),
    routes: [
      ...stubbed([...homeRoutes, ...repoRoutes, ...packageRoutes, ...projectRoutes, ...bundleRoutes, ...securityRoutes, ...settingsRoutes]),
      ...legacyRedirects,
      { path: '/:pathMatch(.*)*', name: 'not-found', component: stub },
    ],
  })
  installLegacyArtifactRedirect(router)
})

async function land(path: string): Promise<string> {
  await router.push(path)
  return router.currentRoute.value.fullPath
}

describe('legacy redirects', () => {
  it.each([
    ['/stats', '/'],
    ['/search', '/packages'],
    ['/search?q=left-pad&format=npm', '/packages?q=left-pad&format=npm'],
    ['/profile', '/settings/profile'],
    ['/repos/new', '/repos?new=1'],
    ['/repos/new?project=core', '/repos?project=core&new=1'],
    ['/repos/demo/edit', '/repos/demo?tab=settings'],
    ['/repos/demo/settings', '/repos/demo?tab=settings'],
    ['/repos/demo/browse', '/repos/demo?tab=packages'],
    ['/repos/demo/graph', '/repos/demo?tab=packages'],
    ['/repos/demo/trash', '/repos/demo?tab=trash'],
    ['/security/vulns', '/security?type=vuln'],
    ['/security/licenses', '/security?type=license'],
    ['/security/secrets', '/security?type=secret'],
    ['/security/ai-analysis', '/security?type=ai'],
    ['/security/vulns?repo=maven-dev', '/security?repo=maven-dev&type=vuln'],
    ['/admin', '/settings'],
    ['/admin/users', '/settings/users'],
    ['/admin/groups', '/settings/groups'],
    ['/admin/permissions', '/settings/permissions'],
    ['/admin/sso', '/settings/sso'],
    ['/admin/webhooks', '/settings/webhooks'],
    ['/admin/backup', '/settings/backup'],
    ['/admin/ai', '/settings/ai'],
    ['/admin/branding', '/settings/branding'],
    ['/admin/audit', '/settings/audit'],
    ['/admin/audit?actor=bob', '/settings/audit?actor=bob'],
    ['/admin/dependency-track', '/settings/dependency-track'],
    ['/admin/trash', '/settings/trash'],
    ['/admin/system', '/settings/health'],
    ['/admin/gpg', '/settings/signing'],
    ['/admin/storage', '/settings/backends'],
    ['/admin/storage/tiers', '/settings/tiers'],
    ['/admin/tokens', '/settings/tokens?type=all'],
    ['/admin/projects', '/projects?scope=all'],
  ])('%s -> %s', async (from, to) => {
    expect(await land(from)).toBe(to)
  })

  it('keeps the hash', async () => {
    expect(await land('/admin/users#row-3')).toBe('/settings/users#row-3')
  })

  it('re-encodes repository names with special characters', async () => {
    expect(await land('/repos/my%20repo/edit')).toBe('/repos/my%20repo?tab=settings')
    expect(await land('/repos/my%20repo/trash?x=1')).toBe('/repos/my%20repo?x=1&tab=trash')
  })

  it('has no redirect that loops or lands on the catch-all', async () => {
    for (const record of legacyRedirects) {
      const path = record.path.replace(/:\w+/g, 'demo')
      await router.push(path)
      expect(router.currentRoute.value.name).not.toBe('not-found')
      expect(router.currentRoute.value.path).not.toBe(path)
    }
  })
})

describe('legacy artifact URLs', () => {
  it('drops the format segment of /repos/:n/packages/:format/:pkg', async () => {
    expect(await land('/repos/demo/packages/npm/left-pad')).toBe('/repos/demo/packages/left-pad')
  })

  it('decodes the single encoded package segment and re-emits raw slashes', async () => {
    expect(await land('/repos/demo/packages/npm/%40scope%2Fleft-pad')).toBe('/repos/demo/packages/@scope/left-pad')
    expect(await land('/repos/demo/packages/docker/team%2Fapp')).toBe('/repos/demo/packages/team/app')
  })

  it('maps ?version= into /versions/:v and keeps ?tab=', async () => {
    expect(await land('/repos/demo/packages/maven/com.acme%3Alib?version=1.2.3&tab=security')).toBe(
      '/repos/demo/packages/com.acme%3Alib/versions/1.2.3?tab=security',
    )
  })

  it('moves a stray ?version= into the path of a new-style URL', async () => {
    expect(await land('/repos/demo/packages/left-pad?version=2.0.0')).toBe('/repos/demo/packages/left-pad/versions/2.0.0')
  })

  it('leaves new URLs alone, including names with slashes and versions', async () => {
    expect(await land('/repos/demo/packages/left-pad')).toBe('/repos/demo/packages/left-pad')
    expect(await land('/repos/demo/packages/@scope/left-pad')).toBe('/repos/demo/packages/@scope/left-pad')
    expect(await land('/repos/demo/packages/left-pad/versions/1.0.0?tab=files')).toBe('/repos/demo/packages/left-pad/versions/1.0.0?tab=files')
  })

  it('matches the versions route for names with slashes', async () => {
    await router.push('/repos/demo/packages/@scope/left-pad/versions/1.0.0')
    const route = router.currentRoute.value
    expect(route.name).toBe('artifact-version')
    expect(route.params.pkg).toBe('@scope/left-pad')
    expect(route.params.v).toBe('1.0.0')
  })

  it('round-trips artifactPath through the router', async () => {
    const path = artifactPath('my repo', '@scope/left pad', '1.0.0+build/2')
    await router.push(path)
    const route = router.currentRoute.value
    expect(route.name).toBe('artifact-version')
    expect(route.params.repo).toBe('my repo')
    expect(route.params.pkg).toBe('@scope/left pad')
    expect(route.params.v).toBe('1.0.0+build/2')
  })
})

describe('new routes', () => {
  it('resolves the settings hub and sections', async () => {
    await router.push('/settings')
    expect(router.currentRoute.value.name).toBe('settings-hub')
    await router.push('/settings/quotas')
    expect(router.currentRoute.value.name).toBe('settings-section')
    expect(router.currentRoute.value.params.section).toBe('quotas')
  })

  it('keeps ?tab= and ?new= on their pages', async () => {
    expect(await land('/repos/demo?tab=activity')).toBe('/repos/demo?tab=activity')
    expect(await land('/repos?new=1')).toBe('/repos?new=1')
    expect(await land('/packages?q=x&mode=checksum')).toBe('/packages?q=x&mode=checksum')
  })
})

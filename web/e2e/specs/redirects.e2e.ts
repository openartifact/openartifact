// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { testAs } from '../lib/browser'
import { expectLoc, shellReady, t } from '../lib/ui'

// Old URLs keep working (web/src/router/redirects.ts). The table is a sample
// with one entry per redirect family, including the query/hash carry-over.
const LEGACY: ReadonlyArray<readonly [from: string, to: string]> = [
  ['/stats', '/'],
  ['/search?q=left-pad', '/packages?q=left-pad'],
  ['/profile', '/settings/profile'],
  ['/repos/new', '/repos?new=1'],
  ['/repos/seed-raw/settings', '/repos/seed-raw?tab=settings'],
  ['/repos/seed-raw/edit', '/repos/seed-raw?tab=settings'],
  ['/repos/seed-raw/browse', '/repos/seed-raw?tab=packages'],
  ['/repos/seed-raw/graph', '/repos/seed-raw?tab=packages'],
  ['/repos/seed-raw/trash', '/repos/seed-raw?tab=trash'],
  ['/security/vulns', '/security?type=vuln'],
  ['/security/licenses', '/security?type=license'],
  ['/security/secrets', '/security?type=secret'],
  ['/security/ai-analysis', '/security?type=ai'],
  ['/admin', '/settings'],
  ['/admin/users', '/settings/users'],
  ['/admin/system', '/settings/health'],
  ['/admin/gpg', '/settings/signing'],
  ['/admin/storage', '/settings/backends'],
  ['/admin/storage/tiers', '/settings/tiers'],
  ['/admin/tokens', '/settings/tokens?type=all'],
  ['/admin/projects', '/projects?scope=all'],
  // The query string is kept next to the remapped key.
  ['/admin/users?q=dev', '/settings/users?q=dev'],
  // Old artifact URLs: format segment in the path, version in the query.
  ['/repos/seed-npm/packages/npm/left-pad', '/repos/seed-npm/packages/left-pad'],
  ['/repos/seed-npm/packages/npm/left-pad?version=1.3.0', '/repos/seed-npm/packages/left-pad/versions/1.3.0'],
  ['/repos/seed-npm/packages/npm/%40scope%2Fname?version=2.0.0', '/repos/seed-npm/packages/@scope/name/versions/2.0.0'],
  ['/repos/seed-npm/packages/left-pad?version=1.3.0', '/repos/seed-npm/packages/left-pad/versions/1.3.0'],
]

describe('legacy URLs', () => {
  const it = testAs('admin')

  for (const [from, to] of LEGACY) {
    it(`${from} lands on ${to}`, async (ui) => {
      await ui.goto(from)
      await shellReady(ui)
      await ui.expectUrl((u) => decodeURIComponent(u.pathname) + u.search === decodeURIComponent(to))
      // The destination is a real page, not the not-found screen.
      await expectLoc(ui.getByText(t('shell.notFoundTitle'))).toHaveCount(0)
    })
  }

  it('the hash survives the redirect', async (ui) => {
    await ui.goto('/admin/users#top')
    await shellReady(ui)
    await ui.expectUrl((u) => u.hash === '#top')
    expect(ui.url().pathname).toBe('/settings/users')
  })

  it('/repos/new opens the create drawer', async (ui) => {
    await ui.goto('/repos/new')
    await expectLoc(ui.getByRole('dialog', { name: t('rl.create.title') })).toBeVisible()
  })

  it('an unknown path is the not-found page', async (ui) => {
    await ui.goto('/no/such/page')
    await expectLoc(ui.getByText(t('shell.notFoundTitle'))).toBeVisible()
  })
})

describe('legacy URLs of a developer', () => {
  const it = testAs('dev')

  it('an admin section keeps being refused after the redirect', async (ui) => {
    await ui.goto('/admin/users')
    await expectLoc(ui.getByText(t('shell.denied.admin'))).toBeVisible()
    await ui.expectUrl((u) => u.pathname === '/')
  })
})

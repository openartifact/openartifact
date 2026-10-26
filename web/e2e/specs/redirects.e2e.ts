// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { expect, test } from '../fixtures'
import { shellReady, t } from '../lib/ui'

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

test.describe('legacy URLs', () => {
  test.use({ role: 'admin' })

  for (const [from, to] of LEGACY) {
    test(`${from} lands on ${to}`, async ({ page }) => {
      await page.goto(from)
      await shellReady(page)
      await expect
        .poll(() => {
          const u = new URL(page.url())
          return decodeURIComponent(u.pathname) + u.search
        })
        .toBe(decodeURIComponent(to))
      // The destination is a real page, not the not-found screen.
      await expect(page.getByText(t('shell.notFoundTitle'))).toHaveCount(0)
    })
  }

  test('the hash survives the redirect', async ({ page }) => {
    await page.goto('/admin/users#top')
    await shellReady(page)
    await expect.poll(() => new URL(page.url()).hash).toBe('#top')
    expect(new URL(page.url()).pathname).toBe('/settings/users')
  })

  test('/repos/new opens the create drawer', async ({ page }) => {
    await page.goto('/repos/new')
    await expect(page.getByRole('dialog', { name: t('rl.create.title') })).toBeVisible()
  })

  test('an unknown path is the not-found page', async ({ page }) => {
    await page.goto('/no/such/page')
    await expect(page.getByText(t('shell.notFoundTitle'))).toBeVisible()
  })
})

test.describe('legacy URLs of a developer', () => {
  test.use({ role: 'dev' })

  test('an admin section keeps being refused after the redirect', async ({ page }) => {
    await page.goto('/admin/users')
    await expect(page.getByText(t('shell.denied.admin'))).toBeVisible()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/')
  })
})

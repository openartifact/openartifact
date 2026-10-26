// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { expect, test, type Role } from '../fixtures'
import { seriousViolations } from '../lib/a11y'
import { chooseTheme, shellReady } from '../lib/ui'

/**
 * Axe scan of every page the suite visits, once in the light and once in the
 * dark theme.
 *
 * Lightpanda has no rendering engine, so only the layout-independent rules run
 * (see lib/a11y.ts): names, roles, ARIA validity, landmarks, headings, labels,
 * duplicate ids. Colour contrast of either theme is not checked here. What the
 * dark pass does prove is that the page renders and stays accessible once the
 * theme is applied through the real user-menu toggle.
 */
interface Target {
  name: string
  path: string
  role: Role
  /** Selector that is visible once the page has loaded its content. */
  ready: string
}

const TARGETS: Target[] = [
  { name: 'login', path: '/login', role: 'anon', ready: '#username' },
  { name: 'dashboard', path: '/', role: 'admin', ready: 'h1' },
  { name: 'repositories', path: '/repos', role: 'admin', ready: 'h1' },
  { name: 'repository', path: '/repos/seed-raw', role: 'admin', ready: 'h1' },
  { name: 'repository packages tab', path: '/repos/seed-raw?tab=packages', role: 'admin', ready: 'h1' },
  { name: 'repository security tab', path: '/repos/seed-raw?tab=security', role: 'admin', ready: 'h1' },
  { name: 'artifact', path: '/repos/seed-raw/packages/hello.txt', role: 'admin', ready: 'h1' },
  { name: 'packages search', path: '/packages', role: 'admin', ready: 'h1' },
  { name: 'projects', path: '/projects', role: 'admin', ready: 'h1' },
  { name: 'project', path: '/projects/system', role: 'admin', ready: 'h1' },
  { name: 'bundles', path: '/bundles', role: 'admin', ready: 'h1' },
  { name: 'security', path: '/security', role: 'admin', ready: 'h1' },
  { name: 'settings hub', path: '/settings', role: 'admin', ready: 'h1' },
  { name: 'settings profile', path: '/settings/profile', role: 'admin', ready: 'h1' },
  { name: 'settings users', path: '/settings/users', role: 'admin', ready: 'h1' },
  { name: 'settings tokens', path: '/settings/tokens', role: 'admin', ready: 'h1' },
  { name: 'settings health', path: '/settings/health', role: 'admin', ready: 'h1' },
  { name: 'developer dashboard', path: '/', role: 'dev', ready: 'h1' },
  { name: 'developer repositories', path: '/repos', role: 'dev', ready: 'h1' },
  { name: 'not found', path: '/no-such-page', role: 'admin', ready: 'main h2, main h3' },
]

for (const theme of ['light', 'dark'] as const) {
  test.describe(`axe, ${theme} theme`, () => {
    for (const target of TARGETS) {
      test.describe(target.name, () => {
        test.use({ role: target.role })

        test(`${target.role} ${target.path} has no serious or critical violations`, async ({ page }) => {
          await page.goto(target.path)
          await expect(page.locator(target.ready).first()).toBeVisible()
          if (target.role === 'anon') {
            // No user menu before sign-in: the theme comes from the stored preference, as on a return visit.
            await page.evaluate((mode) => localStorage.setItem('oa.theme', JSON.stringify(mode)), theme)
            await page.reload()
            await expect(page.locator(target.ready).first()).toBeVisible()
          } else {
            await shellReady(page)
            await chooseTheme(page, theme)
          }
          const dark = page.locator('html')
          if (theme === 'dark') await expect(dark).toHaveClass(/(^|\s)dark(\s|$)/)
          // Content that loads after the heading must be in place before the scan.
          await page.waitForLoadState('networkidle')

          const violations = await seriousViolations(page)
          expect(violations, JSON.stringify(violations, null, 2)).toEqual([])
        })
      })
    }
  })
}

test('the scan reports a violation when there is one (guards against a scan that silently runs nothing)', async ({ page }) => {
  await page.goto('/login')
  await expect(page.locator('#username')).toBeVisible()
  await page.evaluate(() => {
    document.body.insertAdjacentHTML('beforeend', '<img src="/x.png"><button type="button"></button>')
  })
  const ids = (await seriousViolations(page)).map((v) => v.id)
  expect(ids).toEqual(expect.arrayContaining(['image-alt', 'button-name']))
})

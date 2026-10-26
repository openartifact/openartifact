// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Page } from '@playwright/test'
import { expect, test } from '../fixtures'
import { breadcrumbs, shellReady, t } from '../lib/ui'

interface Crumb {
  label: string
  href: string | null
  current: boolean
}

/** The trail as the page header renders it: ancestors are links, the page itself is the `aria-current` crumb. */
async function trail(page: Page): Promise<Crumb[]> {
  await expect(breadcrumbs(page)).toBeVisible()
  return breadcrumbs(page).locator('li').evaluateAll((items) =>
    items.map((li) => {
      const link = li.querySelector('a')
      const current = li.querySelector('[aria-current="page"]')
      return {
        label: (link ?? current ?? li).textContent?.trim() ?? '',
        href: link?.getAttribute('href') ?? null,
        current: current !== null,
      }
    }),
  )
}

test.use({ role: 'admin' })

test.describe('top-level pages show only themselves, as the current page', () => {
  for (const path of ['/', '/repos', '/packages', '/projects', '/security', '/settings']) {
    test(path, async ({ page }) => {
      await page.goto(path)
      await shellReady(page)
      const crumbs = await trail(page)
      expect(crumbs).toHaveLength(1)
      expect(crumbs[0]).toMatchObject({ href: null, current: true })
      // The crumb names the page the heading names.
      await expect(page.getByRole('heading', { level: 1 })).toHaveText(crumbs[0]?.label ?? '')
    })
  }
})

test.describe('detail pages link their ancestors', () => {
  test('repository: Repositories / repo', async ({ page }) => {
    await page.goto('/repos/seed-raw')
    expect(await trail(page)).toEqual([
      { label: t('shell.nav.repos'), href: '/repos', current: false },
      { label: 'seed-raw', href: null, current: true },
    ])
  })

  test('artifact: Repositories / repo / package', async ({ page }) => {
    await page.goto('/repos/seed-raw/packages/hello.txt')
    expect(await trail(page)).toEqual([
      { label: t('shell.nav.repos'), href: '/repos', current: false },
      { label: 'seed-raw', href: '/repos/seed-raw', current: false },
      { label: 'hello.txt', href: null, current: true },
    ])
  })

  test('project: Projects / project', async ({ page }) => {
    await page.goto('/projects/system')
    expect(await trail(page)).toEqual([
      { label: t('shell.nav.projects'), href: '/projects', current: false },
      { label: 'system', href: null, current: true },
    ])
  })

  test('settings section: Settings / group / section', async ({ page }) => {
    await page.goto('/settings/profile')
    const crumbs = await trail(page)
    expect(crumbs).toHaveLength(3)
    expect(crumbs[0]).toEqual({ label: t('shell.nav.settings'), href: '/settings', current: false })
    // The group is a label, not a destination.
    expect(crumbs[1]).toMatchObject({ href: null, current: false })
    expect(crumbs[2]).toMatchObject({ href: null, current: true })
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(crumbs[2]?.label ?? '')
  })

  test('an ancestor crumb navigates there', async ({ page }) => {
    await page.goto('/repos/seed-raw/packages/hello.txt')
    await breadcrumbs(page).getByRole('link', { name: 'seed-raw' }).click()
    await expect(page).toHaveURL('/repos/seed-raw')
    await breadcrumbs(page).getByRole('link', { name: t('shell.nav.repos') }).click()
    await expect(page).toHaveURL('/repos')
  })
})

test.describe('as a developer', () => {
  test.use({ role: 'dev' })

  test('the trail is the same', async ({ page }) => {
    await page.goto('/repos/seed-raw/packages/hello.txt')
    const crumbs = await trail(page)
    expect(crumbs.map((c) => c.label)).toEqual([t('shell.nav.repos'), 'seed-raw', 'hello.txt'])
  })
})

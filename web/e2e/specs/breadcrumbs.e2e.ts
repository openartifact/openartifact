// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { testAs } from '../lib/browser'
import { breadcrumbs, expectLoc, shellReady, t, type Ui } from '../lib/ui'

interface Crumb {
  label: string
  href: string | null
}

/**
 * The trail as the page header renders it: the parent pages only. The page
 * itself is the heading, never a crumb, so nothing in the trail is the
 * current page.
 */
async function trail(ui: Ui): Promise<Crumb[]> {
  await expectLoc(breadcrumbs(ui)).toBeVisible()
  const items = await breadcrumbs(ui)
    .locator('li')
    .evaluateAll((lis) =>
      lis.map((li) => {
        const link = li.querySelector('a')
        return {
          label: (link ?? li).textContent?.trim() ?? '',
          href: link?.getAttribute('href') ?? null,
          current: li.querySelector('[aria-current]') !== null,
        }
      }),
    )
  expect(items.filter((c) => c.current)).toEqual([])
  return items.map(({ label, href }) => ({ label, href }))
}

describe('top-level pages have no breadcrumb', () => {
  const it = testAs('admin')

  for (const path of ['/', '/repos', '/packages', '/projects', '/security', '/settings']) {
    it(path, async (ui) => {
      await ui.goto(path)
      await shellReady(ui)
      // The page is rendered (it has its heading) and still offers no trail.
      await expectLoc(ui.getByRole('heading', { level: 1 })).toBeVisible()
      await expectLoc(breadcrumbs(ui)).toHaveCount(0)
    })
  }
})

describe('detail pages link their parents only', () => {
  const it = testAs('admin')

  it('repository: Repositories', async (ui) => {
    await ui.goto('/repos/seed-raw')
    expect(await trail(ui)).toEqual([{ label: t('shell.nav.repos'), href: '/repos' }])
    await expectLoc(ui.getByRole('heading', { level: 1, name: 'seed-raw' })).toBeVisible()
  })

  it('artifact: Repositories / repo', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    expect(await trail(ui)).toEqual([
      { label: t('shell.nav.repos'), href: '/repos' },
      { label: 'seed-raw', href: '/repos/seed-raw' },
    ])
    await expectLoc(ui.getByRole('heading', { level: 1, name: 'hello.txt' })).toBeVisible()
  })

  it('project: Projects', async (ui) => {
    await ui.goto('/projects/system')
    expect(await trail(ui)).toEqual([{ label: t('shell.nav.projects'), href: '/projects' }])
    await expectLoc(ui.getByRole('heading', { level: 1, name: 'system' })).toBeVisible()
  })

  it('settings section: Settings / group', async (ui) => {
    await ui.goto('/settings/profile')
    const crumbs = await trail(ui)
    expect(crumbs).toHaveLength(2)
    expect(crumbs[0]).toEqual({ label: t('shell.nav.settings'), href: '/settings' })
    // The group is a label, not a destination.
    expect(crumbs[1]?.href).toBeNull()
    expect(crumbs[1]?.label).not.toBe('')
    // The section itself is the heading.
    await expectLoc(ui.getByRole('heading', { level: 1 })).toBeVisible()
  })

  it('a parent crumb navigates there', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    await breadcrumbs(ui).getByRole('link', { name: 'seed-raw' }).click()
    await ui.expectUrl('/repos/seed-raw')
    await breadcrumbs(ui).getByRole('link', { name: t('shell.nav.repos') }).click()
    await ui.expectUrl('/repos')
    // Back on a top-level page the trail is gone.
    await expectLoc(breadcrumbs(ui)).toHaveCount(0)
  })
})

describe('as a developer', () => {
  const it = testAs('dev')

  it('the trail is the same', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    const crumbs = await trail(ui)
    expect(crumbs.map((c) => c.label)).toEqual([t('shell.nav.repos'), 'seed-raw'])
  })
})

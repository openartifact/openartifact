// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { testAs } from '../lib/browser'
import { breadcrumbs, expectLoc, shellReady, t, type Ui } from '../lib/ui'

interface Crumb {
  label: string
  href: string | null
  current: boolean
}

/** The trail as the page header renders it: ancestors are links, the page itself is the `aria-current` crumb. */
async function trail(ui: Ui): Promise<Crumb[]> {
  await expectLoc(breadcrumbs(ui)).toBeVisible()
  return breadcrumbs(ui)
    .locator('li')
    .evaluateAll((items) =>
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

describe('top-level pages show only themselves, as the current page', () => {
  const it = testAs('admin')

  for (const path of ['/', '/repos', '/packages', '/projects', '/security', '/settings']) {
    it(path, async (ui) => {
      await ui.goto(path)
      await shellReady(ui)
      const crumbs = await trail(ui)
      expect(crumbs).toHaveLength(1)
      expect(crumbs[0]).toMatchObject({ href: null, current: true })
      // The crumb names the page the heading names.
      await expectLoc(ui.getByRole('heading', { level: 1 })).toHaveText(crumbs[0]?.label ?? '')
    })
  }
})

describe('detail pages link their ancestors', () => {
  const it = testAs('admin')

  it('repository: Repositories / repo', async (ui) => {
    await ui.goto('/repos/seed-raw')
    expect(await trail(ui)).toEqual([
      { label: t('shell.nav.repos'), href: '/repos', current: false },
      { label: 'seed-raw', href: null, current: true },
    ])
  })

  it('artifact: Repositories / repo / package', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    expect(await trail(ui)).toEqual([
      { label: t('shell.nav.repos'), href: '/repos', current: false },
      { label: 'seed-raw', href: '/repos/seed-raw', current: false },
      { label: 'hello.txt', href: null, current: true },
    ])
  })

  it('project: Projects / project', async (ui) => {
    await ui.goto('/projects/system')
    expect(await trail(ui)).toEqual([
      { label: t('shell.nav.projects'), href: '/projects', current: false },
      { label: 'system', href: null, current: true },
    ])
  })

  it('settings section: Settings / group / section', async (ui) => {
    await ui.goto('/settings/profile')
    const crumbs = await trail(ui)
    expect(crumbs).toHaveLength(3)
    expect(crumbs[0]).toEqual({ label: t('shell.nav.settings'), href: '/settings', current: false })
    // The group is a label, not a destination.
    expect(crumbs[1]).toMatchObject({ href: null, current: false })
    expect(crumbs[2]).toMatchObject({ href: null, current: true })
    await expectLoc(ui.getByRole('heading', { level: 1 })).toHaveText(crumbs[2]?.label ?? '')
  })

  it('an ancestor crumb navigates there', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    await breadcrumbs(ui).getByRole('link', { name: 'seed-raw' }).click()
    await ui.expectUrl('/repos/seed-raw')
    await breadcrumbs(ui).getByRole('link', { name: t('shell.nav.repos') }).click()
    await ui.expectUrl('/repos')
  })
})

describe('as a developer', () => {
  const it = testAs('dev')

  it('the trail is the same', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    const crumbs = await trail(ui)
    expect(crumbs.map((c) => c.label)).toEqual([t('shell.nav.repos'), 'seed-raw', 'hello.txt'])
  })
})

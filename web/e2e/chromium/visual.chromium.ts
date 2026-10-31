// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe } from 'bun:test'
import { testAs } from '../lib/browser'
import { expectLoc, shellReady, type Ui } from '../lib/ui'
import { expectMatchesBaseline, freeze, painted, toastAtRest } from '../lib/visual'

/**
 * Pixel snapshots of the app shell in Chromium, in the light and the dark
 * theme: the expanded sidebar, the collapsed rail, a rail tooltip, the header
 * and a toast. Baselines live in `chromium/__screenshots__`; the tolerance is
 * in `lib/visual.ts`. After an intended change of the look:
 * `bun run e2e:visual:update`, then commit the PNGs that changed.
 *
 * The page behind the shell is the not-found page, which shows no data of the
 * seeded server, so a snapshot only changes when the shell does.
 */
const it = testAs('admin', 'chromium')

/** Opens `path` in `theme`, with the rail expanded or collapsed, and freezes animations. */
async function openShell(ui: Ui, path: string, theme: 'light' | 'dark', collapsed: boolean): Promise<void> {
  await ui.goto(path)
  await ui.page.evaluate(
    (mode, rail) => {
      localStorage.setItem('oa.theme', JSON.stringify(mode))
      localStorage.setItem('oa.sidebar.collapsed', JSON.stringify(rail))
    },
    theme,
    collapsed,
  )
  await ui.reload()
  await shellReady(ui)
  await expectLoc(ui.locator('html')).toHaveClass(theme === 'dark' ? /(^|\s)dark(\s|$)/ : /^((?!\bdark\b).)*$/)
  await painted(ui)
  await freeze(ui)
  // Nothing may keep the pointer or focus on a control: both change how it is painted.
  await ui.page.mouse.move(683, 600)
}

for (const theme of ['light', 'dark'] as const) {
  describe(`app shell, ${theme} theme`, () => {
    it('expanded sidebar', async (ui) => {
      await openShell(ui, '/no-such-page', theme, false)
      await expectLoc(ui.getByTestId('nav-repos')).toBeVisible()
      await expectMatchesBaseline(`shell-expanded-${theme}`, await ui.page.screenshot())
    })

    it('collapsed sidebar', async (ui) => {
      await openShell(ui, '/no-such-page', theme, true)
      await expectMatchesBaseline(`shell-collapsed-${theme}`, await ui.page.screenshot())
    })

    it('tooltip of the collapsed rail', async (ui) => {
      await openShell(ui, '/no-such-page', theme, true)
      await ui.page.hover('[data-testid="nav-repos"]')
      await expectLoc(ui.locator('[role="tooltip"]')).toBeVisible()
      await expectMatchesBaseline(`shell-rail-tooltip-${theme}`, await ui.page.screenshot({ clip: { x: 0, y: 0, width: 320, height: 360 } }))
    })

    it('header', async (ui) => {
      await openShell(ui, '/no-such-page', theme, false)
      const header = await ui.page.$('header')
      if (header === null) throw new Error('the page has no <header>')
      await expectMatchesBaseline(`header-${theme}`, await header.screenshot())
    })

    it('toast', async (ui) => {
      await openShell(ui, '/repos/seed-raw/packages/hello.txt', theme, false)
      // A headless page has no clipboard permission; the toast is what is under test, not the clipboard.
      await ui.page.evaluate(() => {
        Object.defineProperty(navigator, 'clipboard', { value: { writeText: () => Promise.resolve() }, configurable: true })
      })
      await ui.getByTestId('artifact-menu').click()
      await ui.getByRole('menuitem', { name: 'Copy link to this version' }).click()
      const toast = await toastAtRest(ui)
      await expectMatchesBaseline(`toast-${theme}`, await toast.screenshot())
    })
  })
}

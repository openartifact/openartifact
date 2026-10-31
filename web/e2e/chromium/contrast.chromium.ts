// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { contrastViolations } from '../lib/a11y'
import { testAs, type Role } from '../lib/browser'
import { freeze, painted, toastAtRest } from '../lib/visual'
import { chooseTheme, expectLoc, shellReady, type Ui } from '../lib/ui'

/**
 * axe-core `color-contrast` (WCAG 1.4.3) on the shell and the key pages, in the
 * light and the dark theme. It needs painted pixels and computed layout, so it
 * runs in Chromium; the Lightpanda axe scan (`specs/a11y.e2e.ts`) switches the
 * rule off. A failure names the element, the measured ratio and both colours:
 * fix the token in `src/style.css`, not the component.
 */
interface Target {
  name: string
  path: string
  role: Role
  ready: string
  /** Collapse the sidebar first: the rail paints its own icons and tooltip-less links. */
  collapsed?: boolean
}

const TARGETS: Target[] = [
  { name: 'login', path: '/login', role: 'anon', ready: '#username' },
  { name: 'shell with the not-found page', path: '/no-such-page', role: 'admin', ready: 'main h2, main h3' },
  { name: 'shell with the collapsed rail', path: '/no-such-page', role: 'admin', ready: 'main h2, main h3', collapsed: true },
  { name: 'dashboard', path: '/', role: 'admin', ready: 'h1' },
  { name: 'repositories', path: '/repos', role: 'admin', ready: 'h1' },
  { name: 'repository', path: '/repos/seed-raw', role: 'admin', ready: 'h1' },
  { name: 'artifact', path: '/repos/seed-raw/packages/hello.txt', role: 'admin', ready: 'h1' },
  { name: 'security', path: '/security', role: 'admin', ready: 'h1' },
  { name: 'dashboard as developer (locked navigation items)', path: '/', role: 'dev', ready: 'h1' },
]

for (const theme of ['light', 'dark'] as const) {
  describe(`color-contrast, ${theme} theme`, () => {
    for (const target of TARGETS) {
      const it = testAs(target.role, 'chromium')

      it(`${target.name}: ${target.path} has no contrast violations`, async (ui) => {
        await ui.goto(target.path)
        await ui.page.evaluate(
          (mode, rail) => {
            localStorage.setItem('oa.theme', JSON.stringify(mode))
            localStorage.setItem('oa.sidebar.collapsed', JSON.stringify(rail))
          },
          theme,
          target.collapsed === true,
        )
        await ui.reload()
        await expectLoc(ui.locator(target.ready).first()).toBeVisible()
        if (target.role !== 'anon') await shellReady(ui)
        if (theme === 'dark') await expectLoc(ui.locator('html')).toHaveClass(/(^|\s)dark(\s|$)/)
        // Content that loads after the heading must be painted before the scan.
        await painted(ui)
        await freeze(ui)

        const violations = await contrastViolations(ui)
        expect(violations, violations.join('\n')).toEqual([])
      })
    }
  })
}

/** Transient parts of the shell, scanned while they are open. */
for (const theme of ['light', 'dark'] as const) {
  describe(`color-contrast of overlays, ${theme} theme`, () => {
    const it = testAs('admin', 'chromium')

    async function open(ui: Ui, path: string, collapsed: boolean): Promise<void> {
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
      await painted(ui)
      // No transition may be half way when axe reads the colours: the menu fades in, the toast slides in.
      await freeze(ui)
    }

    it('tooltip of the collapsed rail', async (ui) => {
      await open(ui, '/no-such-page', true)
      await ui.page.hover('[data-testid="nav-repos"]')
      await expectLoc(ui.locator('[role="tooltip"]')).toBeVisible()
      const violations = await contrastViolations(ui)
      expect(violations, violations.join('\n')).toEqual([])
    })

    it('user menu', async (ui) => {
      await open(ui, '/no-such-page', false)
      await ui.getByTestId('user-menu').click()
      await expectLoc(ui.getByTestId('user-menu-panel')).toBeVisible()
      const violations = await contrastViolations(ui)
      expect(violations, violations.join('\n')).toEqual([])
    })

    it('toast', async (ui) => {
      await open(ui, '/repos/seed-raw/packages/hello.txt', false)
      await ui.page.evaluate(() => {
        Object.defineProperty(navigator, 'clipboard', { value: { writeText: () => Promise.resolve() }, configurable: true })
      })
      await ui.getByTestId('artifact-menu').click()
      await ui.getByRole('menuitem', { name: 'Copy link to this version' }).click()
      await toastAtRest(ui)
      const violations = await contrastViolations(ui)
      expect(violations, violations.join('\n')).toEqual([])
    })
  })
}

// chooseTheme drives the real user-menu toggle; one pass proves it ends in the same palette the stored preference gives.
describe('color-contrast after switching theme in the user menu', () => {
  const it = testAs('admin', 'chromium')

  it('dark theme chosen in the user menu', async (ui) => {
    await ui.goto('/no-such-page')
    await shellReady(ui)
    await chooseTheme(ui, 'dark')
    await painted(ui)
    await freeze(ui)
    const violations = await contrastViolations(ui)
    expect(violations, violations.join('\n')).toEqual([])
  })
})

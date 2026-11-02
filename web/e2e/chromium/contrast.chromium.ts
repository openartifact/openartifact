// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { contrastViolations } from '../lib/a11y'
import { testAs, type Role } from '../lib/browser'
import { freeze, painted, toastAtRest } from '../lib/visual'
import { chooseTheme, expectLoc, shellReady, t, type Ui } from '../lib/ui'

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
      // A scan of a page whose tooltip is gone finds nothing and passes: the tooltip must be there before and after it.
      const tooltip = ui.locator('[role="tooltip"]')
      await expectLoc(tooltip).toBeVisible()
      const violations = await contrastViolations(ui)
      await expectLoc(tooltip).toBeVisible()
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

/** The upload zone of a hosted repository: its inputs, a field error, and a file that fails on its row. */
for (const theme of ['light', 'dark'] as const) {
  describe(`color-contrast of the upload zone, ${theme} theme`, () => {
    const it = testAs('admin', 'chromium')

    it('inputs, a field error and a failed file', async (ui) => {
      await ui.goto('/repos/seed-maven')
      await ui.page.evaluate((mode) => localStorage.setItem('oa.theme', JSON.stringify(mode)), theme)
      await ui.reload()
      await shellReady(ui)
      await ui.getByRole('button', { name: t('repoDetail.upload'), exact: true }).click()
      const zone = ui.getByTestId('upload-fields')
      await expectLoc(zone).toBeVisible()
      // Browse is disabled until the coordinates are filled in.
      await ui.locator('#upload-version').fill('not a version')
      await expectLoc(ui.locator('#upload-version[aria-invalid="true"]')).toBeVisible()
      await painted(ui)
      await freeze(ui)
      // axe skips disabled controls (WCAG 1.4.3 exempts them), so the disabled Browse button is held to 3:1 here:
      // its label blended by the button's opacity over the dropzone, which must stay readable as "unavailable".
      const disabledRatio = await ui.page.evaluate(() => {
        const button = [...document.querySelectorAll('button')].find((b) => b.disabled && b.closest('[class*="border-dashed"]'))
        if (!button) return 0
        const rgb = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number)
        const lum = (c: number[]) => {
          const [r, g, b] = c.map((v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
          return 0.2126 * r + 0.7152 * g + 0.0722 * b
        }
        const bg = rgb(getComputedStyle(button.closest('[class*="border-dashed"]')!).backgroundColor)
        const fg = rgb(getComputedStyle(button).color)
        const a = Number(getComputedStyle(button).opacity)
        const shown = fg.map((v, i) => v * a + bg[i] * (1 - a))
        const [hi, lo] = [lum(shown), lum(bg)].sort((x, y) => y - x)
        return (hi + 0.05) / (lo + 0.05)
      })
      expect(disabledRatio).toBeGreaterThanOrEqual(3)
      const withError = await contrastViolations(ui)
      expect(withError, withError.join('\n')).toEqual([])

      await ui.locator('#upload-group').fill('com.example')
      await ui.locator('#upload-artifact').fill('lib')
      await ui.locator('#upload-version').fill('1.0.0')
      const file = join(mkdtempSync(join(tmpdir(), 'oa-upload-')), 'other-1.0.0.jar')
      writeFileSync(file, 'x')
      const input = await ui.page.$('input[type="file"]')
      await input!.uploadFile(file)
      await expectLoc(ui.getByText('other-1.0.0.jar')).toBeVisible()
      await expectLoc(ui.getByText(t('upload.error.mavenName'))).toBeVisible()
      await painted(ui)
      await freeze(ui)
      const withRow = await contrastViolations(ui)
      expect(withRow, withRow.join('\n')).toEqual([])
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

/**
 * Format badges (WCAG 1.4.11 non-text contrast): every one of the 22 formats on the login page is
 * painted with its icon, holds 3:1 between the icon's computed colour and the badge's own
 * background in both themes, has its name as text (colour is never the only carrier, 1.4.1), and
 * every icon has the same box and sits on the label's vertical centre.
 */
for (const theme of ['light', 'dark'] as const) {
  describe(`format badge contrast, ${theme} theme`, () => {
    const it = testAs('anon', 'chromium')

    it('every format icon holds 3:1 on its badge', async (ui) => {
      await ui.goto('/login')
      await ui.page.evaluate((mode) => localStorage.setItem('oa.theme', JSON.stringify(mode)), theme)
      await ui.reload()
      await expectLoc(ui.locator('#username')).toBeVisible()
      await painted(ui)
      await freeze(ui)

      const rows = await ui.page.evaluate(() => {
        const rgb = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number)
        const lum = (c: number[]) => {
          const [r, g, b] = c.map((v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
          return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!
        }
        const badges = [...document.querySelectorAll('span.font-mono')].filter((e) => e.querySelector('svg'))
        return badges.map((badge) => {
          const icon = badge.querySelector('svg')!
          const wrap = icon.parentElement!
          const [hi, lo] = [lum(rgb(getComputedStyle(wrap).color)), lum(rgb(getComputedStyle(badge).backgroundColor))].sort((x, y) => y - x)
          const label = badge.lastElementChild as HTMLElement
          const ir = icon.getBoundingClientRect()
          const lr = label.getBoundingClientRect()
          return {
            name: label.textContent?.trim() ?? '',
            ratio: (hi! + 0.05) / (lo! + 0.05),
            w: Math.round(ir.width),
            h: Math.round(ir.height),
            drawn: (icon.querySelector('path')?.getAttribute('d') ?? '').length > 20,
            offCentre: Math.abs(ir.top + ir.height / 2 - (lr.top + lr.height / 2)),
          }
        })
      })

      expect(rows).toHaveLength(22)
      const weak = rows.filter((r) => r.ratio < 3).map((r) => `${r.name}: ${r.ratio.toFixed(2)}:1`)
      expect(weak, `icons under 3:1 on the badge in the ${theme} theme`).toEqual([])
      expect(rows.filter((r) => r.name === '' || !r.drawn).map((r) => r.name || '(no label)')).toEqual([])
      expect(new Set(rows.map((r) => `${r.w}x${r.h}`)).size, 'icons share one box').toBe(1)
      expect(rows.filter((r) => r.offCentre > 1).map((r) => r.name), 'icons centred on the label').toEqual([])
    })
  })
}

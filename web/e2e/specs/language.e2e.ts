// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe } from 'bun:test'
import de from '../../src/i18n/locales/de/core.json'
import deAuth from '../../src/i18n/locales/de/auth.json'
import ja from '../../src/i18n/locales/ja/core.json'
import deShell from '../../src/i18n/locales/de/shell.json'
import { testAs } from '../lib/browser'
import { expectLoc, primaryNav, shellReady, t, type Loc, type Ui } from '../lib/ui'

/** Picks a language in the user menu (a native <select>: the same events a user's choice fires). */
async function chooseLanguage(ui: Ui, value: string): Promise<void> {
  await ui.getByTestId('user-menu').click()
  await expectLoc(ui.locator('#language-select-menu')).toBeVisible()
  await ui.page.select('#language-select-menu', value)
}

/** The primary navigation while the UI is German (its landmark name is translated too). */
function germanNav(ui: Ui): Loc {
  return ui.getByRole('navigation', { name: deAuth['shell.primaryNav'] })
}

describe('language picker', () => {
  const it = testAs('admin')

  it('switches the UI language without a reload and remembers it', async (ui) => {
    await ui.goto('/repos')
    await shellReady(ui)
    await expectLoc(ui.locator('html')).toHaveAttribute('lang', 'en')
    await expectLoc(primaryNav(ui).getByRole('link', { name: t('nav.settings'), exact: true })).toBeVisible()

    await chooseLanguage(ui, 'de')
    await expectLoc(ui.locator('html')).toHaveAttribute('lang', 'de')
    await expectLoc(germanNav(ui).getByRole('link', { name: de['nav.settings'], exact: true })).toBeVisible()
    await expectLoc(germanNav(ui).getByRole('link', { name: t('nav.settings'), exact: true })).toHaveCount(0)

    // Still on the same page: the language applied in place.
    await ui.expectUrl('/repos')

    // The choice is per browser: a reload keeps it.
    await ui.reload()
    await shellReady(ui)
    await expectLoc(ui.locator('html')).toHaveAttribute('lang', 'de')
    await expectLoc(germanNav(ui).getByRole('link', { name: de['nav.settings'], exact: true })).toBeVisible()

    // Back to the browser default (English in the test browser).
    await ui.getByTestId('user-menu').click()
    await expectLoc(ui.getByLabel(deShell['shell.menu.language'])).toBeVisible()
    await ui.page.select('#language-select-menu', 'auto')
    await expectLoc(ui.locator('html')).toHaveAttribute('lang', 'en')
    await expectLoc(primaryNav(ui).getByRole('link', { name: t('nav.settings'), exact: true })).toBeVisible()
  })
})

describe('language picker on the login page', () => {
  const it = testAs('anon')

  it('translates the form before sign-in', async (ui) => {
    await ui.goto('/login')
    await ui.page.select('#language-select-login', 'ja')
    await expectLoc(ui.locator('html')).toHaveAttribute('lang', 'ja')
    await expectLoc(ui.getByRole('button', { name: ja['auth.signIn'] })).toBeVisible()
    await expectLoc(ui.getByRole('button', { name: t('auth.signIn') })).toHaveCount(0)
  })
})

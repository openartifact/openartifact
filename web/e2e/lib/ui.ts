// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { expect, type Locator, type Page } from '@playwright/test'
import type { Account } from '../env'
import { t } from '../../src/i18n/en'

export { t }

/** Fills the login form and submits it. */
export async function submitLogin(page: Page, account: Account): Promise<void> {
  await page.getByLabel(t('auth.username')).fill(account.username)
  await page.locator('#password').fill(account.password)
  await page.getByRole('button', { name: t('auth.signIn') }).click()
}

/** The primary navigation landmark. */
export function primaryNav(page: Page) {
  return page.getByRole('navigation', { name: t('shell.primaryNav') })
}

/** The breadcrumb trail of the page header. */
export function breadcrumbs(page: Page) {
  return page.getByRole('navigation', { name: t('shell.breadcrumbs') })
}

/** Waits until the app shell is mounted for a signed-in user. */
export async function shellReady(page: Page): Promise<void> {
  await expect(page.getByTestId('user-menu')).toBeVisible()
}

/** Opens the user menu and picks a theme; the choice is persisted by the app in localStorage. */
export async function chooseTheme(page: Page, mode: 'light' | 'dark'): Promise<void> {
  await activate(page.getByTestId('user-menu'))
  await activate(page.getByTestId(`theme-${mode}`))
  await expect(page.locator('html')).toHaveClass(mode === 'dark' ? /(^|\s)dark(\s|$)/ : /^((?!\bdark\b).)*$/)
  await page.keyboard.press('Escape')
}

/**
 * Clicks an element the way the browser would for a user, minus the pointer:
 * waits for it to be visible and enabled, then dispatches the click event on it.
 *
 * Lightpanda has no layout, so `Locator.click()` (which clicks at the element's
 * reported coordinates) lands on <body> for elements that appear after the first
 * layout pass, such as the items of a popover menu. Dispatching the event on the
 * element itself reaches the same handlers.
 */
export async function activate(target: Locator): Promise<void> {
  await expect(target).toBeVisible()
  await expect(target).toBeEnabled()
  await target.dispatchEvent('click')
}

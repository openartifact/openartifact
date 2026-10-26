// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ADMIN, DEV } from '../env'
import { adminApi, uniqueName } from '../lib/api'
import { expect, test } from '../fixtures'
import { primaryNav, shellReady, submitLogin, t } from '../lib/ui'

// Token-free sign-in: the username/password form sets an httpOnly session
// cookie; the browser never holds a bearer token.
test.describe('sign-in form', () => {
  test('admin signs in and lands on the dashboard with an httpOnly session cookie', async ({ page, context }) => {
    await page.goto('/login')
    await submitLogin(page, ADMIN())
    await expect(page).toHaveURL('/')
    await shellReady(page)

    await page.getByTestId('user-menu').click()
    await expect(page.getByTestId('user-menu-name')).toHaveText('admin')
    await expect(page.getByTestId('user-menu-role')).toHaveText(t('shell.role.admin'))

    const session = (await context.cookies()).find((c) => c.name === 'oa_session')
    expect(session, 'session cookie').toBeDefined()
    expect(session?.httpOnly).toBe(true)
    expect(session?.sameSite).not.toBe('None')

    const stored = await page.evaluate(() => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]))
    expect(stored).not.toMatch(/oa_pat_|bearer/i)
  })

  test('developer signs in as a member', async ({ page }) => {
    await page.goto('/login')
    await submitLogin(page, DEV())
    await expect(page).toHaveURL('/')
    await shellReady(page)
    await page.getByTestId('user-menu').click()
    await expect(page.getByTestId('user-menu-name')).toHaveText('dev')
    await expect(page.getByTestId('user-menu-role')).toHaveText(t('shell.role.member'))
  })

  test('a wrong password is refused with an error and no session', async ({ page, context }) => {
    // A dedicated account: failed attempts count against the user name, and the
    // shared admin and dev accounts must stay signed in for the other specs.
    const api = await adminApi()
    const username = uniqueName('e2e-wrongpw')
    const password = `${uniqueName('pw')}-${uniqueName('x')}`
    const created = await api.post('/api/v1/users', { data: { username, email: `${username}@example.com`, password } })
    expect(created.ok()).toBe(true)
    await api.dispose()

    await page.goto('/login')
    await submitLogin(page, { username, password: 'not-the-password' })
    await expect(page.getByRole('alert')).toBeVisible()
    await expect(page.getByRole('alert')).not.toBeEmpty()
    await expect(page).toHaveURL(/\/login/)
    expect((await context.cookies()).some((c) => c.name === 'oa_session')).toBe(false)
  })

  test('a protected page sends a visitor to the form and back after sign-in', async ({ page }) => {
    await page.goto('/repos')
    await expect(page).toHaveURL((url) => url.pathname === '/login' && url.searchParams.get('next') === '/repos')
    await submitLogin(page, DEV())
    await expect(page).toHaveURL('/repos')
    await expect(primaryNav(page)).toBeVisible()
  })
})

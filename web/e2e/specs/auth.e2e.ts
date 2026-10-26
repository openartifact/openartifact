// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { ADMIN, DEV } from '../env'
import { adminApi, uniqueName } from '../lib/api'
import { testAs } from '../lib/browser'
import { expectLoc, primaryNav, shellReady, submitLogin, t } from '../lib/ui'

// Token-free sign-in: the username/password form sets an httpOnly session
// cookie; the browser never holds a bearer token.
describe('sign-in form', () => {
  const it = testAs('anon')

  it('admin signs in and lands on the dashboard with an httpOnly session cookie', async (ui) => {
    await ui.goto('/login')
    await submitLogin(ui, ADMIN())
    await ui.expectUrl('/')
    await shellReady(ui)

    await ui.getByTestId('user-menu').click()
    await expectLoc(ui.getByTestId('user-menu-name')).toHaveText('admin')
    await expectLoc(ui.getByTestId('user-menu-role')).toHaveText(t('shell.role.admin'))

    const session = (await ui.cookies()).find((c) => c.name === 'oa_session')
    expect(session, 'session cookie').toBeDefined()
    expect(session?.httpOnly).toBe(true)
    expect(session?.sameSite).not.toBe('None')

    const stored = (await ui.page.evaluate(() => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]))) as string
    expect(stored).not.toMatch(/oa_pat_|bearer/i)
  })

  it('developer signs in as a member', async (ui) => {
    await ui.goto('/login')
    await submitLogin(ui, DEV())
    await ui.expectUrl('/')
    await shellReady(ui)
    await ui.getByTestId('user-menu').click()
    await expectLoc(ui.getByTestId('user-menu-name')).toHaveText('dev')
    await expectLoc(ui.getByTestId('user-menu-role')).toHaveText(t('shell.role.member'))
  })

  it('a wrong password is refused with an error and no session', async (ui) => {
    // A dedicated account: failed attempts count against the user name, and the
    // shared admin and dev accounts must stay signed in for the other specs.
    const username = uniqueName('e2e-wrongpw')
    const password = `${uniqueName('pw')}-${uniqueName('x')}`
    const created = await adminApi().post('/api/v1/users', { data: { username, email: `${username}@example.com`, password } })
    expect(created.ok, created.text()).toBe(true)

    await ui.goto('/login')
    await submitLogin(ui, { username, password: 'not-the-password' })
    await expectLoc(ui.getByRole('alert')).toBeVisible()
    await expectLoc(ui.getByRole('alert')).not.toBeEmpty()
    await ui.expectUrl(/\/login/)
    expect((await ui.cookies()).some((c) => c.name === 'oa_session')).toBe(false)
  })

  it('a protected page sends a visitor to the form and back after sign-in', async (ui) => {
    await ui.goto('/repos')
    await ui.expectUrl((url) => url.pathname === '/login' && url.searchParams.get('next') === '/repos')
    await submitLogin(ui, DEV())
    await ui.expectUrl('/repos')
    await expectLoc(primaryNav(ui)).toBeVisible()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe } from 'bun:test'
import { testAs } from '../lib/browser'
import { expectLoc, shellReady, t, type Ui } from '../lib/ui'

// The command palette's actions: opened from the keyboard and from the header, narrowed to actions
// with ">", run with Enter, and offered only to whoever may run them.
const palette = (ui: Ui) => ui.getByTestId('palette')
const input = (ui: Ui) => ui.getByTestId('palette-input')
const option = (ui: Ui, id: string) => ui.getByTestId(`palette-option-${id}`)

async function openWithSlash(ui: Ui): Promise<void> {
  await ui.goto('/')
  await shellReady(ui)
  await ui.press('/')
  await expectLoc(palette(ui)).toBeVisible()
}

describe('command palette as an admin', () => {
  const it = testAs('admin')

  it('the slash key opens it, an action is found by name and run with Enter, and Escape closes it', async (ui) => {
    await openWithSlash(ui)
    await input(ui).fill(`>${t('shell.action.createToken')}`)
    await expectLoc(option(ui, 'action:create-token')).toBeVisible()
    await expectLoc(option(ui, 'action:create-token')).toContainText(t('shell.action.createToken'))
    await ui.press('Enter')

    // The action navigates to the token page with its create drawer open, and the palette is gone.
    await ui.expectUrl((url) => url.pathname === '/settings/tokens' && url.searchParams.get('new') === '1')
    await expectLoc(palette(ui)).toBeHidden()
    await expectLoc(ui.getByRole('dialog')).toBeVisible()
  })

  it('the header search opens it, and actions only an admin may run are offered', async (ui) => {
    await ui.goto('/')
    await shellReady(ui)
    await ui.getByTestId('search-pill').click()
    await expectLoc(palette(ui)).toBeVisible()

    await input(ui).fill('>')
    await expectLoc(option(ui, 'action:create-repo')).toBeVisible()
    await expectLoc(option(ui, 'action:create-user')).toBeVisible()

    await ui.press('Escape')
    await expectLoc(palette(ui)).toBeHidden()
    await ui.expectUrl('/')
  })
})

describe('command palette as a developer', () => {
  const it = testAs('dev')

  it('is not offered the actions its role may not run', async (ui) => {
    await openWithSlash(ui)
    await input(ui).fill('>')
    // Everybody may create a token; only admins may create users or, here, repositories.
    await expectLoc(option(ui, 'action:create-token')).toBeVisible()
    await expectLoc(option(ui, 'action:create-user')).toHaveCount(0)
    await expectLoc(option(ui, 'action:create-repo')).toHaveCount(0)

    await input(ui).fill(`>${t('shell.action.createUser')}`)
    await expectLoc(ui.getByText(t('shell.palette.noActions'))).toBeVisible()
  })
})

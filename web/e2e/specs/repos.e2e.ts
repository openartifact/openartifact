// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { adminApi, uniqueName } from '../lib/api'
import { testAs } from '../lib/browser'
import { expectLoc, t } from '../lib/ui'

describe('create a repository', () => {
  const it = testAs('admin')

  it('the drawer creates a hosted npm repository that then opens and lists', async (ui) => {
    const name = uniqueName('e2e-ui')
    await ui.goto('/repos')
    await ui.getByTestId('new-repo').first().click()

    const drawer = ui.getByRole('dialog', { name: t('rl.create.title') })
    await expectLoc(drawer).toBeVisible()

    // The create button stays disabled until the name is valid.
    const create = drawer.getByRole('button', { name: t('repos.create') })
    await expectLoc(create).toBeDisabled()
    await drawer.getByLabel(t('wizard.name')).fill(name)
    await drawer.getByRole('radio', { name: 'npm' }).click()
    await expectLoc(drawer.getByRole('radio', { name: 'npm' })).toHaveAttribute('aria-checked', 'true')
    await expectLoc(create).toBeEnabled()
    await create.click()

    await expectLoc(ui.getByText(t('repos.new.created'))).toBeVisible()
    await expectLoc(drawer).toBeHidden()

    // The server agrees: a hosted, private npm repository.
    const res = await adminApi().get('/api/v1/repositories', { params: { q: name } })
    expect(res.ok).toBe(true)
    const { repositories } = res.json<{ repositories: Array<Record<string, unknown>> }>()
    expect(repositories).toHaveLength(1)
    expect(repositories[0]).toMatchObject({ name, format: 'npm', repo_type: 'hosted', public: false })

    // It shows up in the list and its page opens.
    await ui.goto('/repos')
    await ui.getByRole('searchbox', { name: t('repos.search') }).fill(name)
    await ui.getByRole('link', { name, exact: true }).click()
    await ui.expectUrl(`/repos/${name}`)
    await expectLoc(ui.getByRole('heading', { level: 1, name })).toBeVisible()
  })

  it('an invalid name is explained and cannot be submitted', async (ui) => {
    await ui.goto('/repos?new=1')
    const drawer = ui.getByRole('dialog', { name: t('rl.create.title') })
    await expectLoc(drawer).toBeVisible()
    await drawer.getByLabel(t('wizard.name')).fill('Not A Valid Name!')
    await expectLoc(drawer.getByRole('button', { name: t('repos.create') })).toBeDisabled()
    await expectLoc(drawer.getByLabel(t('wizard.name'))).toHaveAttribute('aria-invalid', 'true')
  })
})

describe('developer', () => {
  const it = testAs('dev')

  it('is not offered the create button without a project role', async (ui) => {
    await ui.goto('/repos')
    await expectLoc(ui.getByRole('heading', { level: 1, name: t('repos.title') })).toBeVisible()
    await expectLoc(ui.getByRole('link', { name: 'seed-raw', exact: true })).toBeVisible()
    await expectLoc(ui.getByTestId('new-repo')).toHaveCount(0)
  })
})

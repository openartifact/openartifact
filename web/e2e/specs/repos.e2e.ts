// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { expect, test } from '../fixtures'
import { adminApi, uniqueName } from '../lib/api'
import { t } from '../lib/ui'

test.describe('create a repository', () => {
  test.use({ role: 'admin' })

  test('the drawer creates a hosted npm repository that then opens and lists', async ({ page }) => {
    const name = uniqueName('e2e-ui')
    await page.goto('/repos')
    await page.getByTestId('new-repo').first().click()

    const drawer = page.getByRole('dialog', { name: t('rl.create.title') })
    await expect(drawer).toBeVisible()

    // The create button stays disabled until the name is valid.
    const create = drawer.getByRole('button', { name: t('repos.create') })
    await expect(create).toBeDisabled()
    await drawer.getByLabel(t('wizard.name')).fill(name)
    await drawer.getByRole('radio', { name: 'npm' }).click()
    await expect(drawer.getByRole('radio', { name: 'npm' })).toHaveAttribute('aria-checked', 'true')
    await expect(create).toBeEnabled()
    await create.click()

    await expect(page.getByText(t('repos.new.created'))).toBeVisible()
    await expect(drawer).toBeHidden()

    // The server agrees: a hosted, private npm repository.
    const api = await adminApi()
    try {
      const res = await api.get('/api/v1/repositories', { params: { q: name } })
      expect(res.ok()).toBe(true)
      const { repositories } = (await res.json()) as { repositories: Array<Record<string, unknown>> }
      expect(repositories).toHaveLength(1)
      expect(repositories[0]).toMatchObject({ name, format: 'npm', repo_type: 'hosted', public: false })
    } finally {
      await api.dispose()
    }

    // It shows up in the list and its page opens.
    await page.goto('/repos')
    await page.getByRole('searchbox', { name: t('repos.search') }).fill(name)
    await page.getByRole('link', { name, exact: true }).click()
    await expect(page).toHaveURL(`/repos/${name}`)
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible()
  })

  test('an invalid name is explained and cannot be submitted', async ({ page }) => {
    await page.goto('/repos?new=1')
    const drawer = page.getByRole('dialog', { name: t('rl.create.title') })
    await expect(drawer).toBeVisible()
    await drawer.getByLabel(t('wizard.name')).fill('Not A Valid Name!')
    await expect(drawer.getByRole('button', { name: t('repos.create') })).toBeDisabled()
    await expect(drawer.getByLabel(t('wizard.name'))).toHaveAttribute('aria-invalid', 'true')
  })
})

test.describe('developer', () => {
  test.use({ role: 'dev' })

  test('is not offered the create button without a project role', async ({ page }) => {
    await page.goto('/repos')
    await expect(page.getByRole('heading', { level: 1, name: t('repos.title') })).toBeVisible()
    await expect(page.getByRole('link', { name: 'seed-raw', exact: true })).toBeVisible()
    await expect(page.getByTestId('new-repo')).toHaveCount(0)
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeAll, describe, expect } from 'bun:test'
import { adminApi, uniqueName } from '../lib/api'
import { testAs } from '../lib/browser'
import { expectLoc, t } from '../lib/ui'
import { tp } from '../../src/i18n'

// Settings > Service accounts: an account created and deleted in the UI, and a CI OIDC trust policy
// created (with the server's identity rule shown as it comes), listed and deleted.
const api = adminApi()
const LIST = '/settings/service-accounts'

interface Account {
  id: string
  name: string
}

/** The service account with this name as the server holds it, or undefined. */
async function accountNamed(name: string): Promise<Account | undefined> {
  const res = await api.get('/api/v1/service-accounts', { params: { per_page: '100' } })
  return res.json<{ data: Account[] }>().data.find((a) => a.name === name)
}

async function policyNames(): Promise<string[]> {
  const res = await api.get('/api/v1/oidc-trust-policies')
  return res.json<{ data: Array<{ name: string }> }>().data.map((p) => p.name)
}

describe('service accounts', () => {
  const it = testAs('admin')

  it('a service account is created in the drawer, opens on its page, and is deleted from the list', async (ui) => {
    const name = uniqueName('e2e-sa')
    await ui.goto(LIST)
    await ui.getByTestId('account-new').click()
    const drawer = ui.getByRole('dialog', { name: t('sa.create.title') })
    await expectLoc(drawer).toBeVisible()

    // A name the server's pattern rejects is explained before anything is sent.
    await drawer.getByLabel(t('sa.field.name')).fill('no')
    await drawer.getByRole('button', { name: t('common.create') }).click()
    await expectLoc(drawer.getByLabel(t('sa.field.name'))).toHaveAttribute('aria-invalid', 'true')
    expect(await accountNamed('no')).toBeUndefined()

    await drawer.getByLabel(t('sa.field.name')).fill(name)
    await drawer.getByLabel(t('sa.field.description')).fill('created by the e2e suite')
    await drawer.getByRole('button', { name: t('common.create') }).click()

    // It lands on the account's own page.
    await ui.expectUrl(/\/settings\/service-accounts\/[0-9a-f-]{36}$/)
    await expectLoc(ui.getByRole('heading', { level: 1, name })).toBeVisible()
    expect(await accountNamed(name)).toMatchObject({ name })

    // Back on the list: found by search, then deleted after the confirmation.
    await ui.goto(LIST)
    await ui.getByRole('searchbox', { name: t('sa.search') }).fill(name)
    await expectLoc(ui.getByRole('link', { name, exact: true })).toBeVisible()
    await ui.getByTestId('action-delete').click()
    const confirm = ui.getByRole('dialog', { name: t('sa.confirmDelete') })
    await expectLoc(confirm).toBeVisible()
    await expectLoc(confirm).toContainText(tp('sa.confirmDeleteBody', { name }).slice(0, 40))
    await confirm.getByRole('button', { name: t('common.delete') }).click()

    await expectLoc(ui.getByText(tp('sa.deleted', { name }))).toBeVisible()
    await expectLoc(ui.getByRole('link', { name, exact: true })).toHaveCount(0)
    expect(await accountNamed(name)).toBeUndefined()
  })
})

describe('OIDC trust policies', () => {
  const it = testAs('admin')
  let account: Account
  const policy = uniqueName('e2e-policy')

  beforeAll(async () => {
    const name = uniqueName('e2e-ci')
    const created = await api.post('/api/v1/service-accounts', { data: { name } })
    expect(created.ok, created.text()).toBe(true)
    account = created.json<Account>()
  })

  it('a policy for a shared CI issuer needs a pinned repository: the server says so, the corrected policy is created', async (ui) => {
    await ui.goto(`${LIST}/${account.id}`)
    await expectLoc(ui.getByRole('heading', { level: 1, name: account.name })).toBeVisible()
    await ui.getByTestId('policy-new').click()
    const drawer = ui.getByRole('dialog', { name: t('sa.policy.createTitle') })
    await expectLoc(drawer).toBeVisible()

    // The GitHub preset fills the issuer and the claim that names the repository.
    await drawer.getByTestId('preset-github').click()
    await expectLoc(drawer.locator('#policy-issuer')).toHaveValue('https://token.actions.githubusercontent.com')
    await expectLoc(drawer.locator('#cond-claim-0')).toHaveValue('repository')
    await drawer.locator('#policy-name').fill(policy)
    await drawer.locator('#policy-audience').fill('openartifact-e2e')

    // A pattern that starts with a wildcard would trust every customer of the CI system.
    await drawer.locator('#cond-patterns-0').fill('*/anything')
    await drawer.getByRole('button', { name: t('common.create') }).click()
    await expectLoc(drawer.getByTestId('policy-error')).toContainText('shared by every customer')
    expect(await policyNames()).not.toContain(policy)

    await drawer.locator('#cond-patterns-0').fill('openartifact/*')
    await drawer.getByRole('button', { name: t('common.create') }).click()
    await expectLoc(drawer).toBeHidden()
    await expectLoc(ui.getByText(tp('sa.policy.created', { name: policy }))).toBeVisible()
    await expectLoc(ui.getByRole('cell', { name: policy, exact: true })).toBeVisible()
    expect(await policyNames()).toContain(policy)
  })

  it('the policy is listed with every other on the Trust policies tab, and deleting it removes it', async (ui) => {
    await ui.goto(`${LIST}?tab=policies`)
    const name = ui.getByRole('cell', { name: policy, exact: true })
    await expectLoc(name).toBeVisible()
    await expectLoc(ui.getByRole('link', { name: account.name, exact: true })).toBeVisible()

    await ui.getByRole('searchbox', { name: t('sa.policy.search') }).fill(policy)
    await ui.getByTestId('action-delete').click()
    const confirm = ui.getByRole('dialog', { name: t('sa.policy.confirmDelete') })
    await expectLoc(confirm).toBeVisible()
    await confirm.getByRole('button', { name: t('common.delete') }).click()

    await expectLoc(ui.getByText(tp('sa.policy.deleted', { name: policy }))).toBeVisible()
    await expectLoc(name).toHaveCount(0)
    expect(await policyNames()).not.toContain(policy)
  })

  it('deleting the account from its page takes it away', async (ui) => {
    await ui.goto(`${LIST}/${account.id}`)
    await expectLoc(ui.getByRole('heading', { level: 1, name: account.name })).toBeVisible()
    await ui.getByTestId('account-delete').click()
    const confirm = ui.getByRole('dialog', { name: t('sa.confirmDelete') })
    await expectLoc(confirm).toBeVisible()
    await confirm.getByRole('button', { name: t('common.delete') }).click()

    await ui.expectUrl(LIST)
    expect(await accountNamed(account.name)).toBeUndefined()
  })
})

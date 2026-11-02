// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterAll, beforeAll, describe, expect } from 'bun:test'
import { requireEnv } from '../env'
import { adminApi, uniqueName } from '../lib/api'
import { testAs } from '../lib/browser'
import { npmEnv, writePackage, type NpmEnv } from '../lib/npm'
import { expectLoc, t, type Ui } from '../lib/ui'

// The live preview of the cleanup policy editor: the server evaluates the policy being typed against
// three published versions and says what a run would delete; nothing is deleted or saved.
const VERSIONS = ['1.0.0', '1.1.0', '1.2.0']
let repo: string
let pkg: string
let env: NpmEnv
const api = adminApi()

beforeAll(async () => {
  const origin = requireEnv('OA_URL')
  repo = uniqueName('e2e-cleanup')
  pkg = uniqueName('e2e-old')
  const created = await api.post('/api/v1/repositories', { data: { name: repo, format: 'npm', public: false } })
  expect(created.ok, created.text()).toBe(true)
  env = await npmEnv(origin, repo, requireEnv('OA_ADMIN_TOKEN'))
  for (const version of VERSIONS) {
    await env.npm(['publish', '--registry', `${origin}/repos/${repo}/`], await writePackage(env, pkg, version))
  }
}, 120_000)

afterAll(async () => {
  await env?.dispose()
})

const drawer = (ui: Ui) => ui.getByRole('dialog', { name: t('cleanup.addTitle') })

async function storedVersions(): Promise<number> {
  const res = await api.get(`/api/v1/repositories/${repo}/packages`)
  return res.json<{ packages: Array<{ name: string }> }>().packages.filter((p) => p.name === pkg).length
}

describe('cleanup policy preview', () => {
  const it = testAs('admin')

  it('says what the policy being typed would delete, and deletes nothing', async (ui) => {
    await ui.goto(`/repos/${repo}?tab=settings`)
    await ui.getByRole('button', { name: t('cleanup.add') }).first().click()
    await expectLoc(drawer(ui)).toBeVisible()
    await expectLoc(drawer(ui).getByTestId('cleanup-preview-safe')).toContainText(t('cleanup.preview.safe'))

    // Keeping ten per package leaves all three published versions alone.
    await drawer(ui).locator('#cleanup-name').fill('keep-newest')
    await expectLoc(drawer(ui).getByTestId('cleanup-preview-none')).toBeVisible()

    // Keeping one per package would delete the two older versions: the sample names them.
    await drawer(ui).locator('#cleanup-keep-count').fill('1')
    await expectLoc(drawer(ui).getByTestId('cleanup-preview-rows')).toHaveText('2')
    const sample = drawer(ui).getByTestId('cleanup-preview-item')
    await expectLoc(sample).toHaveCount(2)
    await expectLoc(sample.filter({ hasText: pkg })).toHaveCount(2)
    await expectLoc(sample.filter({ hasText: VERSIONS[2]! })).toHaveCount(0)

    // Raising the count again empties the preview; an empty value goes back to waiting for a rule.
    await drawer(ui).locator('#cleanup-keep-count').fill('2')
    await expectLoc(drawer(ui).getByTestId('cleanup-preview-rows')).toHaveText('1')
    await drawer(ui).locator('#cleanup-keep-count').fill('')
    await expectLoc(drawer(ui).getByTestId('cleanup-preview-idle')).toBeVisible()

    // Cancelling saves no policy and removes no version.
    await drawer(ui).getByRole('button', { name: t('common.cancel') }).click()
    await expectLoc(drawer(ui)).toBeHidden()
    const policies = await api.get(`/api/v1/repositories/${repo}/cleanup-policies`)
    expect(policies.ok, policies.text()).toBe(true)
    expect(JSON.stringify(policies.json())).not.toContain('keep-newest')
    expect(await storedVersions()).toBe(VERSIONS.length)
  })
})

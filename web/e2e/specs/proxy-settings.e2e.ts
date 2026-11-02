// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterAll, beforeAll, describe, expect } from 'bun:test'
import { repositoryOf } from '../lib/api'
import { testAs } from '../lib/browser'
import { createCachedNpmProxy, startFakeNpmUpstream, type FakeUpstream } from '../lib/upstream'
import { expectLoc, t, type Ui } from '../lib/ui'
import { tp } from '../../src/i18n'

// Editing the upstream and cache settings of a proxy repository: the values persist (the summary
// after a reload, the server's own record), the minimum age holds a fresh upstream release back,
// and what only the server can judge comes back as an error on the field it names.
const PKG = 'fresh-pkg'

let upstream: FakeUpstream
let repo: string

beforeAll(async () => {
  // Published a moment ago, so a minimum age of a day holds it back.
  upstream = startFakeNpmUpstream([{ name: PKG, version: '1.0.0', published: new Date().toISOString() }])
  repo = await createCachedNpmProxy(upstream, [PKG], 'e2e-proxyset')
})

afterAll(async () => {
  await upstream?.stop()
})

const form = (ui: Ui) => ui.locator(`form[aria-label="${t('repoDetail.proxy.editTitle')}"]`)
const summary = (ui: Ui) => ui.locator(`section[aria-label="${t('repoDetail.proxy.title')}"]`)

async function openForm(ui: Ui): Promise<void> {
  await ui.goto(`/repos/${repo}`)
  await ui.getByTestId('edit-proxy-settings').click()
  await expectLoc(form(ui)).toBeVisible()
}

describe('proxy settings of a proxy repository', () => {
  const it = testAs('admin')

  it('saving new TTLs and a minimum age persists them, and the minimum age holds a fresh release back', async (ui) => {
    await openForm(ui)
    // The form starts from the server's defaults.
    await expectLoc(ui.locator('#ps-min-age')).toHaveValue('')

    await ui.locator('#ps-cache-ttl').fill('7200')
    await ui.locator('#ps-negative-ttl').fill('120')
    await ui.locator('#ps-max-stale').fill('86400')
    await ui.locator('#ps-min-age').fill('24')
    await form(ui).getByRole('button', { name: t('common.save') }).click()

    await expectLoc(ui.getByText(t('repoDetail.proxy.saved'))).toBeVisible()
    await expectLoc(form(ui)).toBeHidden()
    await expectLoc(summary(ui)).toContainText(tp('repoDetail.proxy.minAgeValue', { hours: '24' }))
    expect(await repositoryOf(repo)).toMatchObject({ cache_ttl_seconds: 7200, negative_cache_ttl_seconds: 120, max_stale_seconds: 86400, min_age_hours: 24 })

    // A reload shows what the server stored, in the form too.
    await ui.reload()
    await expectLoc(summary(ui)).toContainText(tp('repoDetail.proxy.minAgeValue', { hours: '24' }))
    await ui.getByTestId('edit-proxy-settings').click()
    await expectLoc(ui.locator('#ps-cache-ttl')).toHaveValue('7200')
    await expectLoc(ui.locator('#ps-negative-ttl')).toHaveValue('120')
    await expectLoc(ui.locator('#ps-max-stale')).toHaveValue('86400')
    await expectLoc(ui.locator('#ps-min-age')).toHaveValue('24')

    // The gate now holds the release back until a day has passed since its publication.
    await ui.goto(`/repos/${repo}/packages/${PKG}`)
    await expectLoc(ui.getByTestId('proxy-versions')).toContainText(tp('artifact.versions.availableAt', { when: '@@' }).split('@@')[0]!)
  })

  it('a value that is not a whole number is explained and cannot be saved', async (ui) => {
    await openForm(ui)
    await ui.locator('#ps-cache-ttl').fill('soon')
    await expectLoc(ui.locator('#ps-cache-ttl')).toHaveAttribute('aria-invalid', 'true')
    await expectLoc(ui.getByText(t('repoDetail.proxy.error.whole'))).toBeVisible()
    await expectLoc(form(ui).getByRole('button', { name: t('common.save') })).toBeDisabled()

    await form(ui).getByTestId('proxy-cancel').click()
    await expectLoc(form(ui)).toBeHidden()
    expect(await repositoryOf(repo)).toMatchObject({ cache_ttl_seconds: 7200 })
  })

  it('an upstream address the server refuses is reported on the upstream field and nothing is saved', async (ui) => {
    const before = (await repositoryOf(repo)).upstream_url
    await openForm(ui)
    // Well-formed for the browser; the cloud metadata address is refused by the server alone.
    await ui.locator('#ps-upstream-url').fill('http://169.254.169.254')
    await form(ui).getByRole('button', { name: t('common.save') }).click()

    const error = ui.locator('#ps-upstream-url-error')
    await expectLoc(error).toContainText('not allowed')
    await expectLoc(ui.locator('#ps-upstream-url')).toHaveAttribute('aria-invalid', 'true')
    await expectLoc(form(ui)).toBeVisible()
    expect((await repositoryOf(repo)).upstream_url).toBe(before)

    // Editing the field clears the server's complaint.
    await ui.locator('#ps-upstream-url').fill(upstream.url)
    await expectLoc(ui.locator('#ps-upstream-url-error')).toHaveCount(0)
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterAll, beforeAll, describe, expect } from 'bun:test'
import { adminApi, repositoryOf } from '../lib/api'
import { testAs } from '../lib/browser'
import { createCachedNpmProxy, startFakeNpmUpstream, type FakeUpstream } from '../lib/upstream'
import { expectLoc, t, type Ui } from '../lib/ui'
import { tp } from '../../src/i18n'

// Package curation of a proxy repository, end to end: the mode, a rule added, edited and deleted in
// the drawer, and what that does to a cached package (the page lists the versions the gate holds
// back and why). The upstream is a stand-in on loopback; nothing leaves the machine.
const PKG = 'curated-pkg'
const PATTERN = 'curated-*'
const VERSION = '1.0.0'
const RULE_REASON = 'vetted by the e2e suite'

interface Rule {
  action: string
  package_pattern: string
  version_pattern: string
  reason: string
}

let upstream: FakeUpstream
let repo: string
const api = adminApi()

beforeAll(async () => {
  upstream = startFakeNpmUpstream([{ name: PKG, version: VERSION, published: '2020-01-01T00:00:00.000Z' }])
  repo = await createCachedNpmProxy(upstream, [PKG], 'e2e-curate')
})

afterAll(async () => {
  await upstream?.stop()
})

type Mode = 'off' | 'denylist' | 'allowlist'

const mode = (ui: Ui, m: Mode) => ui.getByTestId(`mode-${m}`)

/** Picks a mode and waits for the server's confirmation (the radio flips before the request ends). */
async function chooseMode(ui: Ui, m: Mode): Promise<void> {
  await mode(ui, m).click()
  await expectLoc(ui.getByText(tp('curation.modeSaved', { mode: t(`curation.mode.${m}`) }))).toBeVisible()
  await expectLoc(mode(ui, m)).toHaveAttribute('aria-checked', 'true')
}

async function curationMode(): Promise<string> {
  return (await repositoryOf(repo)).curation_mode as string
}

async function rules(): Promise<Rule[]> {
  const res = await api.get(`/api/v1/repositories/${repo}/curation-rules`)
  return res.json<{ rules: Rule[] }>().rules
}

describe('curation of a proxy repository', () => {
  const it = testAs('admin')

  it('starts with curation off and no rules, and an allowlist without an allow rule says what that means', async (ui) => {
    await ui.goto(`/repos/${repo}?tab=settings`)
    await expectLoc(ui.getByTestId('curation-section')).toBeVisible()
    await expectLoc(ui.getByTestId('curation-empty')).toBeVisible()
    await expectLoc(mode(ui, 'off')).toHaveAttribute('aria-checked', 'true')

    await chooseMode(ui, 'allowlist')
    await expectLoc(ui.getByTestId('allowlist-empty')).toContainText(t('curation.allowlistEmpty'))
    expect(await curationMode()).toBe('allowlist')

    await chooseMode(ui, 'off')
    await expectLoc(ui.getByTestId('allowlist-empty')).toHaveCount(0)
    expect(await curationMode()).toBe('off')
  })

  it('a block rule added in the drawer is listed, and the package page says its version is held back', async (ui) => {
    await ui.goto(`/repos/${repo}?tab=settings`)
    await chooseMode(ui, 'denylist')

    await ui.getByTestId('rule-add').click()
    const drawer = ui.getByRole('dialog', { name: t('curation.addTitle') })
    await expectLoc(drawer).toBeVisible()
    await drawer.getByTestId('rule-action-block').click()
    await expectLoc(drawer.getByTestId('rule-action-block')).toHaveAttribute('aria-checked', 'true')
    await drawer.getByLabel(t('curation.field.package')).fill(PATTERN)
    await drawer.getByLabel(t('curation.field.reason')).fill(RULE_REASON)
    await drawer.getByTestId('rule-save').click()

    await expectLoc(drawer).toBeHidden()
    await expectLoc(ui.getByTestId('rule-package')).toHaveText(PATTERN)
    expect(await rules()).toMatchObject([{ action: 'block', package_pattern: PATTERN, version_pattern: '', reason: RULE_REASON }])

    // The server agrees the version is unavailable, and names the rule.
    const verdict = await api.get(`/api/v1/repositories/${repo}/availability`, { params: { package: PKG, version: VERSION } })
    expect(verdict.json<{ available: boolean; reasons: Array<{ source: string }> }>()).toMatchObject({ available: false, reasons: [{ source: 'curation' }] })

    // The package page explains it: the version is listed as held back, with the rule's reason.
    await ui.goto(`/repos/${repo}/packages/${PKG}`)
    const held = ui.getByTestId('held-back-list')
    await expectLoc(held).toBeVisible()
    await expectLoc(held).toContainText(VERSION)
    await expectLoc(held).toContainText(tp('artifact.versions.blocked', { reason: RULE_REASON }))
  })

  it('editing the rule so it no longer matches releases the version', async (ui) => {
    await ui.goto(`/repos/${repo}?tab=settings`)
    await expectLoc(ui.getByTestId('rule-package')).toHaveText(PATTERN)
    await ui.getByTestId('action-edit').click()

    const drawer = ui.getByRole('dialog', { name: t('curation.editTitle') })
    await expectLoc(drawer).toBeVisible()
    await expectLoc(drawer.getByLabel(t('curation.field.package'))).toHaveValue(PATTERN)
    await drawer.getByLabel(t('curation.field.version')).fill('9.*')
    await drawer.getByTestId('rule-save').click()
    await expectLoc(drawer).toBeHidden()

    expect(await rules()).toMatchObject([{ version_pattern: '9.*' }])
    await ui.goto(`/repos/${repo}/packages/${PKG}`)
    await expectLoc(ui.getByTestId('metadata-only')).toBeVisible()
    await expectLoc(ui.getByTestId('held-back-list')).toHaveCount(0)
  })

  it('deleting the rule empties the list again', async (ui) => {
    await ui.goto(`/repos/${repo}?tab=settings`)
    await expectLoc(ui.getByTestId('rule-package')).toBeVisible()
    await ui.getByTestId('action-delete').click()

    const confirm = ui.getByRole('dialog', { name: t('curation.deleteTitle') })
    await expectLoc(confirm).toBeVisible()
    await confirm.getByRole('button', { name: t('common.delete') }).click()

    await expectLoc(ui.getByTestId('curation-empty')).toBeVisible()
    expect(await rules()).toEqual([])
    // The mode survives the last rule: an empty denylist serves everything.
    expect(await curationMode()).toBe('denylist')
  })
})

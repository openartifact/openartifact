// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeAll, describe, expect } from 'bun:test'
import { adminApi, uniqueName } from '../lib/api'
import { testAs } from '../lib/browser'
import { expectLoc, t, type Ui } from '../lib/ui'

// The activity tab of a repository: a file uploaded through the API shows up as an event, and what
// the viewer may see follows their role (an admin gets the settings, cleanup and federation
// filters and the events behind them; a reader gets the artifact events only).
const FILE = 'activity-probe.txt'
let repo: string

beforeAll(async () => {
  const api = adminApi()
  repo = uniqueName('e2e-activity')
  const created = await api.post('/api/v1/repositories', { data: { name: repo, format: 'raw', public: false } })
  expect(created.ok, created.text()).toBe(true)
  const upload = await api.put(`/repos/raw/${repo}/${FILE}`, { data: 'activity probe\n', headers: { 'Content-Type': 'text/plain' } })
  expect(upload.ok, upload.text()).toBe(true)
})

const rows = (ui: Ui) => ui.getByTestId('activity-row')
const category = (ui: Ui, c: string) => ui.locator(`[data-testid="activity-filter"] [data-category="${c}"]`)

describe('repository activity as an admin', () => {
  const it = testAs('admin')

  it('lists the upload and the repository creation, offers every event type, and filters by it', async (ui) => {
    await ui.goto(`/repos/${repo}?tab=activity`)
    await expectLoc(rows(ui)).toHaveCount(2)
    await expectLoc(rows(ui).filter({ hasText: FILE })).toHaveCount(1)
    await expectLoc(rows(ui).filter({ hasText: FILE }).getByTestId('activity-actor')).toHaveText('admin')

    for (const c of ['uploads', 'deletions', 'trash', 'promotions', 'quarantine', 'cleanup', 'settings', 'federation']) {
      await expectLoc(category(ui, c)).toBeVisible()
    }

    // Only the upload remains under "Uploads"; "Settings" holds the creation.
    await category(ui, 'uploads').click()
    await expectLoc(category(ui, 'uploads')).toHaveAttribute('aria-pressed', 'true')
    await expectLoc(rows(ui)).toHaveCount(1)
    await expectLoc(rows(ui)).toContainText(FILE)

    await category(ui, 'settings').click()
    await expectLoc(rows(ui)).toHaveCount(1)
    await expectLoc(rows(ui)).toContainText(repo)

    // An event type with nothing in it says so and offers the way back.
    await category(ui, 'federation').click()
    await expectLoc(ui.getByTestId('activity-empty')).toBeVisible()
    await ui.getByTestId('activity-empty').getByRole('button', { name: t('ui.filter.clear') }).click()
    await expectLoc(rows(ui)).toHaveCount(2)
  })
})

describe('repository activity as a developer', () => {
  const it = testAs('dev')

  it('shows the upload but neither the settings events nor the admin-only event types', async (ui) => {
    await ui.goto(`/repos/${repo}?tab=activity`)
    await expectLoc(rows(ui)).toHaveCount(1)
    await expectLoc(rows(ui)).toContainText(FILE)

    for (const c of ['uploads', 'deletions', 'trash', 'promotions', 'quarantine']) {
      await expectLoc(category(ui, c)).toBeVisible()
    }
    for (const c of ['cleanup', 'settings', 'federation']) {
      await expectLoc(category(ui, c)).toHaveCount(0)
    }
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { anonApi } from '../lib/api'
import { testAs } from '../lib/browser'
import { expectLoc, shellReady, t } from '../lib/ui'

const REASON = t('shell.requiresAdmin')

describe('administrator', () => {
  const it = testAs('admin')

  it('sees every area, the create button and every settings section', async (ui) => {
    await ui.goto('/')
    await shellReady(ui)
    for (const id of ['home', 'repos', 'packages', 'projects', 'bundles', 'security', 'settings']) {
      await expectLoc(ui.getByTestId(`nav-${id}`)).toHaveAttribute('href', /^\//)
    }
    await expectLoc(ui.getByTestId('nav-reason')).toHaveCount(0)

    await ui.goto('/repos')
    await expectLoc(ui.getByTestId('new-repo').first()).toBeVisible()

    await ui.goto('/settings')
    await expectLoc(ui.getByRole('link', { name: new RegExp(t('ui.settings.users.title')) })).toBeVisible()
    await expectLoc(ui.getByTestId('hub-locked')).toHaveCount(0)
    await expectLoc(ui.getByTestId('hub-managed-note')).toHaveCount(0)
  })

  it('opens the admin-only pages', async (ui) => {
    await ui.goto('/bundles')
    await expectLoc(ui.getByRole('heading', { level: 1 })).toBeVisible()
    expect(ui.url().pathname).toBe('/bundles')
    await ui.goto('/settings/users')
    await expectLoc(ui.getByRole('heading', { level: 1 })).toBeVisible()
    expect(ui.url().pathname).toBe('/settings/users')
  })

  it('can quarantine and delete from an artifact page', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    await ui.getByTestId('artifact-menu').click()
    await expectLoc(ui.getByTestId('action-quarantine')).toBeVisible()
    await expectLoc(ui.getByTestId('action-delete')).toBeVisible()
  })
})

describe('developer', () => {
  const it = testAs('dev')

  it('has the areas of a member and Bundles locked with the reason', async (ui) => {
    await ui.goto('/')
    await shellReady(ui)
    for (const id of ['home', 'repos', 'packages', 'projects', 'security', 'settings']) {
      await expectLoc(ui.getByTestId(`nav-${id}`)).toHaveAttribute('href', /^\//)
    }
    const bundles = ui.getByTestId('nav-bundles')
    // Kept in the list as a disabled link without a destination, saying why.
    await expectLoc(bundles).toHaveAttribute('aria-disabled', 'true')
    await expectLoc(bundles).not.toHaveAttribute('href', /.+/)
    await expectLoc(bundles.getByTestId('nav-reason')).toHaveText(REASON)
    await expectLoc(ui.getByTestId('nav-repos')).not.toHaveAttribute('aria-disabled', 'true')
  })

  it('is sent back to the dashboard from an admin page, and told why', async (ui) => {
    await ui.goto('/bundles')
    await expectLoc(ui.getByText(t('shell.denied.admin'))).toBeVisible()
    await ui.expectUrl((url) => url.pathname === '/')

    await ui.goto('/settings/users')
    await expectLoc(ui.getByText(t('shell.denied.admin'))).toBeVisible()
    await ui.expectUrl((url) => url.pathname === '/')
  })

  it('sees the settings hub with admin sections locked, and still opens their own', async (ui) => {
    await ui.goto('/settings')
    await expectLoc(ui.getByTestId('hub-locked').first()).toContainText(REASON)
    await expectLoc(ui.getByTestId('hub-managed-note')).toBeVisible()
    await ui.goto('/settings/profile')
    await expectLoc(ui.getByRole('heading', { level: 1 })).toBeVisible()
    expect(ui.url().pathname).toBe('/settings/profile')
  })

  it('cannot create repositories and is not offered to', async (ui) => {
    await ui.goto('/repos')
    await expectLoc(ui.getByRole('heading', { level: 1 })).toBeVisible()
    await expectLoc(ui.getByTestId('new-repo')).toHaveCount(0)
  })

  it('has no quarantine action on an artifact page', async (ui) => {
    await ui.goto('/repos/seed-raw/packages/hello.txt')
    await expectLoc(ui.getByRole('heading', { level: 1, name: 'hello.txt' })).toBeVisible()
    await expectLoc(ui.getByTestId('action-quarantine')).toHaveCount(0)
  })
})

describe('anonymous visitor', () => {
  const it = testAs('anon')

  it('is sent to sign-in from every area, remembering where to return', async (ui) => {
    for (const path of ['/', '/repos', '/packages', '/projects', '/security', '/settings', '/repos/seed-raw']) {
      await ui.goto(path)
      await ui.expectUrl((url) => url.pathname === '/login')
      expect(decodeURIComponent(ui.url().searchParams.get('next') ?? '')).toBe(path)
      await expectLoc(ui.getByTestId('user-menu')).toHaveCount(0)
    }
  })

  it('reads a public repository natively but nothing private, and writes nothing', async () => {
    const api = anonApi()
    const hello = await api.get('/repos/raw/seed-raw/hello.txt')
    expect(hello.status).toBe(200)
    expect(hello.text().length).toBeGreaterThan(0)

    expect((await api.get('/repos/seed-npm-private/anything')).status).toBe(401)
    expect((await api.get('/api/v1/repositories')).status).toBe(401)
    expect((await api.put('/repos/raw/seed-raw/anon-write.txt', { data: 'nope' })).status).toBe(401)
    expect((await api.post('/api/v1/repositories', { data: { name: 'anon-repo', format: 'raw' } })).status).toBe(401)
    expect((await api.get('/repos/raw/seed-raw/anon-write.txt')).status).toBe(404)
  })
})

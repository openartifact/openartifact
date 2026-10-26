// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { expect, test } from '../fixtures'
import { anonApi } from '../lib/api'
import { shellReady, t } from '../lib/ui'

const REASON = t('shell.requiresAdmin')

test.describe('administrator', () => {
  test.use({ role: 'admin' })

  test('sees every area, the create button and every settings section', async ({ page }) => {
    await page.goto('/')
    await shellReady(page)
    for (const id of ['home', 'repos', 'packages', 'projects', 'bundles', 'security', 'settings']) {
      await expect(page.getByTestId(`nav-${id}`)).toHaveAttribute('href', /^\//)
    }
    await expect(page.getByTestId('nav-reason')).toHaveCount(0)

    await page.goto('/repos')
    await expect(page.getByTestId('new-repo').first()).toBeVisible()

    await page.goto('/settings')
    await expect(page.getByTestId('hub-locked')).toHaveCount(0)
    await expect(page.getByTestId('hub-managed-note')).toHaveCount(0)
    await expect(page.getByRole('link', { name: new RegExp(t('ui.settings.users.title')) })).toBeVisible()
  })

  test('opens the admin-only pages', async ({ page }) => {
    await page.goto('/bundles')
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    expect(new URL(page.url()).pathname).toBe('/bundles')
    await page.goto('/settings/users')
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    expect(new URL(page.url()).pathname).toBe('/settings/users')
  })

  test('can quarantine and delete from an artifact page', async ({ page }) => {
    await page.goto('/repos/seed-raw/packages/hello.txt')
    await page.getByTestId('artifact-menu').dispatchEvent('click')
    await expect(page.getByTestId('action-quarantine')).toBeVisible()
    await expect(page.getByTestId('action-delete')).toBeVisible()
  })
})

test.describe('developer', () => {
  test.use({ role: 'dev' })

  test('has the areas of a member and Bundles locked with the reason', async ({ page }) => {
    await page.goto('/')
    await shellReady(page)
    for (const id of ['home', 'repos', 'packages', 'projects', 'security', 'settings']) {
      await expect(page.getByTestId(`nav-${id}`)).toHaveAttribute('href', /^\//)
    }
    const bundles = page.getByTestId('nav-bundles')
    // Kept in the list as a disabled link without a destination, saying why.
    await expect(bundles).toHaveAttribute('aria-disabled', 'true')
    await expect(bundles).not.toHaveAttribute('href', /.+/)
    await expect(bundles.getByTestId('nav-reason')).toHaveText(REASON)
    await expect(page.getByTestId('nav-repos')).not.toHaveAttribute('aria-disabled', 'true')
  })

  test('is sent back to the dashboard from an admin page, and told why', async ({ page }) => {
    await page.goto('/bundles')
    await expect(page.getByText(t('shell.denied.admin'))).toBeVisible()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/')

    await page.goto('/settings/users')
    await expect(page.getByText(t('shell.denied.admin'))).toBeVisible()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/')
  })

  test('sees the settings hub with admin sections locked, and still opens their own', async ({ page }) => {
    await page.goto('/settings')
    await expect(page.getByTestId('hub-locked').first()).toContainText(REASON)
    await expect(page.getByTestId('hub-managed-note')).toBeVisible()
    await page.goto('/settings/profile')
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    expect(new URL(page.url()).pathname).toBe('/settings/profile')
  })

  test('cannot create repositories and is not offered to', async ({ page }) => {
    await page.goto('/repos')
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    await expect(page.getByTestId('new-repo')).toHaveCount(0)
  })

  test('has no quarantine action on an artifact page', async ({ page }) => {
    await page.goto('/repos/seed-raw/packages/hello.txt')
    await expect(page.getByRole('heading', { level: 1, name: 'hello.txt' })).toBeVisible()
    await expect(page.getByTestId('action-quarantine')).toHaveCount(0)
  })
})

test.describe('anonymous visitor', () => {
  test.use({ role: 'anon' })

  test('is sent to sign-in from every area, remembering where to return', async ({ page }) => {
    for (const path of ['/', '/repos', '/packages', '/projects', '/security', '/settings', '/repos/seed-raw']) {
      await page.goto(path)
      await expect.poll(() => new URL(page.url()).pathname).toBe('/login')
      expect(decodeURIComponent(new URL(page.url()).searchParams.get('next') ?? '')).toBe(path)
      await expect(page.getByTestId('user-menu')).toHaveCount(0)
    }
  })

  test('reads a public repository natively but nothing private, and writes nothing', async () => {
    const api = await anonApi()
    try {
      const hello = await api.get('/repos/raw/seed-raw/hello.txt')
      expect(hello.status()).toBe(200)
      expect((await hello.text()).length).toBeGreaterThan(0)

      expect((await api.get('/repos/seed-npm-private/anything')).status()).toBe(401)
      expect((await api.get('/api/v1/repositories')).status()).toBe(401)
      expect((await api.put('/repos/raw/seed-raw/anon-write.txt', { data: 'nope' })).status()).toBe(401)
      expect((await api.post('/api/v1/repositories', { data: { name: 'anon-repo', format: 'raw' } })).status()).toBe(401)
      expect((await api.get('/repos/raw/seed-raw/anon-write.txt')).status()).toBe(404)
    } finally {
      await api.dispose()
    }
  })
})

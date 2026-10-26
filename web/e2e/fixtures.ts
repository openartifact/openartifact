// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { chromium, test as base, type Browser } from '@playwright/test'
import { CDP_URL } from './env'
import { authFile, type Role } from './lib/auth'

export { expect } from '@playwright/test'
export type { Role } from './lib/auth'

interface Options {
  /** Who the page is signed in as; `anon` is a visitor without a session. */
  role: Role
}

/**
 * `test` with two changes to the stock fixtures:
 *  - `browser` is a Lightpanda server reached over CDP instead of a launched
 *    Chromium, so no browser is downloaded or started by the runner;
 *  - `storageState` follows `role` (cookies written by global setup).
 * Lightpanda allows a single browser context per connection, so the stock
 * per-test `context` (closed after each test) fits as long as workers is 1.
 */
export const test = base.extend<Options, { browser: Browser }>({
  browser: [
    async ({}, use) => {
      const browser = await chromium.connectOverCDP(CDP_URL)
      await use(browser)
      await browser.close()
    },
    { scope: 'worker', timeout: 30_000 },
  ],
  role: ['anon', { option: true }],
  storageState: async ({ role }, use) => {
    await use(role === 'anon' ? undefined : authFile(role))
  },
})

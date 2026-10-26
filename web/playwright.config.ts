// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineConfig } from '@playwright/test'
import { requireEnv } from './e2e/env'

// Black-box UI suite. It drives a real server (built with the SPA embedded,
// see scripts/testenv.sh) through Lightpanda over CDP: there is no
// Playwright-managed browser (see e2e/fixtures.ts). Lightpanda serves one
// browser context per connection and one page per context, so tests run
// serially in a single worker.
//
//   scripts/testenv.sh start && . "$OA_TESTENV_DIR/env"
//   lightpanda serve --host 127.0.0.1 --port 9222 --cdp-max-message-size 8388608
//   bun run e2e
export default defineConfig({
  testDir: './e2e',
  testMatch: /specs\/.*\.e2e\.ts$/,
  globalSetup: './e2e/global-setup.ts',
  outputDir: './test-results',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]] : [['list']],
  use: {
    baseURL: requireEnv('OA_URL'),
    // Lightpanda has no rendering: no screenshots, DOM snapshots only.
    trace: { mode: 'retain-on-failure', screenshots: false, snapshots: true },
    screenshot: 'off',
    video: 'off',
    actionTimeout: 10_000,
    navigationTimeout: 20_000,
  },
})

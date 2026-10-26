// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { defineConfig } from '@playwright/test'
import { requireEnv } from './e2e/env'
import { parseSeed, randomSeed } from './e2e/lib/prng'

// Random-walk ("monkey") test of the UI, run nightly. Same server and browser
// as the e2e suite (see playwright.config.ts); see e2e/monkey.e2e.ts for what
// the walk does and what it never clicks.
//
//   MONKEY_SEED   pin a seed to replay a run (default: a random one, printed)
//   MONKEY_STEPS  steps per role (default 150)
//   MONKEY_MAX_SECONDS  wall-clock bound per role (default 180)
//
//   bun run monkey
//
// The seed is fixed here, once, so every worker and role derives from the same
// printed value, and written to monkey-seed.txt for CI to upload.
const seed = process.env.MONKEY_SEED ? parseSeed(process.env.MONKEY_SEED) : randomSeed()
process.env.MONKEY_SEED = String(seed)
if (!process.env.TEST_WORKER_INDEX) {
  console.log(`MONKEY_SEED=${seed}  (replay: MONKEY_SEED=${seed} bun run monkey)`)
  writeFileSync(join(import.meta.dirname, 'monkey-seed.txt'), `${seed}\n`)
}

const maxSeconds = Number(process.env.MONKEY_MAX_SECONDS ?? 180)

export default defineConfig({
  testDir: './e2e',
  testMatch: /monkey\.e2e\.ts$/,
  globalSetup: './e2e/global-setup.ts',
  outputDir: './test-results',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: (maxSeconds + 120) * 1000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]] : [['list']],
  use: {
    baseURL: requireEnv('OA_URL'),
    trace: { mode: 'retain-on-failure', screenshots: false, snapshots: true },
    screenshot: 'off',
    video: 'off',
    actionTimeout: 10_000,
    navigationTimeout: 20_000,
  },
})

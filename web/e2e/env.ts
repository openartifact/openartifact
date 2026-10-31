// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/** An environment variable the suites cannot run without (exported by scripts/testenv.sh). */
export function requireEnv(name: string): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is not set: run scripts/testenv.sh start and source $OA_TESTENV_DIR/env`)
  return value
}

/** CDP endpoint of the Lightpanda server (`lightpanda serve`). */
export const CDP_URL = process.env.LIGHTPANDA_CDP_URL ?? 'http://127.0.0.1:9222'

/** CDP endpoint of the Chromium that renders the visual and contrast specs (`bun run e2e:chromium`). */
export const CHROMIUM_CDP_URL = process.env.CHROMIUM_CDP_URL ?? 'http://127.0.0.1:9223'

export interface Account {
  username: string
  password: string
}

export const ADMIN: () => Account = () => ({ username: requireEnv('OA_ADMIN_USER'), password: requireEnv('OA_ADMIN_PASS') })
export const DEV: () => Account = () => ({ username: requireEnv('OA_DEV_USER'), password: requireEnv('OA_DEV_PASS') })

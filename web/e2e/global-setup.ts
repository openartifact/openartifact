// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { mkdirSync } from 'node:fs'
import { request } from '@playwright/test'
import { ADMIN, DEV, requireEnv, type Account } from './env'
import { authFile } from './lib/auth'

// Signs every role in once through the API (the UI login form has its own
// specs) and stores the session cookies, so a spec starts already signed in.
async function signIn(role: string, account: Account): Promise<void> {
  const ctx = await request.newContext({ baseURL: requireEnv('OA_URL') })
  try {
    const res = await ctx.post('/api/v1/auth/login', { data: account })
    if (!res.ok()) throw new Error(`login as ${role} failed: HTTP ${res.status()}`)
    await ctx.storageState({ path: authFile(role) })
  } finally {
    await ctx.dispose()
  }
}

export default async function globalSetup(): Promise<void> {
  mkdirSync(authFile('admin').replace(/[^/]+$/, ''), { recursive: true })
  await signIn('admin', ADMIN())
  await signIn('dev', DEV())
}

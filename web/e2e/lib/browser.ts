// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { test } from 'bun:test'
import puppeteer, { type Browser } from 'puppeteer-core'
import { ADMIN, CDP_URL, DEV, requireEnv, type Account } from '../env'
import { login, sessionCookies } from './api'
import { Ui } from './ui'
import { eventually } from './wait'
import { problems, watch } from './watch'

/** Roles the suites run as; `anon` carries no session. */
export type Role = 'admin' | 'dev' | 'anon'

const ACCOUNTS: Record<Exclude<Role, 'anon'>, () => Account> = { admin: ADMIN, dev: DEV }

/** Where failure reports are written (git-ignored; uploaded by CI). */
export const RESULTS_DIR = join(import.meta.dirname, '..', '..', 'test-results')

let connection: Promise<Browser> | undefined

/** The Lightpanda server, connected once per test process. */
export function browser(): Promise<Browser> {
  connection ??= puppeteer.connect({ browserURL: CDP_URL })
  return connection
}

interface CookieData {
  name: string
  value: string
  httpOnly: boolean
}

const sessions = new Map<Role, Promise<CookieData[]>>()

/**
 * The session cookies of a role, from one API login per process. The UI login
 * form has its own specs; everything else starts signed in, as a returning
 * visitor does.
 */
function cookiesOf(role: Role): Promise<CookieData[]> {
  if (role === 'anon') return Promise.resolve([])
  let pending = sessions.get(role)
  if (pending === undefined) {
    const account = ACCOUNTS[role]()
    pending = login(account.username, account.password).then(sessionCookies)
    sessions.set(role, pending)
  }
  return pending
}

/**
 * Runs `fn` against a fresh page of its own browser context, signed in as
 * `role`, and closes the context afterwards. Lightpanda allows one browser
 * context per connection, so the next test waits until this one is gone.
 * A failure leaves a report in RESULTS_DIR before it propagates.
 */
export async function withUi(role: Role, name: string, fn: (ui: Ui) => Promise<void>): Promise<void> {
  const origin = new URL(requireEnv('OA_URL')).origin
  const connected = await browser()
  const context = await eventually(() => connected.createBrowserContext(), 15_000)
  try {
    const cookies = await cookiesOf(role)
    if (cookies.length > 0) {
      await context.setCookie(...cookies.map((c) => ({ name: c.name, value: c.value, domain: new URL(origin).hostname, path: '/', httpOnly: c.httpOnly })))
    }
    const page = await context.newPage()
    const ui = new Ui(page, origin, watch(page))
    try {
      await fn(ui)
    } catch (err) {
      await report(name, ui, err)
      throw err
    }
  } finally {
    await context.close().catch((err: unknown) => console.error(`closing the browser context failed: ${String(err)}`))
  }
}

/** `test` that runs as `role` in a browser page of its own. */
export function testAs(role: Role) {
  return (name: string, fn: (ui: Ui) => Promise<void>, timeoutMs = 90_000): void => {
    test(name, () => withUi(role, name, fn), timeoutMs)
  }
}

async function report(name: string, ui: Ui, err: unknown): Promise<void> {
  const dir = join(RESULTS_DIR, name.replace(/[^A-Za-z0-9._-]+/g, '_').slice(0, 120))
  const lines: string[] = [`test: ${name}`, `url: ${ui.page.url()}`, `error: ${err instanceof Error ? (err.stack ?? err.message) : String(err)}`]
  const found = problems(ui.findings)
  if (found.length > 0) lines.push('page reported:', ...found.map((p) => `  ${p}`))
  try {
    // Lightpanda's own account of the page: HTTP status, JS errors, resources it skipped.
    const session = await ui.page.createCDPSession()
    const status = await session.send('LP.getPageStatus' as never)
    lines.push(`LP.getPageStatus: ${JSON.stringify(status)}`)
  } catch (statusErr) {
    lines.push(`LP.getPageStatus unavailable: ${String(statusErr)}`)
  }
  let html = ''
  try {
    html = await ui.page.content()
  } catch (htmlErr) {
    html = `<!-- page content unavailable: ${String(htmlErr)} -->`
  }
  console.error(lines.join('\n'))
  await mkdir(dir, { recursive: true })
  await writeFile(join(dir, 'failure.txt'), `${lines.join('\n')}\n`)
  await writeFile(join(dir, 'console.log'), `${ui.findings.console.join('\n')}\n`)
  await writeFile(join(dir, 'page.html'), html)
}

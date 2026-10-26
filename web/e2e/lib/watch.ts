// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Page } from 'puppeteer-core'

/** What a page reported about itself while it was driven. */
export interface Findings {
  /** Uncaught exceptions. */
  pageErrors: string[]
  /** `console.error` output, minus the browser's own lines for 4xx responses (refusals are the API working). */
  consoleErrors: string[]
  /** Responses with status >= 500. */
  serverErrors: string[]
  /** Every console line, for the failure report. */
  console: string[]
}

/** Starts recording; the lists grow as the page runs. */
export function watch(page: Page): Findings {
  const findings: Findings = { pageErrors: [], consoleErrors: [], serverErrors: [], console: [] }
  page.on('pageerror', (err) => findings.pageErrors.push(err instanceof Error ? err.message : String(err)))
  page.on('console', (msg) => {
    const text = msg.text()
    findings.console.push(`${msg.type()}: ${text}`)
    if (msg.type() !== 'error') return
    if (/Failed to load resource: the server responded with a status of 4\d\d/.test(text)) return
    findings.consoleErrors.push(text)
  })
  page.on('response', (res) => {
    if (res.status() >= 500) findings.serverErrors.push(`${res.status()} ${res.request().method()} ${res.url()}`)
  })
  return findings
}

/** The findings as one list of lines; empty when the page behaved. */
export function problems(f: Findings): string[] {
  return [
    ...f.pageErrors.map((m) => `pageerror: ${m}`),
    ...f.consoleErrors.map((m) => `console error: ${m}`),
    ...f.serverErrors.map((m) => `server error: ${m}`),
  ]
}

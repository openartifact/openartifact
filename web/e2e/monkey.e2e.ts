// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Page } from '@playwright/test'
import { expect, test, type Role } from './fixtures'
import { createPrng, deriveSeed, parseSeed, type Prng } from './lib/prng'
import { activate, primaryNav, shellReady, t } from './lib/ui'

/**
 * Random walk over the whole UI, as an administrator and as a developer.
 *
 * For each top-level area of the sidebar the walk enters it and then takes
 * random steps: it follows a visible link, presses a visible button or types
 * into a search box / filter. Every random choice comes from one seeded PRNG
 * over candidates in a fixed order (kind, label, href), so the same seed on the
 * same data replays the same walk. The seed is printed first, and is part of
 * every failure message.
 *
 * It fails on:
 *  - an uncaught exception in the page (`pageerror`);
 *  - a console error (the browser's "Failed to load resource" lines for 4xx are
 *    ignored: refusals are the API working, and 5xx are policed below);
 *  - any HTTP response with status >= 500;
 *  - landing on the not-found page by following an in-app link.
 *
 * What it never touches (DENY_*): anything that destroys or changes state
 * (delete, remove, revoke, disable, restore, empty trash, quarantine, release,
 * promote, seal, publish, upload, import, backup, save, apply, update, create,
 * invite, confirm, sign out, password changes, token rotation...), buttons styled
 * as dangerous, links that leave the SPA (API, downloads, new tabs). Text is only
 * typed into search boxes and filters, and it is garbage on purpose.
 */

const STEPS = positiveInt(process.env.MONKEY_STEPS, 150)
const MAX_MS = positiveInt(process.env.MONKEY_MAX_SECONDS, 180) * 1000

/** Accessible names (or test ids) of controls the walk must never press. */
export const DENY_LABELS =
  /\b(delete|remove|revoke|disable|deactivate|enable|activate|restore|purge|empty|destroy|wipe|clear|reset|rotate|regenerate|generate|quarantine|release|approve|reject|suppress|promote|seal|publish|upload|import|migrate|backup|scan|retry|trigger|run|save|apply|update|create|add|new|invite|submit|confirm|accept|grant|assign|unlock|lock|send|undo|sign[ -]?out|log[ -]?out|logout|password|token|copy)\b/i
export const DENY_TESTIDS = /(sign-out|logout|delete|remove|revoke|disable|restore|destroy|danger|quarantine|new-repo)/i
/** Top-level path segments of the SPA; every other href is outside it (API, registries, downloads). */
export const SPA_AREAS = ['', 'repos', 'packages', 'projects', 'bundles', 'security', 'settings']
/** Inputs that take free text but only when they are a search box or a filter. */
export const SEARCHABLE = /search|filter|query|find/i

/** Text a user would never type, to shake out parsing, escaping and length problems. */
const GARBAGE = [
  '\'"`;--',
  '<script>alert(1)</script>',
  '%00%0a%0d',
  '../../../etc/passwd',
  '{{7*7}}${7*7}',
  '𝔘𝔫𝔦𝔠𝔬𝔡𝔢 ☃ \u202e rtl 🐒',
  ' ',
  '%',
  '*',
  '\\',
  'a'.repeat(3000),
  'x'.repeat(300) + '/' + 'y'.repeat(300),
  '-1',
  '1e999',
  'null',
  '@scope/name@^1.x || >=2',
  '(?i)(a+)+$',
]

interface Candidate {
  /** Index stamped on the element as `data-monkey-id` while it is collected. */
  id: number
  kind: 'link' | 'button' | 'input'
  label: string
  href: string
}

interface Findings {
  pageErrors: string[]
  consoleErrors: string[]
  serverErrors: string[]
}

function positiveInt(raw: string | undefined, fallback: number): number {
  if (raw === undefined || raw === '') return fallback
  const n = Number(raw)
  if (!Number.isInteger(n) || n <= 0) throw new Error(`expected a positive integer, got "${raw}"`)
  return n
}

/** Records what the page reports about itself; the walk fails on the first non-empty list. */
function watch(page: Page): Findings {
  const findings: Findings = { pageErrors: [], consoleErrors: [], serverErrors: [] }
  page.on('pageerror', (err) => findings.pageErrors.push(err.message))
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    const text = msg.text()
    if (/Failed to load resource: the server responded with a status of 4\d\d/.test(text)) return
    findings.consoleErrors.push(text)
  })
  page.on('response', (res) => {
    if (res.status() >= 500) findings.serverErrors.push(`${res.status()} ${res.request().method()} ${res.url()}`)
  })
  return findings
}

function problems(f: Findings): string[] {
  return [
    ...f.pageErrors.map((m) => `pageerror: ${m}`),
    ...f.consoleErrors.map((m) => `console error: ${m}`),
    ...f.serverErrors.map((m) => `server error: ${m}`),
  ]
}

/**
 * Collects the controls the walk may use. Runs inside the page in one round
 * trip: scope is the open dialog if there is one, else the main content and
 * the header (the sidebar is entered deliberately, not wandered into).
 */
async function collect(page: Page): Promise<{ candidates: Candidate[]; dialog: boolean }> {
  return page.evaluate(
    ({ denyLabels, denyTestids, areas, searchable }) => {
      const deny = new RegExp(denyLabels.source, denyLabels.flags)
      const denyId = new RegExp(denyTestids.source, denyTestids.flags)
      const search = new RegExp(searchable.source, searchable.flags)
      for (const el of document.querySelectorAll('[data-monkey-id]')) el.removeAttribute('data-monkey-id')

      const shown = (el: Element): boolean => {
        for (let n: Element | null = el; n; n = n.parentElement) {
          if ((n as HTMLElement).hidden || n.getAttribute('aria-hidden') === 'true' || n.hasAttribute('inert')) return false
          const cs = getComputedStyle(n)
          if (cs.display === 'none' || cs.visibility === 'hidden') return false
        }
        return true
      }
      const nameOf = (el: Element): string =>
        (el.getAttribute('aria-label') || el.textContent || el.getAttribute('title') || '').replace(/\s+/g, ' ').trim().slice(0, 80)

      const dialogs = [...document.querySelectorAll('[role="dialog"]')].filter(shown)
      const dialog = dialogs.length > 0
      const roots: Element[] = dialog ? [dialogs[dialogs.length - 1] as Element] : [...document.querySelectorAll('main, header')]

      const seen = new Set<string>()
      const found: Array<{ el: Element; kind: 'link' | 'button' | 'input'; label: string; href: string; key: string }> = []
      const add = (el: Element, kind: 'link' | 'button' | 'input', label: string, href: string): void => {
        const key = `${kind}|${label}|${href}`
        if (seen.has(key)) return
        seen.add(key)
        found.push({ el, kind, label, href, key })
      }

      for (const root of roots) {
        // The header's only link is the logo; the walk reaches areas through the sidebar.
        for (const el of root.tagName === 'HEADER' ? [] : root.querySelectorAll('a[href]')) {
          if (!shown(el) || el.hasAttribute('download') || (el.getAttribute('target') ?? '') !== '' || el.getAttribute('aria-disabled') === 'true') continue
          const href = el.getAttribute('href') ?? ''
          if (!href.startsWith('/') || href.startsWith('//')) continue
          if (!areas.includes(href.split(/[?#]/)[0]?.split('/')[1] ?? '')) continue
          const label = nameOf(el)
          if (deny.test(label) || denyId.test(el.getAttribute('data-testid') ?? '')) continue
          add(el, 'link', label, href)
        }
        for (const el of root.querySelectorAll('button, [role="tab"], [role="menuitem"]')) {
          if (!shown(el) || (el as HTMLButtonElement).disabled || el.getAttribute('aria-disabled') === 'true') continue
          if ((el as HTMLButtonElement).type === 'submit') continue
          const label = nameOf(el)
          if (label === '' || deny.test(label)) continue
          if (denyId.test(el.getAttribute('data-testid') ?? '') || /danger/.test(el.getAttribute('class') ?? '')) continue
          add(el, 'button', label, '')
        }
        if (!dialog) {
          for (const el of root.querySelectorAll('input, textarea')) {
            const type = (el.getAttribute('type') ?? 'text').toLowerCase()
            if (!['text', 'search', ''].includes(type) || !shown(el) || (el as HTMLInputElement).disabled || (el as HTMLInputElement).readOnly) continue
            const label = `${el.getAttribute('aria-label') ?? ''} ${el.getAttribute('placeholder') ?? ''} ${el.getAttribute('name') ?? ''} ${type}`.trim()
            if (type !== 'search' && !search.test(label)) continue
            add(el, 'input', label, '')
          }
        }
      }
      found.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0))
      for (const [i, c] of found.entries()) c.el.setAttribute('data-monkey-id', String(i))
      return { candidates: found.map((c, i) => ({ id: i, kind: c.kind, label: c.label, href: c.href })), dialog }
    },
    { denyLabels: DENY_LABELS, denyTestids: DENY_TESTIDS, areas: SPA_AREAS, searchable: SEARCHABLE },
  )
}

/** Picks the kind first (so a page with 80 links does not drown its three buttons), then an element. */
function choose(prng: Prng, candidates: Candidate[]): Candidate | null {
  const kinds = ['link', 'button', 'input'] as const
  const byKind = kinds.map((k) => candidates.filter((c) => c.kind === k))
  const weights = [6, 3, 1.5].map((w, i) => ((byKind[i] ?? []).length > 0 ? w : 0))
  if (weights.every((w) => w === 0)) return null
  return prng.pick(byKind[prng.weighted(weights)] ?? [])
}

class MonkeyFailure extends Error {}

async function walk(page: Page, role: Role, seed: number, log: string[]): Promise<void> {
  const prng = createPrng(deriveSeed(seed, role))
  const findings = watch(page)
  const started = Date.now()
  let step = 0
  let stepStart = Date.now()

  const fail = async (why: string): Promise<never> => {
    throw new MonkeyFailure(
      [
        `${why}`,
        `  seed=${seed} role=${role} step=${step} url=${page.url()}`,
        `  replay: MONKEY_SEED=${seed} MONKEY_STEPS=${STEPS} bun run monkey`,
        `  last actions:`,
        ...log.slice(-15).map((l) => `    ${l}`),
      ].join('\n'),
    )
  }
  const check = async (): Promise<void> => {
    const found = problems(findings)
    if (found.length > 0) await fail(`the UI reported ${found.length} problem(s):\n${found.map((p) => `    ${p}`).join('\n')}`)
  }

  await page.goto('/')
  await shellReady(page)
  await check()

  // The areas of the sidebar, in its order. A locked area (no destination) is not an area for this role.
  const areas = await primaryNav(page)
    .getByRole('link')
    .evaluateAll((links) => links.map((a) => ({ id: a.getAttribute('data-testid') ?? '', href: a.getAttribute('href') ?? '' })).filter((a) => a.href !== ''))
  expect(areas.length, 'sidebar areas').toBeGreaterThanOrEqual(5)
  log.push(`areas: ${areas.map((a) => a.id).join(', ')}`)

  const perArea = Math.max(1, Math.ceil(STEPS / areas.length))
  const enter = async (area: { id: string; href: string }): Promise<void> => {
    log.push(`enter ${area.id} (${area.href})`)
    await activate(page.getByTestId(area.id))
    await page.waitForLoadState('networkidle')
    await expect(page.getByTestId('user-menu')).toBeVisible()
    await check()
  }

  for (const area of areas) {
    await enter(area)
    for (let i = 0; i < perArea; i++) {
      if (Date.now() - started > MAX_MS) {
        log.push('stopped by the time bound')
        return
      }
      step++
      if (log.length > 0) log[log.length - 1] += ` [${Date.now() - stepStart}ms]`
      stepStart = Date.now()
      const { candidates, dialog } = await collect(page)

      // Now and then go back to the area's front page, and leave open dialogs with the keyboard.
      if (dialog && prng.next() < 0.3) {
        log.push(`step ${step}: Escape`)
        await page.keyboard.press('Escape')
        continue
      }
      if (!dialog && prng.next() < 0.08) {
        await enter(area)
        continue
      }
      const pick = choose(prng, candidates)
      if (pick === null) {
        if (dialog) {
          await page.keyboard.press('Escape')
          log.push(`step ${step}: Escape (nothing to use in the dialog)`)
        } else {
          await enter(area)
        }
        continue
      }

      const target = page.locator(`[data-monkey-id="${pick.id}"]`)
      if ((await target.count()) === 0) {
        log.push(`step ${step}: control vanished before use (${pick.kind} "${pick.label}"), skipped`)
        continue
      }
      try {
        if (pick.kind === 'input') {
          const text = prng.pick(GARBAGE)
          log.push(`step ${step}: type ${JSON.stringify(text.slice(0, 40))} into [${pick.label}]`)
          await target.fill(text, { timeout: 3000 })
          await target.press('Enter', { timeout: 3000 })
        } else {
          log.push(`step ${step}: ${pick.kind} "${pick.label}"${pick.href ? ` -> ${pick.href}` : ''}`)
          // Visible and enabled were established when collecting; a short timeout keeps a vanished control cheap.
          await target.dispatchEvent('click', undefined, { timeout: 2000 })
        }
      } catch (err) {
        // The page re-rendered between collecting and acting (data arrived): the control is gone, not broken.
        if ((await target.count()) > 0) throw err
        log.push(`step ${step}: control vanished, skipped`)
        continue
      }
      await page.waitForLoadState('networkidle')
      await check()
      if (pick.kind === 'link' && (await page.getByText(t('shell.notFoundTitle')).count()) > 0) {
        await fail(`the link "${pick.label}" (${pick.href}) leads to the not-found page`)
      }
    }
  }
  log.push(`finished ${step} steps`)
}

test.describe('monkey', () => {
  const seed = parseSeed(process.env.MONKEY_SEED ?? '')

  test.beforeAll(() => {
    console.log(`MONKEY_SEED=${seed} steps=${STEPS} max=${MAX_MS / 1000}s`)
  })

  for (const role of ['admin', 'dev'] as const) {
    test.describe(role, () => {
      test.use({ role })

      test(`random walk as ${role} (seed ${seed})`, async ({ page }, testInfo) => {
        const log: string[] = []
        try {
          await walk(page, role, seed, log)
        } finally {
          // Every step of the walk, kept with the report and traces for replay and triage.
          await testInfo.attach(`monkey-${role}-seed-${seed}.log`, { body: log.join('\n'), contentType: 'text/plain' })
          if (process.env.MONKEY_VERBOSE) console.log(log.join('\n'))
        }
      })
    })
  }
})

test.describe('monkey harness', () => {
  test.use({ role: 'admin' })

  // A walk that cannot see failures would pass forever. This proves the detectors fire in this browser.
  test('sees page errors, console errors and 5xx responses', async ({ page }) => {
    const findings = watch(page)
    await page.goto('/')
    await shellReady(page)
    expect(problems(findings)).toEqual([])

    await page.evaluate(() => {
      setTimeout(() => {
        throw new Error('canary-uncaught')
      }, 0)
      console.error('canary-console')
    })
    await expect.poll(() => findings.pageErrors.join('|')).toContain('canary-uncaught')
    await expect.poll(() => findings.consoleErrors.join('|')).toContain('canary-console')

    // A 5xx is reported too. Lightpanda must support request interception for this stand-in.
    await page.route('**/api/v1/canary-5xx', (route) => route.fulfill({ status: 503, body: 'canary' }))
    await page.evaluate(() => fetch('/api/v1/canary-5xx'))
    await expect.poll(() => findings.serverErrors.join('|')).toContain('503')
  })
})

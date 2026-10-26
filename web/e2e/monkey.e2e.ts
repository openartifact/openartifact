// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect } from 'bun:test'
import { mkdir, writeFile } from 'node:fs/promises'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { RESULTS_DIR, testAs, type Role } from './lib/browser'
import { createPrng, deriveSeed, parseSeed, randomSeed, type Prng } from './lib/prng'
import { expectLoc, primaryNav, shellReady, t, type Ui } from './lib/ui'
import { eventually } from './lib/wait'
import { problems } from './lib/watch'

/**
 * Random walk over the whole UI, as an administrator and as a developer.
 *
 * For each top-level area of the sidebar the walk enters it and then takes
 * random steps: it follows a visible link, presses a visible button or types
 * into a search box / filter. Every random choice comes from one seeded PRNG
 * over candidates in a fixed order (kind, label, href), so the same seed on the
 * same data replays the same walk. The seed is printed first, written to
 * monkey-seed.txt, and is part of every failure message.
 *
 * Controls are used through the DOM (a click event on the element, a value set
 * plus `input`/`change` events), never by coordinates: the browser has no layout
 * to hit-test against.
 *
 * It fails on:
 *  - an uncaught exception in the page (`pageerror`);
 *  - a console error (the browser's "Failed to load resource" lines for 4xx are
 *    ignored: refusals are the API working, and 5xx are policed below);
 *  - any HTTP response with status >= 500;
 *  - a navigation that ends outside the app (the `about:blank` fallback);
 *  - landing on the not-found page by following an in-app link.
 *
 * What it never touches (DENY_*): anything that destroys or changes state
 * (delete, remove, revoke, disable, restore, empty trash, quarantine, release,
 * promote, seal, publish, upload, import, backup, save, apply, update, create,
 * invite, confirm, sign out, password changes, token rotation...), buttons styled
 * as dangerous, links that leave the SPA (API, downloads, new tabs). Text is only
 * typed into search boxes and filters, and it is garbage on purpose. There is no
 * drag-and-drop and no contenteditable: the browser cannot drive either.
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

interface RegexArg {
  source: string
  flags: string
}

function positiveInt(raw: string | undefined, fallback: number): number {
  if (raw === undefined || raw === '') return fallback
  const n = Number(raw)
  if (!Number.isInteger(n) || n <= 0) throw new Error(`expected a positive integer, got "${raw}"`)
  return n
}

const asArg = (re: RegExp): RegexArg => ({ source: re.source, flags: re.flags })

/**
 * Collects the controls the walk may use. Runs inside the page in one round
 * trip: scope is the open dialog if there is one, else the main content and
 * the header (the sidebar is entered deliberately, not wandered into).
 */
async function collect(ui: Ui): Promise<{ candidates: Candidate[]; dialog: boolean }> {
  return ui.page.evaluate(
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
    { denyLabels: asArg(DENY_LABELS), denyTestids: asArg(DENY_TESTIDS), areas: SPA_AREAS, searchable: asArg(SEARCHABLE) },
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

async function walk(ui: Ui, role: Role, seed: number, log: string[]): Promise<void> {
  const prng = createPrng(deriveSeed(seed, role))
  const started = Date.now()
  let step = 0
  let stepStart = Date.now()

  const fail = (why: string): never => {
    throw new MonkeyFailure(
      [
        `${why}`,
        `  seed=${seed} role=${role} step=${step} url=${ui.page.url()}`,
        `  replay: MONKEY_SEED=${seed} MONKEY_STEPS=${STEPS} bun run monkey`,
        `  last actions:`,
        ...log.slice(-15).map((l) => `    ${l}`),
      ].join('\n'),
    )
  }
  const check = (): void => {
    // A load that ended outside the app is a navigation failure, not an empty page to wander on.
    if (!ui.page.url().startsWith(ui.origin)) fail(`the page left the app: ${ui.page.url()} (lightpanda #3395: about:blank fallback)`)
    const found = problems(ui.findings)
    if (found.length > 0) fail(`the UI reported ${found.length} problem(s):\n${found.map((p) => `    ${p}`).join('\n')}`)
  }
  /** Waits for the page to go quiet; a page that never does (polling) is noted, not failed. */
  const quiet = async (): Promise<void> => {
    try {
      // A short quiet period: a late response is caught by the next step's check.
      await ui.settled(100)
    } catch (err) {
      log.push(`  (network did not go idle: ${err instanceof Error ? err.message : String(err)})`)
    }
  }

  /** Escape, then wait for the dialog to leave: it closes through a transition, so it is still there for a moment. */
  const dismiss = async (why: string): Promise<void> => {
    log.push(`step ${step}: Escape${why}`)
    await ui.press('Escape')
    try {
      await eventually(async () => {
        if ((await collect(ui)).dialog) throw new Error('the dialog is still open')
      }, 3000)
    } catch {
      log.push('  (the dialog survived Escape)')
    }
  }

  await ui.goto('/')
  await shellReady(ui)
  check()

  // The areas of the sidebar, in its order. A locked area (no destination) is not an area for this role.
  const areas = await primaryNav(ui)
    .getByRole('link')
    .evaluateAll((links) => links.map((a) => ({ id: a.getAttribute('data-testid') ?? '', href: a.getAttribute('href') ?? '' })).filter((a) => a.href !== ''))
  expect(areas.length, 'sidebar areas').toBeGreaterThanOrEqual(5)
  log.push(`areas: ${areas.map((a) => a.id).join(', ')}`)

  const perArea = Math.max(1, Math.ceil(STEPS / areas.length))
  const enter = async (area: { id: string; href: string }): Promise<void> => {
    log.push(`enter ${area.id} (${area.href})`)
    await ui.getByTestId(area.id).click()
    await quiet()
    await expectLoc(ui.getByTestId('user-menu')).toBeVisible()
    check()
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
      const { candidates, dialog } = await collect(ui)

      // Now and then go back to the area's front page, and leave open dialogs with the keyboard.
      if (dialog && prng.next() < 0.3) {
        await dismiss('')
        continue
      }
      if (!dialog && prng.next() < 0.08) {
        await enter(area)
        continue
      }
      const pick = choose(prng, candidates)
      if (pick === null) {
        if (dialog) {
          await dismiss(' (nothing to use in the dialog)')
        } else {
          await enter(area)
        }
        continue
      }

      const target = ui.locator(`[data-monkey-id="${pick.id}"]`)
      if ((await target.count()) === 0) {
        log.push(`step ${step}: control vanished before use (${pick.kind} "${pick.label}"), skipped`)
        continue
      }
      try {
        if (pick.kind === 'input') {
          const text = prng.pick(GARBAGE)
          log.push(`step ${step}: type ${JSON.stringify(text.slice(0, 40))} into [${pick.label}]`)
          await target.fill(text, 3000)
          await ui.press('Enter')
        } else {
          log.push(`step ${step}: ${pick.kind} "${pick.label}"${pick.href ? ` -> ${pick.href}` : ''}`)
          // Visible and enabled were established when collecting; a short timeout keeps a vanished control cheap.
          await target.click(2000)
        }
      } catch (err) {
        // The page re-rendered between collecting and acting (data arrived): the control is gone, not broken.
        if ((await target.count()) > 0) throw err
        log.push(`step ${step}: control vanished, skipped`)
        continue
      }
      await quiet()
      check()
      if (pick.kind === 'link' && (await ui.getByText(t('shell.notFoundTitle')).count()) > 0) {
        fail(`the link "${pick.label}" (${pick.href}) leads to the not-found page`)
      }
    }
  }
  log.push(`finished ${step} steps`)
}

// One seed per process: every role derives its own sequence from it.
const seed = process.env.MONKEY_SEED ? parseSeed(process.env.MONKEY_SEED) : randomSeed()
console.log(`MONKEY_SEED=${seed}  steps=${STEPS} max=${MAX_MS / 1000}s  (replay: MONKEY_SEED=${seed} bun run monkey)`)
writeFileSync(join(import.meta.dirname, '..', 'monkey-seed.txt'), `${seed}\n`)

describe('monkey', () => {
  for (const role of ['admin', 'dev'] as const) {
    const it = testAs(role)
    it(`random walk as ${role} (seed ${seed})`, async (ui) => {
      const log: string[] = []
      try {
        await walk(ui, role, seed, log)
      } finally {
        // Every step of the walk, kept for replay and triage.
        await mkdir(RESULTS_DIR, { recursive: true })
        await writeFile(join(RESULTS_DIR, `monkey-${role}-seed-${seed}.log`), `${log.join('\n')}\n`)
        if (process.env.MONKEY_VERBOSE) console.log(log.join('\n'))
      }
    }, MAX_MS + 120_000)
  }
})

describe('monkey harness', () => {
  const it = testAs('admin')

  // A walk that cannot see failures would pass forever. This proves the detectors fire in this browser.
  it('sees page errors, console errors and 5xx responses', async (ui) => {
    await ui.goto('/')
    await shellReady(ui)
    expect(problems(ui.findings)).toEqual([])

    await ui.page.evaluate(() => {
      setTimeout(() => {
        throw new Error('canary-uncaught')
      }, 0)
      console.error('canary-console')
    })
    await eventually(() => expect(ui.findings.pageErrors.join('|')).toContain('canary-uncaught'))
    await eventually(() => expect(ui.findings.consoleErrors.join('|')).toContain('canary-console'))

    // A 5xx is reported too. A stand-in response needs request interception in the browser.
    await ui.page.setRequestInterception(true)
    ui.page.on('request', (req) => {
      if (req.url().endsWith('/api/v1/canary-5xx')) void req.respond({ status: 503, body: 'canary' })
      else void req.continue()
    })
    await ui.page.evaluate(() => fetch('/api/v1/canary-5xx'))
    await eventually(() => expect(ui.findings.serverErrors.join('|')).toContain('503'))
  })
})

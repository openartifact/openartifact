// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Page } from 'puppeteer-core'
import type { Account } from '../env'
import { t } from '../../src/i18n'
import { inpage, type ActionKind, type ActionResult, type RegexSpec, type Snapshot, type Step, type TextMatcher } from './inpage'
import { DEFAULT_TIMEOUT_MS, eventually } from './wait'
import type { Findings } from './watch'

export { t }

const MISSING = '__oa_missing__'

function matcher(m: string | RegExp): TextMatcher {
  return typeof m === 'string' ? m : ({ re: m.source, flags: m.flags } satisfies RegexSpec)
}

function describeMatcher(m: TextMatcher): string {
  return typeof m === 'string' ? JSON.stringify(m) : `/${m.re}/${m.flags}`
}

function describeStep(step: Step): string {
  switch (step.kind) {
    case 'css':
      return `locator(${JSON.stringify(step.selector)})`
    case 'role':
      return `getByRole(${step.role}${step.name !== undefined ? `, name=${describeMatcher(step.name)}` : ''}${step.level !== undefined ? `, level=${step.level}` : ''})`
    case 'testid':
      return `getByTestId(${step.id})`
    case 'text':
      return `getByText(${describeMatcher(step.text)})`
    case 'label':
      return `getByLabel(${describeMatcher(step.text)})`
    case 'hasText':
      return `filter(hasText=${describeMatcher(step.text)})`
    case 'nth':
      return `nth(${step.index})`
  }
}

export interface RoleOptions {
  name?: string | RegExp
  exact?: boolean
  level?: number
}

/** What a test drives: one page of one browser context, queried and acted on through the DOM. */
export class Ui {
  constructor(
    readonly page: Page,
    readonly origin: string,
    readonly findings: Findings,
  ) {}

  /** Evaluates `inpage` method `method` in the page, installing the helper first if the document does not have it yet. */
  async run<T>(method: string, ...args: unknown[]): Promise<T> {
    const call = `window.__oa.${method}(${args.map((a) => (a === undefined ? 'undefined' : JSON.stringify(a))).join(',')})`
    const first: unknown = await this.page.evaluate(`window.__oa ? ${call} : ${JSON.stringify(MISSING)}`)
    if (first !== MISSING) return first as T
    await this.ensure()
    return (await this.page.evaluate(call)) as T
  }

  /** Installs the in-page helper into the current document unless it is there. */
  private async ensure(): Promise<void> {
    await this.page.evaluate(`(window.__oa || (window.__oa = (${inpage.toString()})()), true)`)
  }

  /** Runs `fn` in the page over the elements a locator matches. `fn` must be self-contained (it is serialised). */
  async evaluateAll<R, A>(steps: Step[], fn: (els: Element[], arg: A) => R, arg: A): Promise<R> {
    await this.ensure()
    return (await this.page.evaluate(`(${fn.toString()})(window.__oa.resolve(${JSON.stringify(steps)}), ${JSON.stringify(arg ?? null)})`)) as R
  }

  /**
   * Loads a path of the app. Lightpanda can commit a navigation and then fall
   * back to about:blank when the body fails to arrive (lightpanda #3395), so a
   * load that did not end on the app's own origin is an error here, not a
   * mystery two steps later.
   */
  async goto(path: string): Promise<void> {
    await this.page.goto(new URL(path, this.origin).href, { waitUntil: 'load', timeout: 30_000 })
    this.assertOnApp(`goto ${path}`)
  }

  async reload(): Promise<void> {
    await this.page.reload({ waitUntil: 'load', timeout: 30_000 })
    this.assertOnApp('reload')
  }

  assertOnApp(what: string): void {
    const url = this.page.url()
    if (!url.startsWith(this.origin)) throw new Error(`${what}: the page is on ${JSON.stringify(url)}, not on ${this.origin} (about:blank fallback after navigation, lightpanda #3395)`)
  }

  url(): URL {
    return new URL(this.page.url())
  }

  /** Path, query and hash of the current URL, the way a spec writes an expected location. */
  location(): string {
    const u = this.url()
    return u.pathname + u.search + u.hash
  }

  /** Waits until the URL satisfies `want`: a path (with query and hash) compared as a whole, a pattern, or a predicate. */
  async expectUrl(want: string | RegExp | ((url: URL) => boolean), timeoutMs?: number): Promise<void> {
    await eventually(() => {
      this.assertOnApp('expectUrl')
      const ok = typeof want === 'string' ? this.location() === want : want instanceof RegExp ? want.test(this.page.url()) : want(this.url())
      if (!ok) throw new Error(`expected URL ${String(want)}, got ${this.page.url()}`)
    }, timeoutMs)
  }

  /** Waits until the page has had no network activity for `idleMs`: the data of a view has arrived. */
  async settled(idleMs = 300): Promise<void> {
    await this.page.waitForNetworkIdle({ idleTime: idleMs, timeout: 15_000 })
  }

  locator(selector: string): Loc {
    return new Loc(this, [{ kind: 'css', selector }])
  }
  getByRole(role: string, opts: RoleOptions = {}): Loc {
    return new Loc(this, [{ kind: 'role', role, name: opts.name === undefined ? undefined : matcher(opts.name), exact: opts.exact, level: opts.level }])
  }
  getByTestId(id: string): Loc {
    return new Loc(this, [{ kind: 'testid', id }])
  }
  getByText(text: string | RegExp, opts: { exact?: boolean } = {}): Loc {
    return new Loc(this, [{ kind: 'text', text: matcher(text), exact: opts.exact }])
  }
  getByLabel(text: string | RegExp, opts: { exact?: boolean } = {}): Loc {
    return new Loc(this, [{ kind: 'label', text: matcher(text), exact: opts.exact }])
  }

  /** The cookies of this page's browser context, httpOnly ones included. */
  cookies() {
    return this.page.browserContext().cookies()
  }

  /** A key press goes to whatever has focus (a real keyboard event, not a DOM shortcut). */
  async press(key: Parameters<Page['keyboard']['press']>[0]): Promise<void> {
    await this.page.keyboard.press(key)
  }
}

/**
 * A lazily resolved set of elements, found again on every use (like a
 * Playwright locator, minus the geometry: see inpage.ts). Actions are strict:
 * they act when exactly one visible, enabled element matches, and retry until
 * that is so or the timeout passes.
 */
export class Loc {
  constructor(
    readonly ui: Ui,
    readonly steps: Step[],
  ) {}

  private chain(step: Step): Loc {
    return new Loc(this.ui, [...this.steps, step])
  }

  toString(): string {
    return this.steps.map(describeStep).join('.')
  }

  locator(selector: string): Loc {
    return this.chain({ kind: 'css', selector })
  }
  getByRole(role: string, opts: RoleOptions = {}): Loc {
    return this.chain({ kind: 'role', role, name: opts.name === undefined ? undefined : matcher(opts.name), exact: opts.exact, level: opts.level })
  }
  getByTestId(id: string): Loc {
    return this.chain({ kind: 'testid', id })
  }
  getByText(text: string | RegExp, opts: { exact?: boolean } = {}): Loc {
    return this.chain({ kind: 'text', text: matcher(text), exact: opts.exact })
  }
  getByLabel(text: string | RegExp, opts: { exact?: boolean } = {}): Loc {
    return this.chain({ kind: 'label', text: matcher(text), exact: opts.exact })
  }
  filter(opts: { hasText: string | RegExp }): Loc {
    return this.chain({ kind: 'hasText', text: matcher(opts.hasText) })
  }
  first(): Loc {
    return this.chain({ kind: 'nth', index: 0 })
  }
  last(): Loc {
    return this.chain({ kind: 'nth', index: -1 })
  }
  nth(index: number): Loc {
    return this.chain({ kind: 'nth', index })
  }

  /** What the locator matches right now, with the attributes asked for read off the first match. */
  snapshot(attrs: string[] = []): Promise<Snapshot> {
    return this.ui.run<Snapshot>('snapshot', this.steps, attrs)
  }

  async count(): Promise<number> {
    return (await this.snapshot()).count
  }

  async text(): Promise<string> {
    const s = await this.snapshot()
    if (s.first === null) throw new Error(`${this}: no element matches`)
    return s.first.text
  }

  /** The matched elements, mapped by `fn` inside the page. */
  evaluateAll<R, A = null>(fn: (els: Element[], arg: A) => R, arg?: A): Promise<R> {
    return this.ui.evaluateAll(this.steps, fn, (arg ?? null) as A)
  }

  private async act(action: ActionKind, value: string | undefined, timeoutMs: number): Promise<void> {
    await eventually(async () => {
      const res = await this.ui.run<ActionResult>('act', this.steps, action, value)
      if (!res.ok) throw new Error(`${action} ${this}: ${res.reason}`)
    }, timeoutMs)
  }

  /** Clicks the element with a DOM click, once it is visible and enabled. */
  click(timeoutMs = DEFAULT_TIMEOUT_MS): Promise<void> {
    return this.act('click', undefined, timeoutMs)
  }

  /** Replaces the value of a text field and fires the `input` and `change` events a user's typing would. */
  fill(value: string, timeoutMs = DEFAULT_TIMEOUT_MS): Promise<void> {
    return this.act('fill', value, timeoutMs)
  }

  focus(timeoutMs = DEFAULT_TIMEOUT_MS): Promise<void> {
    return this.act('focus', undefined, timeoutMs)
  }
}

/** Assertions on a locator that retry until they hold or the timeout passes. */
export function expectLoc(loc: Loc): LocExpect {
  return new LocExpect(loc, false)
}

class LocExpect {
  constructor(
    private readonly loc: Loc,
    private readonly negate: boolean,
  ) {}

  get not(): LocExpect {
    return new LocExpect(this.loc, !this.negate)
  }

  private async check(what: string, attrs: string[], probe: (s: Snapshot) => { pass: boolean; actual: string }, timeoutMs?: number): Promise<void> {
    await eventually(async () => {
      const s = await this.loc.snapshot(attrs)
      const { pass, actual } = probe(s)
      if (pass === this.negate) throw new Error(`expected ${this.loc} ${this.negate ? 'not ' : ''}${what}; actual: ${actual}`)
    }, timeoutMs)
  }

  /** The single element a state assertion is about; several matches are an ambiguous locator. */
  private single(s: Snapshot): NonNullable<Snapshot['first']> | null {
    if (s.count > 1) throw new Error(`strict mode violation: ${s.count} elements match ${this.loc}`)
    return s.first
  }

  toBeVisible(timeoutMs?: number): Promise<void> {
    return this.check('to be visible', [], (s) => ({ pass: this.single(s)?.visible === true, actual: s.count === 0 ? 'no element' : 'hidden' }), timeoutMs)
  }
  toBeHidden(timeoutMs?: number): Promise<void> {
    return this.check('to be hidden', [], (s) => ({ pass: s.count === 0 || s.first?.visible === false, actual: 'visible' }), timeoutMs)
  }
  toBeEnabled(timeoutMs?: number): Promise<void> {
    return this.check('to be enabled', [], (s) => ({ pass: this.single(s)?.enabled === true, actual: s.count === 0 ? 'no element' : 'disabled' }), timeoutMs)
  }
  toBeDisabled(timeoutMs?: number): Promise<void> {
    return this.check('to be disabled', [], (s) => ({ pass: this.single(s)?.enabled === false, actual: s.count === 0 ? 'no element' : 'enabled' }), timeoutMs)
  }
  toBeEmpty(timeoutMs?: number): Promise<void> {
    return this.check('to be empty', [], (s) => {
      const first = this.single(s)
      return { pass: first !== null && first.text === '' && (first.value ?? '') === '', actual: JSON.stringify(first?.text ?? null) }
    }, timeoutMs)
  }
  toHaveCount(n: number, timeoutMs?: number): Promise<void> {
    return this.check(`to have count ${n}`, [], (s) => ({ pass: s.count === n, actual: String(s.count) }), timeoutMs)
  }
  /** Whole text, whitespace-normalised (a string must equal it, a pattern must match it). */
  toHaveText(want: string | RegExp, timeoutMs?: number): Promise<void> {
    return this.check(`to have text ${String(want)}`, [], (s) => {
      const text = this.single(s)?.text
      if (text === undefined) return { pass: false, actual: 'no element' }
      return { pass: typeof want === 'string' ? text === want.replace(/\s+/g, ' ').trim() : want.test(text), actual: JSON.stringify(text) }
    }, timeoutMs)
  }
  toContainText(want: string | RegExp, timeoutMs?: number): Promise<void> {
    return this.check(`to contain text ${String(want)}`, [], (s) => {
      const text = this.single(s)?.text
      if (text === undefined) return { pass: false, actual: 'no element' }
      return { pass: typeof want === 'string' ? text.includes(want.replace(/\s+/g, ' ').trim()) : want.test(text), actual: JSON.stringify(text) }
    }, timeoutMs)
  }
  /** The attribute is present and, when `want` is given, equals it (string) or matches it (pattern). */
  toHaveAttribute(name: string, want?: string | RegExp, timeoutMs?: number): Promise<void> {
    return this.check(`to have attribute ${name}${want === undefined ? '' : `=${String(want)}`}`, [name], (s) => {
      const first = this.single(s)
      if (first === null) return { pass: false, actual: 'no element' }
      const value = first.attrs[name] ?? null
      const pass = value !== null && (want === undefined || (typeof want === 'string' ? value === want : want.test(value)))
      return { pass, actual: JSON.stringify(value) }
    }, timeoutMs)
  }
  toHaveClass(want: RegExp, timeoutMs?: number): Promise<void> {
    return this.check(`to have class ${String(want)}`, [], (s) => {
      const first = this.single(s)
      return { pass: first !== null && want.test(first.className), actual: JSON.stringify(first?.className ?? null) }
    }, timeoutMs)
  }
  toHaveValue(want: string, timeoutMs?: number): Promise<void> {
    return this.check(`to have value ${JSON.stringify(want)}`, [], (s) => ({ pass: this.single(s)?.value === want, actual: JSON.stringify(s.first?.value ?? null) }), timeoutMs)
  }
}

// --- app-specific helpers ---------------------------------------------------

/** Fills the login form and submits it. */
export async function submitLogin(ui: Ui, account: Account): Promise<void> {
  await ui.getByLabel(t('auth.username')).fill(account.username)
  await ui.locator('#password').fill(account.password)
  await ui.getByRole('button', { name: t('auth.signIn') }).click()
}

/** The primary navigation landmark. */
export function primaryNav(ui: Ui): Loc {
  return ui.getByRole('navigation', { name: t('shell.primaryNav') })
}

/** The breadcrumb trail of the page header. */
export function breadcrumbs(ui: Ui): Loc {
  return ui.getByRole('navigation', { name: t('shell.breadcrumbs') })
}

/**
 * Waits until the app shell is mounted. The header is in the DOM before the first
 * navigation settles (session probe, lazy page chunk, redirects), so this says nothing
 * about the URL: a spec that depends on where the router landed waits with `expectUrl`.
 */
export async function shellReady(ui: Ui): Promise<void> {
  await expectLoc(ui.getByTestId('user-menu')).toBeVisible()
}

/** Opens the user menu and picks a theme; the choice is persisted by the app in localStorage. */
export async function chooseTheme(ui: Ui, mode: 'light' | 'dark'): Promise<void> {
  await ui.getByTestId('user-menu').click()
  await ui.getByTestId(`theme-${mode}`).click()
  await expectLoc(ui.locator('html')).toHaveClass(mode === 'dark' ? /(^|\s)dark(\s|$)/ : /^((?!\bdark\b).)*$/)
  await ui.press('Escape')
}

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * DOM queries and actions that run inside the page.
 *
 * The browser under test (Lightpanda) has no layout: bounding boxes are made
 * up, so everything that depends on geometry (puppeteer's hit-tested clicks,
 * "visible" as a non-empty box, scrolling into view) is unreliable. This
 * module answers the same questions from the DOM and the computed style only:
 * what an element's role and accessible name are, whether it is shown or
 * enabled, and clicking or filling it through DOM events.
 *
 * `inpage` is serialised with Function.prototype.toString and evaluated in the
 * page (see ui.ts), so it must be self-contained: no imports and no reference
 * to anything outside the function body. Only types are shared.
 */

export interface RegexSpec {
  re: string
  flags: string
}

/** A string matches as a case-insensitive substring (or exactly, with `exact`); a regular expression is tested against the normalised text. */
export type TextMatcher = string | RegexSpec

export type Step =
  | { kind: 'css'; selector: string }
  | { kind: 'role'; role: string; name?: TextMatcher; exact?: boolean; level?: number }
  | { kind: 'testid'; id: string }
  | { kind: 'text'; text: TextMatcher; exact?: boolean }
  | { kind: 'label'; text: TextMatcher; exact?: boolean }
  | { kind: 'hasText'; text: TextMatcher }
  | { kind: 'nth'; index: number }

export interface Snapshot {
  count: number
  first: {
    tag: string
    visible: boolean
    enabled: boolean
    text: string
    value: string | null
    checked: boolean | null
    focused: boolean
    className: string
    attrs: Record<string, string | null>
  } | null
}

export type ActionKind = 'click' | 'fill' | 'focus'

export type ActionResult = { ok: true } | { ok: false; reason: string }

export interface InPage {
  resolve(steps: Step[]): Element[]
  snapshot(steps: Step[], attrs: string[]): Snapshot
  act(steps: Step[], action: ActionKind, value: string | undefined): ActionResult
}

export function inpage(): InPage {
  const norm = (s: string | null | undefined): string => (s ?? '').replace(/\s+/g, ' ').trim()

  function matches(m: TextMatcher, actual: string, exact: boolean | undefined): boolean {
    const a = norm(actual)
    if (typeof m !== 'string') return new RegExp(m.re, m.flags).test(a)
    const want = norm(m)
    return exact ? a === want : a.toLowerCase().includes(want.toLowerCase())
  }

  // --- state ---------------------------------------------------------------

  /** Shown: no `hidden` attribute and no `display: none` on the element or an ancestor, and not `visibility: hidden`. */
  function shown(el: Element): boolean {
    for (let n: Element | null = el; n; n = n.parentElement) {
      if (n.hasAttribute('hidden')) return false
      if (getComputedStyle(n).display === 'none') return false
    }
    return getComputedStyle(el).visibility !== 'hidden'
  }

  /** What accessibility tools skip: not shown, or inside an `aria-hidden` subtree. */
  function hiddenFromTree(el: Element): boolean {
    if (!shown(el)) return true
    for (let n: Element | null = el; n; n = n.parentElement) {
      if (n.getAttribute('aria-hidden') === 'true') return true
    }
    return false
  }

  function enabled(el: Element): boolean {
    for (let n: Element | null = el; n; n = n.parentElement) {
      if (n.getAttribute('aria-disabled') === 'true') return false
      if (n.tagName === 'FIELDSET' && n.hasAttribute('disabled')) return false
    }
    return !(el as HTMLButtonElement).disabled
  }

  // --- role and accessible name -------------------------------------------

  const INPUT_ROLES: Record<string, string> = {
    button: 'button',
    submit: 'button',
    reset: 'button',
    image: 'button',
    checkbox: 'checkbox',
    radio: 'radio',
    range: 'slider',
    number: 'spinbutton',
    search: 'searchbox',
    email: 'textbox',
    tel: 'textbox',
    text: 'textbox',
    url: 'textbox',
    password: 'textbox',
  }
  const TAG_ROLES: Record<string, string> = {
    a: 'link',
    area: 'link',
    article: 'article',
    aside: 'complementary',
    button: 'button',
    dialog: 'dialog',
    footer: 'contentinfo',
    h1: 'heading',
    h2: 'heading',
    h3: 'heading',
    h4: 'heading',
    h5: 'heading',
    h6: 'heading',
    header: 'banner',
    hr: 'separator',
    img: 'img',
    li: 'listitem',
    main: 'main',
    nav: 'navigation',
    ol: 'list',
    option: 'option',
    progress: 'progressbar',
    select: 'combobox',
    summary: 'button',
    table: 'table',
    tbody: 'rowgroup',
    td: 'cell',
    textarea: 'textbox',
    th: 'columnheader',
    thead: 'rowgroup',
    tr: 'row',
    ul: 'list',
  }

  function roleOf(el: Element): string {
    const explicit = norm(el.getAttribute('role'))
    if (explicit !== '') return explicit.split(' ')[0] as string
    const tag = el.tagName.toLowerCase()
    if (tag === 'input') return INPUT_ROLES[(el.getAttribute('type') ?? 'text').toLowerCase()] ?? 'textbox'
    if ((tag === 'a' || tag === 'area') && !el.hasAttribute('href')) return ''
    return TAG_ROLES[tag] ?? ''
  }

  /** Roles whose accessible name may come from their content. */
  const NAME_FROM_CONTENT = new Set([
    'button',
    'cell',
    'checkbox',
    'columnheader',
    'heading',
    'link',
    'listitem',
    'menuitem',
    'option',
    'radio',
    'row',
    'switch',
    'tab',
    'treeitem',
  ])

  function labelsOf(el: Element): Element[] {
    const found: Element[] = []
    if (el.id !== '') {
      for (const label of Array.from(document.querySelectorAll('label'))) {
        if ((label as HTMLLabelElement).htmlFor === el.id) found.push(label)
      }
    }
    const wrapper = el.closest('label')
    if (wrapper && !found.includes(wrapper)) found.push(wrapper)
    return found
  }

  function contentOf(el: Element): string {
    let out = ''
    for (const node of Array.from(el.childNodes)) {
      if (node.nodeType === 3) {
        out += node.textContent ?? ''
      } else if (node.nodeType === 1) {
        const child = node as Element
        if (hiddenFromTree(child)) continue
        const own = norm(child.getAttribute('aria-label'))
        const text = own !== '' ? own : child.tagName === 'IMG' ? (child.getAttribute('alt') ?? '') : contentOf(child)
        const display = getComputedStyle(child).display
        out += display === '' || display.startsWith('inline') ? text : ` ${text} `
      }
    }
    return out
  }

  function nameOf(el: Element): string {
    const ids = norm(el.getAttribute('aria-labelledby'))
    if (ids !== '') {
      const text = norm(
        ids
          .split(' ')
          .map((id) => document.getElementById(id))
          .filter((n): n is HTMLElement => n !== null)
          .map((n) => contentOf(n) || (n.textContent ?? ''))
          .join(' '),
      )
      if (text !== '') return text
    }
    const aria = norm(el.getAttribute('aria-label'))
    if (aria !== '') return aria
    const tag = el.tagName.toLowerCase()
    if (tag === 'img') return norm(el.getAttribute('alt'))
    if (tag === 'input' || tag === 'textarea' || tag === 'select') {
      const type = (el.getAttribute('type') ?? '').toLowerCase()
      if (tag === 'input' && ['button', 'submit', 'reset'].includes(type)) return norm(el.getAttribute('value') ?? type)
      const labels = norm(labelsOf(el).map((l) => contentOf(l)).join(' '))
      if (labels !== '') return labels
      return norm(el.getAttribute('title') ?? el.getAttribute('placeholder'))
    }
    if (NAME_FROM_CONTENT.has(roleOf(el))) {
      const text = norm(contentOf(el))
      if (text !== '') return text
    }
    return norm(el.getAttribute('title'))
  }

  // --- resolution ------------------------------------------------------------

  const SKIPPED_FOR_TEXT = new Set(['SCRIPT', 'STYLE', 'HEAD', 'TITLE', 'META', 'LINK', 'NOSCRIPT', 'TEMPLATE'])

  function descendants(root: ParentNode): Element[] {
    return Array.from(root.querySelectorAll('*'))
  }

  function unique(items: Element[]): Element[] {
    return Array.from(new Set(items))
  }

  function byText(root: ParentNode, text: TextMatcher, exact: boolean | undefined): Element[] {
    const hits = descendants(root).filter((el) => !SKIPPED_FOR_TEXT.has(el.tagName) && matches(text, el.textContent ?? '', exact))
    // The innermost elements that carry the text, as a person would point at it.
    return hits.filter((el) => !hits.some((other) => other !== el && el.contains(other)))
  }

  function byLabel(root: ParentNode, text: TextMatcher, exact: boolean | undefined): Element[] {
    return descendants(root).filter((el) => {
      const tag = el.tagName
      const control = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.hasAttribute('role')
      if (!control) return false
      const aria = el.getAttribute('aria-label')
      if (aria !== null && matches(text, aria, exact)) return true
      const ids = norm(el.getAttribute('aria-labelledby'))
      if (ids !== '') {
        const joined = ids
          .split(' ')
          .map((id) => document.getElementById(id)?.textContent ?? '')
          .join(' ')
        if (matches(text, joined, exact)) return true
      }
      return labelsOf(el).some((l) => matches(text, contentOf(l), exact))
    })
  }

  function resolve(steps: Step[]): Element[] {
    let current: ParentNode[] = [document]
    for (const step of steps) {
      const next: Element[] = []
      switch (step.kind) {
        case 'css':
          for (const root of current) next.push(...Array.from(root.querySelectorAll(step.selector)))
          break
        case 'testid':
          for (const root of current) next.push(...Array.from(root.querySelectorAll('[data-testid]')).filter((el) => el.getAttribute('data-testid') === step.id))
          break
        case 'role':
          for (const root of current) {
            for (const el of descendants(root)) {
              if (roleOf(el) !== step.role || hiddenFromTree(el)) continue
              if (step.level !== undefined) {
                const level = Number(el.getAttribute('aria-level') ?? /^H([1-6])$/.exec(el.tagName)?.[1] ?? 0)
                if (level !== step.level) continue
              }
              if (step.name !== undefined && !matches(step.name, nameOf(el), step.exact)) continue
              next.push(el)
            }
          }
          break
        case 'text':
          for (const root of current) next.push(...byText(root, step.text, step.exact))
          break
        case 'label':
          for (const root of current) next.push(...byLabel(root, step.text, step.exact))
          break
        case 'hasText':
          next.push(...(current as Element[]).filter((el) => matches(step.text, el.textContent ?? '', false)))
          break
        case 'nth': {
          const at = step.index < 0 ? current.length + step.index : step.index
          const picked = current[at]
          if (picked !== undefined) next.push(picked as Element)
          break
        }
      }
      current = unique(next)
    }
    return current as Element[]
  }

  // --- reads and actions -------------------------------------------------------

  function snapshot(steps: Step[], attrs: string[]): Snapshot {
    const els = resolve(steps)
    const el = els[0]
    if (el === undefined) return { count: 0, first: null }
    const input = el as HTMLInputElement
    const checked = el.getAttribute('aria-checked') ?? (el.tagName === 'INPUT' ? String(input.checked) : null)
    return {
      count: els.length,
      first: {
        tag: el.tagName.toLowerCase(),
        visible: shown(el),
        enabled: enabled(el),
        text: norm(el.textContent),
        value: typeof input.value === 'string' ? input.value : null,
        checked: checked === null ? null : checked === 'true',
        focused: document.activeElement === el,
        className: el.getAttribute('class') ?? '',
        attrs: Object.fromEntries(attrs.map((a) => [a, el.getAttribute(a)])),
      },
    }
  }

  function act(steps: Step[], action: ActionKind, value: string | undefined): ActionResult {
    const els = resolve(steps)
    if (els.length === 0) return { ok: false, reason: 'no element matches' }
    if (els.length > 1) return { ok: false, reason: `strict mode violation: ${els.length} elements match` }
    const el = els[0] as HTMLElement
    if (!shown(el)) return { ok: false, reason: 'element is not visible' }
    if (action !== 'focus' && !enabled(el)) return { ok: false, reason: 'element is not enabled' }
    switch (action) {
      case 'click':
        // A DOM click runs the same handlers a pointer would, without asking
        // the (absent) layout engine what is under the pointer.
        el.click()
        return { ok: true }
      case 'focus':
        el.focus()
        return { ok: true }
      case 'fill': {
        const field = el as HTMLInputElement | HTMLTextAreaElement
        if (field.readOnly) return { ok: false, reason: 'element is read-only' }
        if (el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA') return { ok: false, reason: `element is not a text field (${el.tagName.toLowerCase()})` }
        el.focus()
        // The native setter, not an assignment: the framework's own value tracking must see the change.
        const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
        const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set
        if (setter) setter.call(field, value ?? '')
        else field.value = value ?? ''
        el.dispatchEvent(new Event('input', { bubbles: true }))
        el.dispatchEvent(new Event('change', { bubbles: true }))
        return { ok: true }
      }
    }
  }

  return { resolve, snapshot, act }
}

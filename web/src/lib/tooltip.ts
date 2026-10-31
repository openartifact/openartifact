// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { shallowRef, type Directive } from 'vue'
import type { Placement } from './floating'

/** What `v-tooltip` takes: the text, or the text with a second line and a placement. */
export type TooltipValue = string | { text: string; detail?: string; placement?: Placement; offset?: number } | null | undefined | false

export interface ActiveTooltip {
  anchor: HTMLElement
  text: string
  detail?: string
  placement: Placement
  offset: number
}

/** Id of the one tooltip element (`TooltipHost`); the trigger points at it with aria-describedby while it is shown. */
export const TOOLTIP_ID = 'oa-tooltip'

/** Pointer hover waits this long before the tooltip appears; keyboard focus shows it at once. */
export const HOVER_DELAY_MS = 300

/** The tooltip on screen, null when none. Only one exists at a time; `TooltipHost` renders it. */
export const activeTooltip = shallowRef<ActiveTooltip | null>(null)

interface Binding {
  value: TooltipValue
  timer: ReturnType<typeof setTimeout> | undefined
  teardown: () => void
}

const bindings = new WeakMap<HTMLElement, Binding>()

function normalize(value: TooltipValue): Omit<ActiveTooltip, 'anchor'> | null {
  if (!value) return null
  const v = typeof value === 'string' ? { text: value } : value
  if (!v.text) return null
  return { text: v.text, detail: v.detail, placement: v.placement ?? 'top', offset: v.offset ?? 6 }
}

function describe(el: HTMLElement, on: boolean): void {
  const tokens = (el.getAttribute('aria-describedby') ?? '').split(/\s+/).filter((t) => t && t !== TOOLTIP_ID)
  if (on) tokens.push(TOOLTIP_ID)
  if (tokens.length > 0) el.setAttribute('aria-describedby', tokens.join(' '))
  else el.removeAttribute('aria-describedby')
}

/** An icon button named "Delete: foo" with the tooltip "Delete": describing it again would make a screen reader say it twice. */
function repeatsName(el: HTMLElement, tip: Omit<ActiveTooltip, 'anchor'>): boolean {
  const name = el.getAttribute('aria-label') ?? el.textContent ?? ''
  return name.includes(tip.text) && (!tip.detail || name.includes(tip.detail))
}

function show(el: HTMLElement, value: TooltipValue): void {
  const tip = normalize(value)
  if (!tip) return
  const previous = activeTooltip.value?.anchor
  if (previous && previous !== el) describe(previous, false)
  activeTooltip.value = { anchor: el, ...tip }
  describe(el, !repeatsName(el, tip))
}

/** Hides the tooltip of `el`, if it is the one on screen. */
export function hideTooltip(el?: HTMLElement): void {
  const current = activeTooltip.value
  if (!current || (el && current.anchor !== el)) return
  describe(current.anchor, false)
  activeTooltip.value = null
}

/** A keyboard user gets the tooltip on focus; a click that focuses a button with the mouse does not. */
function keyboardFocus(el: HTMLElement): boolean {
  try {
    return el.matches(':focus-visible')
  } catch {
    // An engine without :focus-visible: every focus counts.
    return true
  }
}

function attach(el: HTMLElement, value: TooltipValue): void {
  const b: Binding = { value, timer: undefined, teardown: () => undefined }
  const cancel = () => {
    clearTimeout(b.timer)
    b.timer = undefined
  }
  const onEnter = () => {
    cancel()
    b.timer = setTimeout(() => show(el, b.value), HOVER_DELAY_MS)
  }
  const onLeave = () => {
    cancel()
    hideTooltip(el)
  }
  const onFocus = () => {
    cancel()
    if (keyboardFocus(el)) show(el, b.value)
  }
  const onBlur = onLeave
  const onPress = onLeave

  el.addEventListener('mouseenter', onEnter)
  el.addEventListener('mouseleave', onLeave)
  el.addEventListener('focusin', onFocus)
  el.addEventListener('focusout', onBlur)
  el.addEventListener('pointerdown', onPress)
  b.teardown = () => {
    cancel()
    hideTooltip(el)
    el.removeEventListener('mouseenter', onEnter)
    el.removeEventListener('mouseleave', onLeave)
    el.removeEventListener('focusin', onFocus)
    el.removeEventListener('focusout', onBlur)
    el.removeEventListener('pointerdown', onPress)
  }
  bindings.set(el, b)
}

/**
 * `v-tooltip="'Delete'"`: a tooltip on hover (after a short delay) and on keyboard focus, drawn
 * by `TooltipHost` in the body so no overflow container clips it. It is described to assistive
 * technology through aria-describedby while shown (unless the element's own name already says the same),
 * and dismissed by Escape, a press, leaving or blur. An empty value switches it off. It adds a
 * description only: the element keeps its own accessible name.
 */
export const vTooltip: Directive<HTMLElement, TooltipValue> = {
  mounted(el, binding) {
    attach(el, binding.value)
  },
  updated(el, binding) {
    const b = bindings.get(el)
    if (!b) return
    b.value = binding.value
    // Text changes while shown follow; a value that became empty removes the tooltip.
    if (activeTooltip.value?.anchor === el) {
      const tip = normalize(binding.value)
      if (tip) activeTooltip.value = { anchor: el, ...tip }
      else hideTooltip(el)
    }
  },
  unmounted(el) {
    bindings.get(el)?.teardown()
    bindings.delete(el)
  },
}

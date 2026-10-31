// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it } from 'bun:test'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { useFloating, type ElementRef } from '../useFloating'

// happy-dom does no layout: the anchor's rectangle and the box's size are what the test says they are.
let anchorRect = { top: 100, left: 400, right: 420, bottom: 120 }
let anchor: HTMLElement
let box: HTMLElement
let scope: EffectScope

function define(el: HTMLElement, props: Record<string, number>): void {
  for (const [k, v] of Object.entries(props)) Object.defineProperty(el, k, { value: v, configurable: true })
}

function setup(options: Parameters<typeof useFloating>[2] = { placement: 'bottom' }) {
  const anchorRef = ref<ElementRef>(anchor)
  const boxRef = ref<ElementRef>(null)
  const floating = scope.run(() => useFloating(anchorRef, boxRef, options))!
  return { floating, boxRef }
}

const pos = (style: { top?: unknown; left?: unknown }) => [style.top, style.left]

beforeEach(() => {
  anchorRect = { top: 100, left: 400, right: 420, bottom: 120 }
  anchor = document.createElement('button')
  anchor.getBoundingClientRect = () => ({ ...anchorRect, x: 0, y: 0, width: 20, height: 20, toJSON: () => ({}) })
  box = document.createElement('div')
  define(box, { offsetWidth: 100, offsetHeight: 40 })
  define(document.documentElement, { clientWidth: 1000, clientHeight: 800 })
  document.body.append(anchor, box)
  scope = effectScope()
})

afterEach(() => {
  scope.stop()
  anchor.remove()
  box.remove()
})

describe('useFloating', () => {
  it('stays hidden at the origin until the floating element exists and was measured', async () => {
    const { floating, boxRef } = setup()
    await nextTick()
    expect(floating.style.value).toMatchObject({ position: 'fixed', visibility: 'hidden' })
    boxRef.value = box
    await nextTick()
    expect(floating.style.value.visibility).toBeUndefined()
  })

  it('places the element with fixed coordinates from the anchor rectangle', async () => {
    const { floating, boxRef } = setup({ placement: 'bottom-start', offset: 4 })
    boxRef.value = box
    await nextTick()
    expect(floating.style.value.position).toBe('fixed')
    expect(pos(floating.style.value)).toEqual(['124px', '400px'])
    expect(floating.placement.value).toBe('bottom-start')
  })

  it('flips at the viewport edge and reports the side it used', async () => {
    anchorRect = { top: 770, left: 400, right: 420, bottom: 790 }
    const { floating, boxRef } = setup({ placement: 'bottom', offset: 4 })
    boxRef.value = box
    await nextTick()
    expect(floating.placement.value).toBe('top')
    expect(pos(floating.style.value)).toEqual(['726px', '360px'])
  })

  it('follows the anchor when the page or a scroll container scrolls', async () => {
    const { floating, boxRef } = setup({ placement: 'bottom-start', offset: 4 })
    boxRef.value = box
    await nextTick()
    // A scroll in any ancestor (the event does not bubble: it is caught in the capture phase of the window).
    anchorRect = { top: 60, left: 400, right: 420, bottom: 80 }
    const scroller = document.createElement('div')
    document.body.append(scroller)
    scroller.dispatchEvent(new Event('scroll'))
    expect(pos(floating.style.value)).toEqual(['84px', '400px'])
    scroller.remove()
  })

  it('follows the anchor on window resize', async () => {
    const { floating, boxRef } = setup({ placement: 'bottom-start', offset: 4 })
    boxRef.value = box
    await nextTick()
    anchorRect = { top: 100, left: 300, right: 320, bottom: 120 }
    window.dispatchEvent(new Event('resize'))
    expect(pos(floating.style.value)).toEqual(['124px', '300px'])
  })

  it('stops listening once closed', async () => {
    const open = ref(true)
    const { floating, boxRef } = setup({ placement: 'bottom-start', offset: 4, open })
    boxRef.value = box
    await nextTick()
    open.value = false
    await nextTick()
    anchorRect = { top: 10, left: 10, right: 30, bottom: 30 }
    window.dispatchEvent(new Event('resize'))
    expect(pos(floating.style.value)).toEqual(['124px', '400px'])
  })

  it('takes options from a getter so placement can follow a prop', async () => {
    const placement = ref<'top' | 'bottom'>('bottom')
    const { floating, boxRef } = setup(() => ({ placement: placement.value, offset: 4 }))
    boxRef.value = box
    await nextTick()
    expect(floating.placement.value).toBe('bottom')
    placement.value = 'top'
    await nextTick()
    expect(floating.placement.value).toBe('top')
  })

  it('accepts a component instance as the element (its root element is measured)', async () => {
    const { floating, boxRef } = setup({ placement: 'bottom-start', offset: 4 })
    boxRef.value = { $el: box } as unknown as ElementRef
    await nextTick()
    expect(pos(floating.style.value)).toEqual(['124px', '400px'])
  })
})

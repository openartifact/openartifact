// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it } from 'bun:test'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { useDismiss, type DismissReason, type UseDismissOptions } from '../useDismiss'
import type { ElementRef } from '../useFloating'

let trigger: HTMLButtonElement
let panel: HTMLDivElement
let inside: HTMLButtonElement
let outside: HTMLButtonElement
let scope: EffectScope
let dismissed: DismissReason[]

function setup(extra: Partial<UseDismissOptions> = {}) {
  const open = ref(true)
  const dismiss = scope.run(() =>
    useDismiss({
      open,
      anchor: ref<ElementRef>(trigger),
      floating: ref<ElementRef>(panel),
      onDismiss: (reason) => {
        dismissed.push(reason)
        open.value = false
      },
      ...extra,
    }),
  )!
  return { open, dismiss }
}

const press = (el: Element) => el.dispatchEvent(new Event('pointerdown', { bubbles: true }))
const key = (el: Element, k: string) => el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }))

beforeEach(() => {
  document.body.replaceChildren()
  trigger = document.createElement('button')
  panel = document.createElement('div')
  inside = document.createElement('button')
  outside = document.createElement('button')
  panel.append(inside)
  document.body.append(trigger, panel, outside)
  scope = effectScope()
  dismissed = []
})

afterEach(() => scope.stop())

describe('useDismiss', () => {
  it('dismisses on Escape and gives the focus back to the trigger when it was in the layer', async () => {
    setup()
    await nextTick()
    inside.focus()
    key(inside, 'Escape')
    expect(dismissed).toEqual(['escape'])
    expect(document.activeElement).toBe(trigger)
  })

  it('does not steal the focus when Escape is pressed while it is elsewhere', async () => {
    setup()
    await nextTick()
    outside.focus()
    key(outside, 'Escape')
    expect(dismissed).toEqual(['escape'])
    expect(document.activeElement).toBe(outside)
  })

  it('swallows Escape so a dialog underneath stays open, unless told to let it through', async () => {
    let reached = 0
    const onKey = () => reached++
    window.addEventListener('keydown', onKey)
    const { open } = setup()
    await nextTick()
    key(inside, 'Escape')
    expect(reached).toBe(0)

    open.value = true
    scope.stop()
    scope = effectScope()
    setup({ consumeEscape: false })
    await nextTick()
    key(inside, 'Escape')
    expect(reached).toBe(1)
    window.removeEventListener('keydown', onKey)
  })

  it('ignores other keys', async () => {
    setup()
    await nextTick()
    key(inside, 'a')
    key(inside, 'Enter')
    expect(dismissed).toEqual([])
  })

  it('dismisses on a press outside, not on one inside the layer or on the trigger', async () => {
    setup()
    await nextTick()
    press(inside)
    press(panel)
    // The trigger toggles the layer itself: counting its press as "outside" would reopen it right after.
    press(trigger)
    expect(dismissed).toEqual([])
    press(outside)
    expect(dismissed).toEqual(['outside'])
  })

  it('can leave outside presses alone (a tooltip)', async () => {
    setup({ outsidePress: false })
    await nextTick()
    press(outside)
    expect(dismissed).toEqual([])
  })

  it('dismisses when the focus moves out, only if asked to', async () => {
    setup({ focusOutside: true })
    await nextTick()
    inside.focus()
    expect(dismissed).toEqual([])
    outside.focus()
    expect(dismissed).toEqual(['focus'])
  })

  it('listens only while open', async () => {
    const { open } = setup()
    await nextTick()
    open.value = false
    await nextTick()
    key(inside, 'Escape')
    press(outside)
    expect(dismissed).toEqual([])
    open.value = true
    await nextTick()
    press(outside)
    expect(dismissed).toEqual(['outside'])
  })

  it('restoreFocus hands the focus to the trigger', async () => {
    const { dismiss } = setup()
    inside.focus()
    dismiss.restoreFocus()
    expect(document.activeElement).toBe(trigger)
  })

  it('leaves the focus alone on Escape when returnFocus is off', async () => {
    setup({ returnFocus: false })
    await nextTick()
    inside.focus()
    key(inside, 'Escape')
    expect(document.activeElement).toBe(inside)
  })
})

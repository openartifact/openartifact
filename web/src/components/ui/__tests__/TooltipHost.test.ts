// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, jest } from 'bun:test'
import { defineComponent, h, nextTick, ref, withDirectives } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import TooltipHost from '../TooltipHost.vue'
import { HOVER_DELAY_MS, TOOLTIP_ID, activeTooltip, hideTooltip, vTooltip, type TooltipValue } from '../../../lib/tooltip'

const tip = () => document.body.querySelector<HTMLElement>('[role="tooltip"]')

const text = ref<TooltipValue>('Delete')

// A scroll container around the trigger: the tooltip must not end up inside it.
const Page = defineComponent({
  setup() {
    return () =>
      h('div', { style: 'overflow:hidden' }, [
        withDirectives(h('button', { 'data-testid': 'trigger', 'aria-label': 'Remove item' }, 'x'), [[vTooltip, text.value]]),
        withDirectives(h('button', { 'data-testid': 'other' }, 'other'), [[vTooltip, 'Other']]),
      ])
  },
})

let host: VueWrapper
let page: VueWrapper

beforeEach(() => {
  jest.useFakeTimers()
  document.body.replaceChildren()
  text.value = 'Delete'
  host = mount(TooltipHost, { attachTo: document.body })
  page = mount(Page, { attachTo: document.body })
})

afterEach(() => {
  page.unmount()
  host.unmount()
  hideTooltip()
  jest.useRealTimers()
})

const trigger = () => page.get('[data-testid="trigger"]')
const other = () => page.get('[data-testid="other"]')

// Real focus: the tooltip follows :focus-visible, so a synthetic focusin alone does not count as keyboard focus.
async function focus(w: ReturnType<typeof trigger>): Promise<void> {
  ;(w.element as HTMLElement).focus()
  await nextTick()
}
async function blur(w: ReturnType<typeof trigger>): Promise<void> {
  ;(w.element as HTMLElement).blur()
  await nextTick()
}

describe('v-tooltip', () => {
  it('shows after the hover delay, in the body, and hides on leave', async () => {
    await trigger().trigger('mouseenter')
    jest.advanceTimersByTime(HOVER_DELAY_MS - 1)
    await nextTick()
    expect(tip()).toBeNull()
    jest.advanceTimersByTime(1)
    await nextTick()
    expect(tip()?.textContent?.trim()).toBe('Delete')
    expect(page.element.contains(tip())).toBe(false)
    expect(tip()?.parentElement).toBe(document.body)

    await trigger().trigger('mouseleave')
    expect(tip()).toBeNull()
  })

  it('cancels a pending hover when the pointer leaves first', async () => {
    await trigger().trigger('mouseenter')
    await trigger().trigger('mouseleave')
    jest.advanceTimersByTime(HOVER_DELAY_MS * 2)
    await nextTick()
    expect(tip()).toBeNull()
  })

  it('shows at once on keyboard focus and hides on blur', async () => {
    await focus(trigger())
    expect(tip()?.textContent?.trim()).toBe('Delete')
    await blur(trigger())
    expect(tip()).toBeNull()
  })

  it('is described to assistive technology while shown, as role=tooltip', async () => {
    const el = trigger().element
    await focus(trigger())
    expect(tip()?.id).toBe(TOOLTIP_ID)
    expect(tip()?.getAttribute('role')).toBe('tooltip')
    // The button is named "Remove item" and the tooltip says something else: it is a description.
    expect(el.getAttribute('aria-describedby')).toBe(TOOLTIP_ID)
    await blur(trigger())
    expect(el.hasAttribute('aria-describedby')).toBe(false)
  })

  it('does not describe an element by what its name already says', async () => {
    text.value = 'Remove item'
    await nextTick()
    await focus(trigger())
    expect(tip()).not.toBeNull()
    expect(trigger().element.hasAttribute('aria-describedby')).toBe(false)
  })

  it('keeps an existing description and restores it afterwards', async () => {
    trigger().element.setAttribute('aria-describedby', 'hint')
    await focus(trigger())
    expect(trigger().element.getAttribute('aria-describedby')).toBe('hint oa-tooltip')
    await blur(trigger())
    expect(trigger().element.getAttribute('aria-describedby')).toBe('hint')
  })

  it('is dismissed by Escape without taking the focus, and the key still reaches the page', async () => {
    const el = trigger().element as HTMLElement
    el.focus()
    await focus(trigger())
    let reached = false
    window.addEventListener('keydown', () => (reached = true), { once: true })
    el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    expect(tip()).toBeNull()
    expect(document.activeElement).toBe(el)
    expect(reached).toBe(true)
  })

  it('goes away when the trigger is pressed', async () => {
    await focus(trigger())
    await trigger().trigger('pointerdown')
    expect(tip()).toBeNull()
  })

  it('shows one tooltip at a time', async () => {
    await focus(trigger())
    expect(tip()?.textContent?.trim()).toBe('Delete')
    await focus(other())
    expect(document.body.querySelectorAll('[role="tooltip"]')).toHaveLength(1)
    expect(tip()?.textContent?.trim()).toBe('Other')
    // The first trigger no longer claims to be described by it.
    expect(trigger().element.hasAttribute('aria-describedby')).toBe(false)
  })

  it('does nothing for an empty value, and follows a changed text while shown', async () => {
    text.value = ''
    await nextTick()
    await focus(trigger())
    expect(tip()).toBeNull()

    await blur(trigger())
    text.value = 'First'
    await nextTick()
    await focus(trigger())
    text.value = 'Second'
    await nextTick()
    await nextTick()
    expect(tip()?.textContent?.trim()).toBe('Second')
    text.value = false
    await nextTick()
    await nextTick()
    expect(tip()).toBeNull()
  })

  it('removes the tooltip with its trigger', async () => {
    await focus(trigger())
    page.unmount()
    await nextTick()
    expect(tip()).toBeNull()
    expect(activeTooltip.value).toBeNull()
    page = mount(Page, { attachTo: document.body })
  })

  it('renders a second line from the detail', async () => {
    text.value = { text: 'Bundles', detail: 'Requires administrator', placement: 'right' }
    await nextTick()
    await focus(trigger())
    expect(tip()?.textContent).toContain('Bundles')
    expect(tip()?.querySelector('span')?.textContent).toBe('Requires administrator')
    expect(tip()?.dataset.placement).toBe('right')
  })
})

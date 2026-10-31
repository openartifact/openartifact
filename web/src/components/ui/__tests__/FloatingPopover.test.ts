// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import FloatingPopover from '../FloatingPopover.vue'

function render() {
  return mount(FloatingPopover, {
    attachTo: document.body,
    attrs: { 'aria-label': 'Account menu', 'data-testid': 'trigger' },
    props: { panelTestid: 'panel' },
    slots: {
      default: 'Open',
      panel: `<template #panel="{ close }">
        <a href="#first" data-testid="first">First</a>
        <button type="button" data-testid="last" @click="close()">Last</button>
      </template>`,
    },
  })
}

const panel = () => document.body.querySelector<HTMLElement>('[data-testid="panel"]')
const outside = () => document.body.querySelector<HTMLElement>('#outside')!

beforeEach(() => {
  document.body.replaceChildren()
  const el = document.createElement('button')
  el.id = 'outside'
  document.body.append(el)
})

describe('FloatingPopover', () => {
  it('is a closed disclosure button until pressed: aria-expanded, and aria-controls only while open', async () => {
    const w = render()
    const trigger = w.get('[data-testid="trigger"]')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(trigger.attributes('aria-controls')).toBeUndefined()
    expect(panel()).toBeNull()

    await trigger.trigger('click')
    await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(trigger.attributes('aria-controls')).toBe(panel()?.id)
    expect(panel()?.id).toBeTruthy()
    // Not clipped by an overflow ancestor: the panel lives outside the component's own DOM (teleported to the body), fixed.
    expect(w.element.contains(panel())).toBe(false)
    expect(panel()?.style.position).toBe('fixed')
    w.unmount()
  })

  it('moves the focus into the panel on open', async () => {
    const w = render()
    await w.get('[data-testid="trigger"]').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(panel())
    w.unmount()
  })

  it('closes on Escape and returns the focus to the trigger', async () => {
    const w = render()
    const trigger = w.get('[data-testid="trigger"]')
    await trigger.trigger('click')
    await flushPromises()
    document.body.querySelector<HTMLElement>('[data-testid="first"]')!.focus()
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(panel()).toBeNull()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(trigger.element)
    w.unmount()
  })

  it('closes on a press outside, without moving the focus away from where the user went', async () => {
    const w = render()
    await w.get('[data-testid="trigger"]').trigger('click')
    await flushPromises()
    outside().focus()
    outside().dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(outside())
    w.unmount()
  })

  it('stays open for a press inside the panel and toggles closed from the trigger', async () => {
    const w = render()
    const trigger = w.get('[data-testid="trigger"]')
    await trigger.trigger('click')
    await flushPromises()
    panel()!.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(panel()).not.toBeNull()
    trigger.element.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await trigger.trigger('click')
    await flushPromises()
    expect(panel()).toBeNull()
    w.unmount()
  })

  it('close() from the panel slot closes it and focuses the trigger', async () => {
    const w = render()
    const trigger = w.get('[data-testid="trigger"]')
    await trigger.trigger('click')
    await flushPromises()
    document.body.querySelector<HTMLButtonElement>('[data-testid="last"]')!.click()
    await flushPromises()
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(trigger.element)
    w.unmount()
  })

  it('Tab past the last control closes the panel so the focus continues from the trigger', async () => {
    const w = render()
    const trigger = w.get('[data-testid="trigger"]')
    await trigger.trigger('click')
    await flushPromises()
    const last = document.body.querySelector<HTMLElement>('[data-testid="last"]')!
    last.focus()
    last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    await flushPromises()
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(trigger.element)
    w.unmount()
  })

  it('Shift+Tab from the first control goes back to the trigger', async () => {
    const w = render()
    const trigger = w.get('[data-testid="trigger"]')
    await trigger.trigger('click')
    await flushPromises()
    const first = document.body.querySelector<HTMLElement>('[data-testid="first"]')!
    first.focus()
    const e = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true })
    first.dispatchEvent(e)
    await flushPromises()
    expect(e.defaultPrevented).toBe(true)
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(trigger.element)
    w.unmount()
  })

  it('Tab between the controls inside the panel leaves it open', async () => {
    const w = render()
    await w.get('[data-testid="trigger"]').trigger('click')
    await flushPromises()
    const first = document.body.querySelector<HTMLElement>('[data-testid="first"]')!
    first.focus()
    first.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    await flushPromises()
    expect(panel()).not.toBeNull()
    w.unmount()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AppHeader from '../AppHeader.vue'
import { useUIStore } from '../../stores/ui'

const mountHeader = () => mount(AppHeader, { global: { stubs: { UserMenu: true, RouterLink: true } } })

describe('AppHeader palette trigger', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('is a real button that opens the palette and advertises the hotkey', async () => {
    const ui = useUIStore()
    const wrapper = mountHeader()
    const trigger = wrapper.get('button[aria-label="Open command palette"]')
    expect(trigger.attributes('type')).toBe('button')
    expect(trigger.attributes('aria-haspopup')).toBe('dialog')
    expect(trigger.attributes('aria-keyshortcuts')).toMatch(/^(Meta|Control)\+K$/)
    expect(trigger.get('kbd').text()).toMatch(/^(⌘K|Ctrl K)$/)
    expect(trigger.text()).toContain('Search or jump to')

    await trigger.trigger('click')
    expect(ui.commandPaletteOpen).toBe(true)
    // The palette's chunk is requested by the first open.
    expect(ui.commandPaletteLoaded).toBe(true)
  })

  it('is a banner with the user menu and no navigation of its own', () => {
    const wrapper = mountHeader()
    expect(wrapper.element.tagName).toBe('HEADER')
    expect(wrapper.find('nav').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'UserMenu' }).exists()).toBe(true)
  })
})

describe('AppHeader mobile navigation toggle', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('opens and closes the off-canvas menu and reflects it in aria-expanded', async () => {
    const ui = useUIStore()
    const wrapper = mountHeader()
    const btn = wrapper.get('button[aria-label="Open navigation"]')
    expect(btn.attributes('id')).toBe('mobile-nav-toggle')
    expect(btn.attributes('aria-controls')).toBe('app-sidebar')
    expect(btn.attributes('aria-expanded')).toBe('false')
    expect(btn.classes()).toContain('md:hidden')

    await btn.trigger('click')
    expect(ui.mobileNavOpen).toBe(true)
    expect(btn.attributes('aria-expanded')).toBe('true')

    await btn.trigger('click')
    expect(ui.mobileNavOpen).toBe(false)
  })
})

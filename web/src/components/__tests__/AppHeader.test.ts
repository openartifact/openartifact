// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AppHeader from '../AppHeader.vue'
import { useUIStore } from '../../stores/ui'

describe('AppHeader mobile navigation toggle', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('opens and closes the off-canvas drawer and reflects it in aria-expanded', async () => {
    const ui = useUIStore()
    const wrapper = mount(AppHeader, { global: { stubs: { ThemeToggle: true } } })
    const btn = wrapper.get('button[aria-label="Open navigation"]')
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

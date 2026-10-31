// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { useToastStore } from '../../stores/toast'
import ToastBanner from '../ToastBanner.vue'
import ToastHost from '../ToastHost.vue'

const live = { global: { stubs: { TransitionGroup: false } } }

describe('toasts', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('floats a timed toast with its countdown, and runs its action and dismisses it', async () => {
    const toasts = useToastStore()
    let ran = 0
    toasts.push({ level: 'success', title: 'Quarantined', action: { label: 'Undo', run: () => ran++ } })

    const wrapper = mount(ToastHost, live)
    expect(wrapper.text()).toContain('Quarantined')
    expect(wrapper.find('.toast-progress').exists()).toBe(true)
    await wrapper.findAll('button').find((b) => b.text() === 'Undo')!.trigger('click')
    expect(ran).toBe(1)
    expect(toasts.queue).toHaveLength(0)
  })

  it('docks a toast that stays in the page flow instead of floating it', async () => {
    const toasts = useToastStore()
    let ran = 0
    toasts.push({ level: 'info', title: 'New version available', timeoutMs: 0, action: { label: 'Reload', run: () => ran++ } })

    const host = mount(ToastHost, live)
    expect(host.text()).not.toContain('New version available')

    const banner = mount(ToastBanner, live)
    expect(banner.text()).toContain('New version available')
    // A sticky toast has no countdown bar.
    expect(banner.find('.toast-progress').exists()).toBe(false)
    // In the flow, not fixed over the page.
    expect(banner.classes()).not.toContain('fixed')

    await banner.findAll('button').find((b) => b.text() === 'Reload')!.trigger('click')
    expect(ran).toBe(1)
    expect(toasts.queue).toHaveLength(0)
  })

  it('renders no banner row while nothing sticks, and none for a timed toast', () => {
    const toasts = useToastStore()
    const banner = mount(ToastBanner, live)
    expect(banner.find('[role="region"]').exists()).toBe(false)
    toasts.info('Saved')
    expect(mount(ToastBanner, live).find('[role="region"]').exists()).toBe(false)
  })

  it('lets the user dismiss either kind', async () => {
    const toasts = useToastStore()
    toasts.push({ level: 'info', title: 'Stays', timeoutMs: 0 })
    toasts.error('Failed')
    await mount(ToastBanner, live).get('button[aria-label="Dismiss notification"]').trigger('click')
    await mount(ToastHost, live).get('button[aria-label="Dismiss notification"]').trigger('click')
    expect(toasts.queue).toHaveLength(0)
  })
})

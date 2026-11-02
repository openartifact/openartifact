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

  // Screen readers announce text added to a live region that already exists: both regions are there from the start,
  // an error interrupts (`alert`), everything else waits its turn (`status`).
  it('has its live regions before the first toast, and files an error under alert and a success under status', async () => {
    const toasts = useToastStore()
    const host = mount(ToastHost, live)
    expect(host.find('[data-testid="toast-urgent"]').attributes('aria-live')).toBe('assertive')
    // A permanent role=alert would collide with the inline alerts of forms (the sign-in error).
    expect(host.find('[role="alert"]').exists()).toBe(false)
    expect(host.find('[role="status"]').exists()).toBe(true)
    expect(host.get('[role="region"]').attributes('aria-label')).toBe('Notifications')

    toasts.success('Copied to clipboard')
    toasts.error('Could not save')
    toasts.warning('Quota almost full')
    await host.vm.$nextTick()
    expect(host.get('[role="status"]').text()).toContain('Copied to clipboard')
    expect(host.get('[role="status"]').text()).not.toContain('Could not save')
    expect(host.get('[data-testid="toast-urgent"]').text()).toContain('Could not save')
    expect(host.get('[data-testid="toast-urgent"]').text()).toContain('Quota almost full')
  })

  it('docks the sticky toast inside a status region that is always in the page', () => {
    const toasts = useToastStore()
    const banner = mount(ToastBanner, live)
    expect(banner.attributes('role')).toBe('status')
    expect(banner.find('[role="region"]').exists()).toBe(false)
    toasts.push({ level: 'info', title: 'New version available', timeoutMs: 0 })
    return banner.vm.$nextTick().then(() => {
      expect(banner.get('[role="status"] [role="region"]').text()).toContain('New version available')
    })
  })

  it('stops the countdown while the pointer or focus is on a toast, and starts it again when both have left', async () => {
    const toasts = useToastStore()
    const id = toasts.push({ level: 'info', title: 'Saved' })
    const host = mount(ToastHost, live)
    const card = host.get('[data-testid="toast"]')
    expect(host.find('.toast-held').exists()).toBe(false)

    await card.trigger('mouseenter')
    await card.trigger('focusin')
    expect(host.find('.toast-held').exists()).toBe(true)
    await card.trigger('mouseleave')
    expect(host.find('.toast-held').exists()).toBe(true) // focus still there
    await card.trigger('focusout')
    expect(host.find('.toast-held').exists()).toBe(false)
    expect(toasts.queue.map((t) => t.id)).toEqual([id])
  })
})

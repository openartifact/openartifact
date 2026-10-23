// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { useToastStore } from '../../stores/toast'
import ToastHost from '../ToastHost.vue'

describe('ToastHost action', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('renders the action button, runs it and dismisses the toast', async () => {
    const toasts = useToastStore()
    let ran = 0
    toasts.push({
      level: 'info',
      title: 'New version available',
      timeoutMs: 0,
      action: { label: 'Reload', run: () => ran++ },
    })

    const wrapper = mount(ToastHost, { global: { stubs: { TransitionGroup: false } } })
    const button = wrapper.findAll('button').find((b) => b.text() === 'Reload')
    expect(button).toBeTruthy()
    // A sticky toast has no countdown bar.
    expect(wrapper.find('.toast-progress').exists()).toBe(false)

    await button!.trigger('click')
    expect(ran).toBe(1)
    expect(toasts.queue).toHaveLength(0)
  })
})

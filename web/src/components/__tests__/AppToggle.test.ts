// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import AppToggle from '../AppToggle.vue'

describe('AppToggle', () => {
  it('is an accessible switch that mirrors modelValue', () => {
    const wrapper = mount(AppToggle, { props: { modelValue: true, label: 'Enabled' } })
    const el = wrapper.get('button')
    expect(el.attributes('role')).toBe('switch')
    expect(el.attributes('aria-checked')).toBe('true')
    expect(el.attributes('aria-label')).toBe('Enabled')
  })

  it('emits the inverted value on click', async () => {
    const wrapper = mount(AppToggle, { props: { modelValue: false, label: 'Enabled' } })
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([true])
  })

  it('does not emit while disabled', async () => {
    const wrapper = mount(AppToggle, { props: { modelValue: false, label: 'Enabled', disabled: true } })
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})

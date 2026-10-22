// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import AppButton from '../AppButton.vue'

describe('AppButton', () => {
  it('defaults to type=button so it never submits a form by accident', () => {
    const wrapper = mount(AppButton, { slots: { default: 'Save' } })
    expect(wrapper.get('button').attributes('type')).toBe('button')
  })

  it('blocks clicks and exposes aria-busy while loading', async () => {
    const wrapper = mount(AppButton, { props: { loading: true }, slots: { default: 'Save' } })
    const el = wrapper.get('button')
    expect(el.attributes('disabled')).toBeDefined()
    expect(el.attributes('aria-busy')).toBe('true')
    expect(el.find('svg').exists()).toBe(true)
    expect(el.text()).toContain('Save')
  })

  it('uses the danger contrast token for the danger variant', () => {
    const wrapper = mount(AppButton, { props: { variant: 'danger' }, slots: { default: 'Delete' } })
    expect(wrapper.get('button').classes()).toContain('text-danger-contrast')
  })
})

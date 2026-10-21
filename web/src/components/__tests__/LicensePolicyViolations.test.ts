// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import LicensePolicyViolations from '../LicensePolicyViolations.vue'

describe('LicensePolicyViolations', () => {
  const violations = [
    { policy_id: 'p1', policy_name: 'No copyleft', action: 'block' as const, violation: 'GPL-3.0 is denied' },
    { policy_id: 'p2', policy_name: 'Review', action: 'warn' as const, violation: 'LGPL-2.1 needs review' },
  ]

  it('lists each violation with its action and emphasises block violations', () => {
    const w = mount(LicensePolicyViolations, { props: { violations } })
    const items = w.findAll('li')
    expect(items).toHaveLength(2)
    expect(items[0]!.attributes('data-action')).toBe('block')
    expect(items[0]!.text()).toContain('Block')
    expect(items[0]!.text()).toContain('GPL-3.0 is denied')
    expect(items[0]!.text()).toContain('No copyleft')
    expect(items[0]!.classes()).toContain('font-semibold')
    expect(items[1]!.text()).toContain('Warn')
    expect(items[1]!.classes()).not.toContain('font-semibold')
  })

  it('renders nothing without violations', () => {
    const w = mount(LicensePolicyViolations, { props: { violations: [] } })
    expect(w.find('ul').exists()).toBe(false)
  })
})

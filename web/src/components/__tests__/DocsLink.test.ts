// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import DocsLink from '../DocsLink.vue'

describe('DocsLink', () => {
  it('renders a new-tab link with the default label', () => {
    const w = mount(DocsLink, { props: { url: 'https://docs.example.com/v1/security/sso' } })
    const a = w.get('a')
    expect(a.attributes('href')).toBe('https://docs.example.com/v1/security/sso')
    expect(a.attributes('target')).toBe('_blank')
    expect(a.attributes('rel')).toBe('noopener')
    expect(a.text()).toBe('Read the documentation')
    expect(a.get('svg').attributes('aria-hidden')).toBe('true')
  })

  it('takes the label from the slot', () => {
    const w = mount(DocsLink, { props: { url: 'https://docs.example.com/' }, slots: { default: 'Webhook events' } })
    expect(w.get('a').text()).toBe('Webhook events')
  })

  it('renders nothing without a url', () => {
    for (const url of [null, '', undefined]) {
      expect(mount(DocsLink, { props: { url } }).find('a').exists()).toBe(false)
    }
  })
})

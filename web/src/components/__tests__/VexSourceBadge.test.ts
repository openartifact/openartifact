// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import VexSourceBadge from '../VexSourceBadge.vue'

describe('VexSourceBadge', () => {
  it('names an administrator decision, also when the source is absent', () => {
    expect(mount(VexSourceBadge, { props: { record: { source_kind: 'local' } } }).text()).toBe('Admin')
    expect(mount(VexSourceBadge, { props: { record: {} } }).text()).toBe('Admin')
  })

  it('links a feed decision to its CSAF document in a new tab', () => {
    const w = mount(VexSourceBadge, {
      props: { record: { source_kind: 'csaf', source_name: 'acme-psirt', source_ref: 'https://acme.example/csaf/2026/acme-1.json' } },
    })
    const a = w.get('[data-testid="vex-source-link"]')
    expect(a.attributes('href')).toBe('https://acme.example/csaf/2026/acme-1.json')
    expect(a.attributes('target')).toBe('_blank')
    expect(a.attributes('rel')).toContain('noopener')
    expect(a.text()).toContain('acme-psirt')
    expect(w.text()).toContain('Vendor feed')
  })

  it('never links a reference that is not an http(s) URL', () => {
    const w = mount(VexSourceBadge, { props: { record: { source_kind: 'csaf', source_name: 'acme', source_ref: 'javascript:alert(1)' } } })
    expect(w.find('a').exists()).toBe(false)
    expect(w.text()).toContain('acme')
  })

  it('shows the peer name with its base URL as the title', () => {
    const w = mount(VexSourceBadge, { props: { record: { source_kind: 'peer', source_name: 'site-b', source_ref: 'https://b.example.org' } } })
    expect(w.find('a').exists()).toBe(false)
    expect(w.text()).toContain('Peer')
    expect(w.get('[title="https://b.example.org"]').text()).toBe('site-b')
  })
})

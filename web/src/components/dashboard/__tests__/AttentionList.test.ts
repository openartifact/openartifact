// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { mount, RouterLinkStub } from '@vue/test-utils'
import AttentionList from '../AttentionList.vue'
import type { AttentionItem } from '../../../lib/attention'

const item = (id: string, name: string): AttentionItem => ({ id, kind: 'scanner-down', severity: 'high', params: { name }, to: '/security' })

const render = (items: AttentionItem[]) =>
  mount(AttentionList, { props: { items, loading: false, failed: 0 }, global: { stubs: { RouterLink: RouterLinkStub } } })

describe('AttentionList', () => {
  it('links the header to the security inbox, also when nothing needs attention', () => {
    for (const items of [[], [item('a', 'grype')]]) {
      const link = render(items).get('[data-testid="attention-security"]')
      expect(link.text()).toBe('Open Security')
      expect(link.getComponent(RouterLinkStub).props('to')).toBe('/security')
    }
  })

  it('tells the Open links apart for assistive technology', () => {
    const w = render([item('a', 'grype'), item('b', 'trivy')])
    const labels = w.findAll('li a[aria-label]').map((a) => a.attributes('aria-label'))
    expect(labels).toEqual(['Open: Scanner grype is not ready', 'Open: Scanner trivy is not ready'])
  })
})

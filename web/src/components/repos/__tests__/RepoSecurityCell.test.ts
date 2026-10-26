// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { mount } from '@vue/test-utils'
import RepoSecurityCell from '../RepoSecurityCell.vue'

const item = (security: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
  ({ stats: { package_count: 3, size_bytes: 1, quota: { used_bytes: 1 }, security: { critical: 0, high: 0, medium: 0, low: 0, secrets: 0, license_violations: 0, ...security }, ...extra } }) as never

describe('RepoSecurityCell', () => {
  it('shows the worst finding and how many more there are, with every count in the title', () => {
    const w = mount(RepoSecurityCell, { props: { item: item({ critical: 1, high: 7, secrets: 1 }) } })
    const cell = w.get('[data-testid=security-findings]')
    expect(cell.text()).toContain('1')
    expect(cell.text()).toContain('critical')
    expect(w.get('[data-testid=security-more]').text()).toBe('+8 more')
    expect(cell.attributes('title')).toBe('1 critical, 7 high, 1 secret')
  })

  it('names secrets and license issues when there is no vulnerability, without a "more" note for a single kind', () => {
    const w = mount(RepoSecurityCell, { props: { item: item({ secrets: 2 }) } })
    expect(w.get('[data-testid=security-findings]').text()).toBe('2 secrets')
    expect(w.find('[data-testid=security-more]').exists()).toBe(false)
  })

  it('reads clean, unscanned and empty repositories', () => {
    expect(mount(RepoSecurityCell, { props: { item: item({ scanned_at: '2026-10-25T10:00:00Z' }) } }).text()).toBe('Clean')
    expect(mount(RepoSecurityCell, { props: { item: item({}) } }).text()).toBe('Not scanned')
    expect(mount(RepoSecurityCell, { props: { item: item({}, { package_count: 0 }) } }).text()).toBe('—')
  })
})

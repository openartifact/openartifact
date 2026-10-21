// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import VulnScanStatus from '../VulnScanStatus.vue'

const ERROR = 'trivy: scanner is not available on the server; osv: scan timed out'

describe('VulnScanStatus', () => {
  it('renders failed as a red badge with the sanitized error and the failed scanners', () => {
    const w = mount(VulnScanStatus, {
      props: { status: 'failed', scanError: ERROR, failedScanners: ['osv', 'trivy'] },
    })
    const badge = w.get('[data-testid="scan-status"]')
    expect(badge.text()).toBe('Failed')
    expect(badge.classes()).toContain('text-danger')
    expect(w.get('[data-testid="scan-error"]').text()).toBe(ERROR)
    expect(w.get('[data-testid="failed-scanners"]').text()).toContain('osv, trivy')
  })

  it('renders partial as an amber badge listing the failed scanners', () => {
    const w = mount(VulnScanStatus, {
      props: { status: 'partial', scanError: 'trivy: scan timed out', failedScanners: ['trivy'] },
    })
    const badge = w.get('[data-testid="scan-status"]')
    expect(badge.text()).toBe('Partial')
    expect(badge.classes()).toContain('text-warning')
    expect(w.get('[data-testid="failed-scanners"]').text()).toContain('trivy')
    expect(w.get('[data-testid="scan-error"]').text()).toBe('trivy: scan timed out')
  })

  it('shows nothing beyond the badge for a completed scan', () => {
    const w = mount(VulnScanStatus, { props: { status: 'completed', scanError: null, failedScanners: [] } })
    expect(w.get('[data-testid="scan-status"]').classes()).toContain('text-success')
    expect(w.find('[data-testid="scan-error"]').exists()).toBe(false)
    expect(w.find('[data-testid="failed-scanners"]').exists()).toBe(false)
  })

  it('tolerates a server that omits failed_scanners', () => {
    const w = mount(VulnScanStatus, { props: { status: 'failed', scanError: ERROR } })
    expect(w.find('[data-testid="failed-scanners"]').exists()).toBe(false)
    expect(w.get('[data-testid="scan-error"]').text()).toBe(ERROR)
  })
})

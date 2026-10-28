// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import VulnScanFindingsTable from '../VulnScanFindingsTable.vue'
import type { VulnScan } from '../../api/security'

const vuln = (over: Record<string, unknown>) => ({
  id: 'CVE-0', summary: 's', severity: 'LOW', cvss: 1, fixed_in: '', published_at: '', kev: false, epss: null, epss_percentile: null, epss_date: null, priority: 'low', ...over,
})

function scan(vulnerabilities: unknown[]): VulnScan {
  return { id: 's1', artifact_id: 'a1', vulnerabilities } as VulnScan
}

function mountTable(vulnerabilities: unknown[]) {
  return mount(VulnScanFindingsTable, { props: { scan: scan(vulnerabilities), vexByVuln: new Map(), canDecide: false } })
}

describe('VulnScanFindingsTable', () => {
  const list = [
    vuln({ id: 'CVE-LOW', severity: 'LOW', cvss: 3, priority: 'low' }),
    vuln({ id: 'CVE-CRIT-7', severity: 'CRITICAL', cvss: 7, priority: 'critical' }),
    vuln({ id: 'CVE-EPSS', severity: 'MEDIUM', cvss: 5, priority: 'epss', epss: 0.42, epss_percentile: 0.97, epss_date: '2026-10-27' }),
    vuln({ id: 'CVE-KEV', severity: 'HIGH', cvss: 8, priority: 'kev', cve: 'CVE-KEV', kev: true, kev_date_added: '2024-01-02', kev_due_date: '2024-01-23', kev_ransomware: true, epss: 0.97, epss_percentile: 0.999, epss_date: '2026-10-27' }),
    vuln({ id: 'CVE-CRIT-9', severity: 'CRITICAL', cvss: 9.8, priority: 'critical' }),
  ]

  it('orders by priority rank, then severity, then CVSS', () => {
    const ids = mountTable(list).findAll('[data-testid="finding-row"]').map((r) => r.find('td').text())
    expect(ids).toEqual(['CVE-KEV', 'CVE-EPSS', 'CVE-CRIT-9', 'CVE-CRIT-7', 'CVE-LOW'])
  })

  it('shows the KEV and EPSS badges of each vulnerability', () => {
    const rows = mountTable(list).findAll('[data-testid="finding-row"]')
    const kev = rows[0].get('[data-testid="finding-exploit"]')
    expect(kev.find('[data-testid="kev-badge"]').exists()).toBe(true)
    expect(kev.get('[data-testid="kev-badge"]').attributes('title')).toContain('Known to be used in ransomware campaigns')
    expect(kev.get('[data-testid="epss-badge"]').text()).toBe('EPSS 97.0%')
    const epss = rows[1].get('[data-testid="finding-exploit"]')
    expect(epss.find('[data-testid="kev-badge"]').exists()).toBe(false)
    expect(epss.get('[data-testid="epss-badge"]').text()).toBe('EPSS 42.0%')
    expect(epss.get('[data-testid="epss-badge"]').classes()).toContain('bg-primary-soft')
    const plain = rows[4].get('[data-testid="finding-exploit"]')
    expect(plain.find('[data-testid="kev-badge"]').exists()).toBe(false)
    expect(plain.find('[data-testid="epss-badge"]').exists()).toBe(false)
  })

  it('shows the CVE id under an advisory id it was found through', () => {
    const w = mountTable([vuln({ id: 'GHSA-xxxx', cve: 'CVE-2026-1', kev: true, priority: 'kev' }), vuln({ id: 'CVE-2', cve: 'CVE-2' })])
    const rows = w.findAll('[data-testid="finding-row"]')
    expect(rows[0].get('[data-testid="finding-cve"]').text()).toBe('CVE-2026-1')
    expect(rows[1].find('[data-testid="finding-cve"]').exists()).toBe(false)
  })

  it('falls back to the severity order when the server sent no exploitation data', () => {
    const bare = [{ id: 'A', severity: 'LOW', cvss: 9 }, { id: 'B', severity: 'CRITICAL', cvss: 1 }]
    const ids = mountTable(bare).findAll('[data-testid="finding-row"]').map((r) => r.find('td').text())
    expect(ids).toEqual(['B', 'A'])
  })
})

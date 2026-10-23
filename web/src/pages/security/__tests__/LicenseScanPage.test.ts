// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import LicenseScanPage from '../LicenseScanPage.vue'
import { REPO, makeRouter, mockSecurityApi, selectRepo } from './scanPageHarness'

const base = {
  repo_id: 'r1',
  scan_status: 'completed',
  created_at: '2026-10-20T10:00:00Z',
  updated_at: '2026-10-20T10:00:00Z',
  scanned_at: '2026-10-20T10:00:00Z',
  scan_error: null,
  spdx_expressions: [],
}
const QUARANTINED = { ...base, id: 'q-scan', artifact_id: 'art-1', licenses: ['GPL-3.0'], quarantined: true, quarantine_approved: false }
const OK = { ...base, id: 'ok-scan', artifact_id: 'art-2', licenses: ['MIT'], quarantined: false, quarantine_approved: false }
const APPROVED = { ...base, id: 'a-scan', artifact_id: 'art-3', licenses: ['GPL-3.0'], quarantined: false, quarantine_approved: true }

// PageHeader reads the router; the page tests only need its action slot.
const PageHeaderStub = { template: '<div><slot name="actions" /></div>' }

async function mountPage(extra?: Parameters<typeof mockSecurityApi>[2]) {
  const calls = mockSecurityApi(`/api/v1/repositories/${REPO}/license/scans`, [QUARANTINED, OK, APPROVED], extra)
  const router = await makeRouter('/security/licenses')
  const wrapper = mount(LicenseScanPage, { global: { plugins: [createPinia(), router], stubs: { RouterLink: true, PageHeader: PageHeaderStub } } })
  await selectRepo(wrapper)
  return { wrapper, calls }
}

const rowOf = (wrapper: ReturnType<typeof mount>, license: string, badge?: string) =>
  wrapper.findAll('tbody tr').find((r) => r.text().includes(license) && (!badge || r.find(badge).exists()))!

describe('LicenseScanPage initial selection', () => {
  it('shows the scans of the first repository without any user input and records it in the URL', async () => {
    const calls = mockSecurityApi(`/api/v1/repositories/${REPO}/license/scans`, [OK])
    const router = await makeRouter('/security/licenses')
    const wrapper = mount(LicenseScanPage, { global: { plugins: [createPinia(), router], stubs: { RouterLink: true, PageHeader: PageHeaderStub } } })
    await flushPromises()

    expect(calls.some((c) => c.path === `/api/v1/repositories/${REPO}/license/scans`)).toBe(true)
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(router.currentRoute.value.query.repo).toBe(REPO)
  })
})

describe('LicenseScanPage quarantine', () => {
  it('badges quarantined and approved scans and offers Approve / Quarantine accordingly', async () => {
    const { wrapper } = await mountPage()
    const quarantined = rowOf(wrapper, 'GPL-3.0', '[data-testid="license-quarantined"]')
    expect(quarantined.get('[data-testid="license-quarantined"]').text()).toBe('Quarantined')
    expect(quarantined.get('button').text()).toBe('Approve')

    const approved = rowOf(wrapper, 'GPL-3.0', '[data-testid="license-approved"]')
    expect(approved.get('button').text()).toBe('Quarantine')

    const ok = rowOf(wrapper, 'MIT')
    expect(ok.find('[data-testid="license-quarantined"]').exists()).toBe(false)
    expect(ok.get('button').text()).toBe('Quarantine')
  })

  it('Approve posts quarantined:false to the license quarantine endpoint and updates the row', async () => {
    const { wrapper, calls } = await mountPage((c) =>
      c.method === 'POST' ? { ...QUARANTINED, quarantined: false, quarantine_approved: true } : {},
    )
    await rowOf(wrapper, 'GPL-3.0', '[data-testid="license-quarantined"]').get('button').trigger('click')
    await flushPromises()

    const post = calls.find((c) => c.method === 'POST')!
    expect(post.path).toBe(`/api/v1/repositories/${REPO}/license/scans/q-scan/quarantine`)
    expect(post.body).toEqual({ quarantined: false })
    expect(wrapper.findAll('[data-testid="license-quarantined"]')).toHaveLength(0)
    expect(wrapper.findAll('[data-testid="license-approved"]')).toHaveLength(2)
  })

  it('Quarantine posts quarantined:true', async () => {
    const { wrapper, calls } = await mountPage((c) =>
      c.method === 'POST' ? { ...OK, quarantined: true } : {},
    )
    await rowOf(wrapper, 'MIT').get('button').trigger('click')
    await flushPromises()

    const post = calls.find((c) => c.method === 'POST')!
    expect(post.path).toBe(`/api/v1/repositories/${REPO}/license/scans/ok-scan/quarantine`)
    expect(post.body).toEqual({ quarantined: true })
    expect(rowOf(wrapper, 'MIT').find('[data-testid="license-quarantined"]').exists()).toBe(true)
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import VexStatementsPanel from '../VexStatementsPanel.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const STATEMENTS = [
  {
    id: 's1',
    artifact_id: 'art-1',
    vuln_id: 'CVE-1',
    status: 'not_affected',
    justification: 'component_not_present',
    impact_statement: 'not shipped',
    updated_at: '2026-10-20T10:00:00Z',
  },
  { id: 's2', artifact_id: 'art-2', vuln_id: 'CVE-2', status: 'affected', updated_at: '2026-10-21T10:00:00Z' },
]

let wrapper: VueWrapper | undefined
let fetchSpy: Mock<typeof fetch>

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  fetchSpy?.mockRestore()
})

async function mountPanel(repo: string, statements: unknown[] = STATEMENTS): Promise<void> {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (_url, init) => {
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    return json({ statements })
  })
  wrapper = mount(VexStatementsPanel, {
    props: { repo, artifactLabels: { 'art-1': 'left-pad@1.0.0' } },
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
  await flushPromises()
}

describe('VexStatementsPanel', () => {
  it('asks for a repository instead of listing', async () => {
    await mountPanel('')
    expect(wrapper!.find('[data-testid="vex-select-repo"]').exists()).toBe(true)
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(wrapper!.find('[data-testid="vex-repo-download-openvex"]').exists()).toBe(false)
  })

  it('lists the recorded decisions with artifact name, status, justification and impact', async () => {
    await mountPanel('main')
    expect(String(fetchSpy.mock.calls[0]![0])).toBe('/api/v1/repositories/main/vex/statements')
    const rows = wrapper!.findAll('[data-testid="vex-statement-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('left-pad@1.0.0')
    expect(rows[0]!.text()).toContain('CVE-1')
    expect(rows[0]!.text()).toContain('Not affected')
    expect(rows[0]!.text()).toContain('Component not present')
    expect(rows[0]!.text()).toContain('not shipped')
    expect(rows[1]!.text()).toContain('art-2')
    expect(rows[1]!.text()).toContain('Affected')
    expect(wrapper!.get('[data-testid="vex-count"]').text()).toContain('2')
  })

  it('shows the empty state', async () => {
    await mountPanel('main', [])
    expect(wrapper!.find('[data-testid="vex-statements-empty"]').exists()).toBe(true)
  })

  it('offers both repository-wide downloads', async () => {
    await mountPanel('main')
    expect(wrapper!.find('[data-testid="vex-repo-download-openvex"]').text()).toContain('OpenVEX')
    expect(wrapper!.find('[data-testid="vex-repo-download-cyclonedx"]').text()).toContain('CycloneDX VEX')
  })

  it('deletes a decision after the confirmation and tells the page', async () => {
    await mountPanel('main')
    await wrapper!.findAll('[data-testid="vex-statement-delete"]')[0]!.trigger('click')
    await flushPromises()
    expect(fetchSpy.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)

    const confirm = Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')).find(
      (b) => b.textContent?.trim() === 'Delete',
    )!
    confirm.click()
    await flushPromises()

    const del = fetchSpy.mock.calls.find(([, init]) => init?.method === 'DELETE')!
    expect(String(del[0])).toBe('/api/v1/repositories/main/vex/statements/s1')
    expect(wrapper!.findAll('[data-testid="vex-statement-row"]')).toHaveLength(1)
    expect(wrapper!.emitted('changed')).toHaveLength(1)
  })

  it('reports a load failure with a retry', async () => {
    fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ error: 'admin access required' }, 403))
    wrapper = mount(VexStatementsPanel, {
      props: { repo: 'main' },
      global: { plugins: [createPinia()] },
      attachTo: document.body,
    })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('admin access required')
  })
})

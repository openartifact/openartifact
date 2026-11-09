// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import VexSuggestionsPanel from '../VexSuggestionsPanel.vue'
import type { VexSuggestion } from '../../api/vex'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const SUGGESTION: VexSuggestion = {
  id: 'sug-1',
  artifact_id: 'art-1',
  vuln_id: 'CVE-2026-1',
  status: 'not_affected',
  justification: 'vulnerable_code_not_present',
  impact_statement: 'the parser is never built',
  action_statement: '',
  peer_id: 'p1',
  peer_name: 'site-b',
  peer_base_url: 'https://b.example.org',
  peer_updated_at: '2026-11-01T10:00:00Z',
  state: 'pending',
  decided_by: '',
  decided_at: null,
  local_status: 'affected',
  local_source_kind: 'local',
  created_at: '2026-11-01T10:00:00Z',
  updated_at: '2026-11-01T10:00:00Z',
}

let wrapper: VueWrapper | undefined
let fetchSpy: Mock<typeof fetch>

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  fetchSpy?.mockRestore()
})

async function mountPanel(): Promise<void> {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (url, init) => {
    const u = String(url)
    if (u.endsWith('/accept')) return json({ id: 's1', artifact_id: 'art-1', vuln_id: 'CVE-2026-1', status: 'not_affected', source_kind: 'peer' })
    if (u.endsWith('/dismiss')) return json({ ...SUGGESTION, state: 'dismissed' })
    if (init?.method === undefined || init.method === 'GET') return json({ suggestions: [SUGGESTION] })
    return json({ error: 'unexpected' }, 500)
  })
  wrapper = mount(VexSuggestionsPanel, {
    props: { repo: 'main', artifactLabels: { 'art-1': 'left-pad@1.0.0' } },
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
  await flushPromises()
}

const posts = (): string[] => fetchSpy.mock.calls.filter(([, init]) => init?.method === 'POST').map(([url]) => String(url))

describe('VexSuggestionsPanel', () => {
  it('lists the pending suggestions with the suggested and the current decision', async () => {
    await mountPanel()
    expect(String(fetchSpy.mock.calls[0]![0])).toBe('/api/v1/repositories/main/vex/suggestions?state=pending')
    const row = wrapper!.get('[data-testid="vex-suggestion-row"]')
    expect(row.text()).toContain('left-pad@1.0.0')
    expect(row.text()).toContain('CVE-2026-1')
    expect(row.text()).toContain('Not affected')
    expect(row.text()).toContain('Vulnerable code not present')
    expect(row.text()).toContain('site-b')
    expect(row.text()).toContain('https://b.example.org')
    expect(row.text()).toContain('Affected')
    expect(row.text()).toContain('Admin')
  })

  it('reloads with the chosen state filter', async () => {
    await mountPanel()
    await wrapper!.get('[data-testid="vex-suggestions-state"]').setValue('all')
    await flushPromises()
    expect(String(fetchSpy.mock.calls.at(-1)![0])).toBe('/api/v1/repositories/main/vex/suggestions?state=all')
  })

  it('accepts a suggestion, tells the page and reloads the list', async () => {
    await mountPanel()
    const before = fetchSpy.mock.calls.length
    await wrapper!.get('[data-testid="action-accept"]').trigger('click')
    await flushPromises()
    expect(posts()).toEqual(['/api/v1/repositories/main/vex/suggestions/sug-1/accept'])
    expect(wrapper!.emitted('accepted')).toHaveLength(1)
    expect(fetchSpy.mock.calls.length).toBe(before + 2)
  })

  it('dismisses a suggestion without touching the decisions', async () => {
    await mountPanel()
    await wrapper!.get('[data-testid="action-dismiss"]').trigger('click')
    await flushPromises()
    expect(posts()).toEqual(['/api/v1/repositories/main/vex/suggestions/sug-1/dismiss'])
    expect(wrapper!.emitted('accepted')).toBeUndefined()
  })

  it('does not tell the page when the accept fails', async () => {
    await mountPanel()
    fetchSpy.mockImplementation(async () => json({ error: 'suggestion not found' }, 404))
    await wrapper!.get('[data-testid="action-accept"]').trigger('click')
    await flushPromises()
    expect(wrapper!.emitted('accepted')).toBeUndefined()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ArtifactAIAnalysis from '../artifact/ArtifactAIAnalysis.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const FAILED = {
  id: 'a1', artifact_id: 'f1', artifact_format: 'npm', artifact_name: 'widget', artifact_version: '1.0.0',
  summary: '', tags: [], license_status: 'UNKNOWN', license_name: '', security_notes: '', quarantine_recommended: false,
  status: 'failed', error_message: 'no manifest found', model_used: 'm', updated_at: '2026-02-01T00:00:00Z',
}

describe('ArtifactAIAnalysis', () => {
  afterEach(() => { spyOn(globalThis, 'fetch').mockRestore() })

  it('shows an empty hint when no file was analyzed (404)', async () => {
    setActivePinia(createPinia())
    spyOn(globalThis, 'fetch').mockImplementation(async () => json({ error: 'not found' }, 404))
    const w = mount(ArtifactAIAnalysis, { props: { repo: 'r', artifactIds: ['f1'] } })
    await flushPromises()
    expect(w.find('[data-testid="ai-none"]').exists()).toBe(true)
    expect(w.find('[data-testid="ai-analysis"]').exists()).toBe(false)
  })

  it('shows a failed analysis with its error and a re-run button', async () => {
    setActivePinia(createPinia())
    spyOn(globalThis, 'fetch').mockImplementation(async () => json(FAILED))
    const w = mount(ArtifactAIAnalysis, { props: { repo: 'r', artifactIds: ['f1'] } })
    await flushPromises()
    expect(w.find('[data-testid="ai-error"]').text()).toContain('no manifest found')
    expect(w.find('[data-testid="ai-retrigger"]').exists()).toBe(true)
  })
})

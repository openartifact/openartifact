// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import VexDecisionDialog from '../VexDecisionDialog.vue'
import type { VexRecord } from '../../api/vex'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const EXISTING: VexRecord = {
  id: 'stmt-1',
  artifact_id: 'art-1',
  vuln_id: 'CVE-2024-1',
  status: 'not_affected',
  justification: 'vulnerable_code_not_present',
  impact_statement: 'we never call it',
  action_statement: 'none needed',
  created_by: 'admin',
  updated_at: '2026-10-20T10:00:00Z',
}

let wrapper: VueWrapper | undefined
let fetchSpy: Mock<typeof fetch>

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  fetchSpy?.mockRestore()
})

async function mountDialog(existing: VexRecord | null): Promise<void> {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (_url, init) => {
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    return json({ ...JSON.parse(String(init?.body ?? '{}')), id: 'stmt-new' })
  })
  wrapper = mount(VexDecisionDialog, {
    props: { open: true, repo: 'main', artifactId: 'art-1', artifactLabel: 'left-pad@1.0.0', vulnId: 'CVE-2024-1', existing },
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
  await flushPromises()
}

const q = <T extends HTMLElement>(testId: string): T | null => document.body.querySelector<T>(`[data-testid="${testId}"]`)

async function choose(testId: string, value: string): Promise<void> {
  const el = q<HTMLSelectElement>(testId)!
  el.value = value
  el.dispatchEvent(new Event('change', { bubbles: true }))
  await flushPromises()
}

async function type(testId: string, value: string): Promise<void> {
  const el = q<HTMLTextAreaElement>(testId)!
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
}

async function click(testId: string): Promise<void> {
  q<HTMLButtonElement>(testId)!.click()
  await flushPromises()
}

function calls(method: string): [string, RequestInit][] {
  return fetchSpy.mock.calls.filter(([, init]) => init?.method === method).map(([url, init]) => [String(url), init as RequestInit])
}

describe('VexDecisionDialog', () => {
  it('shows the justification only for not_affected', async () => {
    await mountDialog(null)
    expect(q('vex-status')).not.toBeNull()
    expect(q('vex-justification')).toBeNull()

    await choose('vex-status', 'not_affected')
    expect(q('vex-justification')).not.toBeNull()
    expect(q('vex-justification')?.getAttribute('aria-required')).toBe('true')

    await choose('vex-status', 'fixed')
    expect(q('vex-justification')).toBeNull()
  })

  it('does not save a not_affected decision without a justification', async () => {
    await mountDialog(null)
    await choose('vex-status', 'not_affected')
    await click('vex-save')
    expect(calls('PUT')).toHaveLength(0)
    expect(document.body.querySelector('#vex-justification-error')?.textContent).toContain('Choose a justification')
    expect(q('vex-justification')?.getAttribute('aria-invalid')).toBe('true')

    await choose('vex-justification', 'component_not_present')
    await click('vex-save')
    expect(calls('PUT')).toHaveLength(1)
  })

  it('records the decision with PUT and emits saved', async () => {
    await mountDialog(null)
    await choose('vex-status', 'not_affected')
    await choose('vex-justification', 'component_not_present')
    await type('vex-impact', ' not shipped ')
    await type('vex-action', 'upgrade later')
    await click('vex-save')

    const [[url, init]] = calls('PUT') as [[string, RequestInit]]
    expect(url).toBe('/api/v1/repositories/main/vex/statements')
    expect(JSON.parse(String(init.body))).toEqual({
      artifact_id: 'art-1',
      vuln_id: 'CVE-2024-1',
      status: 'not_affected',
      justification: 'component_not_present',
      impact_statement: 'not shipped',
      action_statement: 'upgrade later',
    })
    expect(wrapper!.emitted('saved')).toHaveLength(1)
    expect((wrapper!.emitted('saved')![0] as [VexRecord])[0].id).toBe('stmt-new')
  })

  it('drops a stale justification when the status moves away from not_affected', async () => {
    await mountDialog(EXISTING)
    await choose('vex-status', 'affected')
    await click('vex-save')
    const body = JSON.parse(String(calls('PUT')[0]![1].body)) as Record<string, unknown>
    expect(body.status).toBe('affected')
    expect(body).not.toHaveProperty('justification')
  })

  it('prefills the recorded decision and offers delete only for it', async () => {
    await mountDialog(EXISTING)
    expect(q<HTMLSelectElement>('vex-status')?.value).toBe('not_affected')
    expect(q<HTMLSelectElement>('vex-justification')?.value).toBe('vulnerable_code_not_present')
    expect(q<HTMLTextAreaElement>('vex-impact')?.value).toBe('we never call it')
    expect(q<HTMLTextAreaElement>('vex-action')?.value).toBe('none needed')
    expect(q('vex-current')?.textContent).toContain('Not affected')
    expect(q('vex-delete')).not.toBeNull()
    wrapper!.unmount()

    await mountDialog(null)
    expect(q('vex-delete')).toBeNull()
  })

  it('deletes the decision only after the confirmation', async () => {
    await mountDialog(EXISTING)
    await click('vex-delete')
    expect(calls('DELETE')).toHaveLength(0)

    const confirm = Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')).find(
      (b) => b.textContent?.trim() === 'Delete',
    )!
    confirm.click()
    await flushPromises()

    expect(calls('DELETE').map(([url]) => url)).toEqual(['/api/v1/repositories/main/vex/statements/stmt-1'])
    expect(wrapper!.emitted('deleted')).toEqual([['stmt-1']])
  })

  it('keeps the dialog open and reports when the server refuses the decision', async () => {
    await mountDialog(null)
    fetchSpy.mockImplementation(async () => json({ error: 'admin access required' }, 403))
    await click('vex-save')
    expect(calls('PUT')).toHaveLength(1)
    expect(wrapper!.emitted('saved')).toBeUndefined()
    expect(q('vex-form')).not.toBeNull()
  })
})

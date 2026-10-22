// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ProjectTokensTab from '../ProjectTokensTab.vue'
import type { Project, ProjectToken } from '../../api/projects'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const TOKENS: ProjectToken[] = [
  { id: 't1', name: 'ci', project: 'p', role: 'developer', created_by_username: 'dev', created_at: '2026-01-01T00:00:00Z' },
]

let fetchSpy: Mock<typeof fetch>

function mountTab(project: Project, tokens = TOKENS) {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (_url, init) => {
    const method = init?.method ?? 'GET'
    if (method === 'POST') return json({ ...TOKENS[0], token: 'oa_dep_secret' }, 201)
    if (method === 'DELETE') return new Response(null, { status: 204 })
    return json({ tokens })
  })
  return mount(ProjectTokensTab, { props: { project }, global: { plugins: [createPinia()] }, attachTo: document.body })
}

const calls = (method: string) => fetchSpy.mock.calls.filter(([, init]) => (init?.method ?? 'GET') === method)

afterEach(() => {
  fetchSpy?.mockRestore()
  document.body.innerHTML = ''
})

describe('ProjectTokensTab', () => {
  it('lists the tokens', async () => {
    const wrapper = mountTab({ name: 'p', my_role: 'maintainer' })
    await flushPromises()
    expect(wrapper.findAll('[data-testid="token-row"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('ci')
  })

  it('mints a token with the capped expiry and shows the secret once', async () => {
    const wrapper = mountTab({ name: 'p', my_role: 'owner', token_max_ttl_days: 7 })
    await flushPromises()
    await wrapper.find('[data-testid="token-new"]').trigger('click')
    await flushPromises()
    const name = document.querySelector<HTMLInputElement>('#project-token-name')!
    name.value = 'deploy'
    name.dispatchEvent(new Event('input'))
    const days = document.querySelector<HTMLInputElement>('#project-token-days')!
    expect(days.max).toBe('7')
    expect(days.required).toBe(true)
    document.querySelector<HTMLFormElement>('#project-token-form')!.dispatchEvent(new Event('submit'))
    await flushPromises()
    const post = calls('POST')
    expect(post).toHaveLength(1)
    const body = JSON.parse(String(post[0]![1]!.body))
    expect(body.name).toBe('deploy')
    expect(body.role).toBe('developer')
    expect(new Date(body.expires_at).getTime()).toBeGreaterThan(Date.now())
    expect(document.body.textContent).toContain('oa_dep_secret')
  })

  it('offers viewers the viewer role only', async () => {
    const wrapper = mountTab({ name: 'p', my_role: 'viewer' })
    await flushPromises()
    await wrapper.find('[data-testid="token-new"]').trigger('click')
    await flushPromises()
    const options = [...document.querySelectorAll<HTMLOptionElement>('#project-token-form select option')]
    expect(options.filter((o) => !o.disabled).map((o) => o.value)).toEqual(['viewer'])
  })

  it('revokes after confirmation', async () => {
    const wrapper = mountTab({ name: 'p', my_role: 'owner' })
    await flushPromises()
    await wrapper.find('[data-testid="token-revoke"]').trigger('click')
    await flushPromises()
    const confirm = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Revoke token')
    confirm!.click()
    await flushPromises()
    expect(calls('DELETE')).toHaveLength(1)
    expect(String(calls('DELETE')[0]![0])).toContain('/api/v1/projects/p/tokens/t1')
  })
})

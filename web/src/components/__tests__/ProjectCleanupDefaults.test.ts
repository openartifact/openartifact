// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ProjectCleanupDefaults from '../ProjectCleanupDefaults.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const POLICIES = [
  { id: 'c1', name: 'keep-5', enabled: true, keep_last_n: 5, project_id: 'p1', created_at: '2026-01-01T00:00:00Z' },
]

let spy: Mock<typeof fetch>

function mountTab(canEdit: boolean) {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async (_u, init) => {
    if (init?.method === 'POST') return json(POLICIES[0], 201)
    return json({ policies: POLICIES })
  })
  return mount(ProjectCleanupDefaults, {
    props: { project: 'p', canEdit },
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
}

afterEach(() => {
  spy?.mockRestore()
  document.body.innerHTML = ''
})

describe('ProjectCleanupDefaults', () => {
  it('lists the policies read-only for members and states the override rule', async () => {
    const wrapper = mountTab(false)
    await flushPromises()
    expect(wrapper.findAll('[data-testid="cleanup-row"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('keep-5')
    expect(wrapper.find('[data-testid="cleanup-add"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="cleanup-readonly"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="cleanup-override-note"]').text()).toContain('its own enabled cleanup policy')
    expect(wrapper.find('button[aria-label="Delete policy"]').exists()).toBe(false)
  })

  it('lets an owner create a default policy', async () => {
    const wrapper = mountTab(true)
    await flushPromises()
    await wrapper.find('[data-testid="cleanup-add"]').trigger('click')
    await flushPromises()
    const name = document.querySelector<HTMLInputElement>('#cleanup-name')!
    name.value = 'weekly'
    name.dispatchEvent(new Event('input'))
    await flushPromises()
    const create = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Create policy'))!
    create.click()
    await flushPromises()

    const post = spy.mock.calls.find(([, init]) => init?.method === 'POST')!
    expect(String(post[0])).toBe('/api/v1/projects/p/cleanup-policies')
    expect(JSON.parse(post[1]!.body as string)).toEqual({ name: 'weekly', enabled: true, keep_last_n: 10 })
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ProjectPolicySection from '../ProjectPolicySection.vue'

interface Put {
  body: unknown
}

let spy: Mock<typeof fetch> | undefined
let puts: Put[] = []

function serve(selfCreate: boolean, putStatus = 200): void {
  puts = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (_input: RequestInfo | URL, init?: RequestInit) => {
    const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
    if (init?.method === 'PUT') {
      puts.push({ body: JSON.parse(String(init.body)) })
      return putStatus === 200 ? json(JSON.parse(String(init.body))) : json({ error: 'not allowed' }, putStatus)
    }
    return json({ self_create: selfCreate })
  }) as never)
}

async function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/project-policy')
  const w = mount(ProjectPolicySection, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

const toggle = (w: Awaited<ReturnType<typeof mountPage>>) => w.find('[role="switch"]')

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('ProjectPolicySection', () => {
  it('shows the current setting on a labelled switch', async () => {
    serve(true)
    const w = await mountPage()
    expect(toggle(w).attributes('aria-checked')).toBe('true')
    expect(toggle(w).attributes('aria-labelledby')).toBe('self-create-label')
    expect(w.find('#self-create-label').text()).toBe('Allow users to create projects')
    w.unmount()
  })

  it('saves the new value when switched', async () => {
    serve(false)
    const w = await mountPage()
    await toggle(w).trigger('click')
    await flushPromises()
    expect(puts).toEqual([{ body: { self_create: true } }])
    expect(toggle(w).attributes('aria-checked')).toBe('true')
    w.unmount()
  })

  it('keeps the old value and says why when saving fails', async () => {
    serve(false, 403)
    const w = await mountPage()
    await toggle(w).trigger('click')
    await flushPromises()
    expect(toggle(w).attributes('aria-checked')).toBe('false')
    expect(w.find('[role="alert"]').text()).toContain('not allowed')
    w.unmount()
  })

  it('explains what project roles grant and points to the projects', async () => {
    serve(false)
    const w = await mountPage()
    expect(w.find('[data-testid="project-effect"]').text()).toContain('never grant administration')
    expect(w.find('[data-testid="per-project"] a').attributes('href')).toBe('/projects')
    w.unmount()
  })

  it('offers a retry when the setting cannot be read', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation((async () => new Response(JSON.stringify({ error: 'down' }), { status: 500 })) as never)
    const w = await mountPage()
    expect(w.text()).toContain('Retry')
    expect(w.find('[role="switch"]').exists()).toBe(false)
    w.unmount()
  })
})

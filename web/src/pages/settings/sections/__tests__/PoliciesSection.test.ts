// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import PoliciesSection from '../PoliciesSection.vue'
import { useToastStore } from '../../../../stores/toast'

const POLICIES = [
  { id: 'a', name: 'No copyleft', action: 'block', denied_licenses: ['GPL-3.0-only', 'AGPL-3.0-only'], allowed_licenses: [], enabled: true, updated_at: '2026-03-01T00:00:00Z' },
  { id: 'b', name: 'Report LGPL', description: 'Informational', action: 'warn', denied_licenses: ['LGPL-2.1-only'], enabled: false, updated_at: '2026-02-01T00:00:00Z' },
]

interface Call {
  method: string
  body?: Record<string, unknown>
}

let spy: Mock<typeof fetch> | undefined
let posts: Call[] = []
let gets = 0

function serve(list: unknown[] = POLICIES, fail = false): void {
  posts = []
  gets = 0
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (_input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method === 'POST') {
      posts.push({ method, body: JSON.parse(String(init!.body)) })
      if (fail) return new Response(JSON.stringify({ error: 'name is too long' }), { status: 400, headers: { 'Content-Type': 'application/json' } })
      return new Response(JSON.stringify({ id: 'c', name: 'New one' }), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    gets++
    return new Response(JSON.stringify({ policies: list }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as never)
}

async function mountPage(path = '/settings/policies') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const w = mount(PoliciesSection, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return Object.assign(w, { router })
}

const field = (id: string) => document.body.querySelector(`#${id}`) as HTMLInputElement

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('PoliciesSection', () => {
  it('lists the global policies with their action, licenses and state in words', async () => {
    serve()
    const w = await mountPage()
    const rows = w.findAll('tbody tr').map((r) => r.text())
    expect(rows[0]).toContain('No copyleft')
    expect(rows[0]).toContain('Block')
    expect(rows[0]).toContain('Denied: GPL-3.0-only, AGPL-3.0-only')
    expect(rows[0]).toContain('Enabled')
    expect(rows[1]).toContain('Warn only')
    expect(rows[1]).toContain('Disabled')
    w.unmount()
  })

  it('filters by what the policy does', async () => {
    serve()
    const w = await mountPage()
    await w.findAll('button').find((b) => b.text() === 'Block')!.trigger('click')
    await flushPromises()
    expect(w.findAll('tbody tr')).toHaveLength(1)
    expect(w.router.currentRoute.value.query.action).toBe('block')
    w.unmount()
  })

  it('creates a policy from the drawer with parsed license lists and refreshes the list', async () => {
    serve()
    const w = await mountPage()
    await w.find('[data-testid="new-policy"]').trigger('click')
    await flushPromises()

    field('lp-name').value = 'New one'
    field('lp-name').dispatchEvent(new Event('input'))
    const action = field('lp-action') as unknown as HTMLSelectElement
    action.value = 'block'
    action.dispatchEvent(new Event('change'))
    const denied = field('lp-denied')
    denied.value = 'GPL-3.0-only, GPL-3.0-only\nAGPL-3.0-only'
    denied.dispatchEvent(new Event('input'))
    ;(document.body.querySelector('#license-policy-form') as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }))
    await flushPromises()

    expect(posts).toEqual([
      {
        method: 'POST',
        body: { name: 'New one', description: '', action: 'block', allowed_licenses: [], denied_licenses: ['GPL-3.0-only', 'AGPL-3.0-only'], enabled: true },
      },
    ])
    expect(gets).toBe(2)
    expect(useToastStore().queue.some((x) => x.level === 'success' && x.title.includes('New one'))).toBe(true)
    w.unmount()
  })

  it('requires a name and shows the server refusal', async () => {
    serve(POLICIES, true)
    const w = await mountPage()
    await w.find('[data-testid="new-policy"]').trigger('click')
    await flushPromises()

    const form = document.body.querySelector('#license-policy-form') as HTMLFormElement
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await flushPromises()
    expect(document.body.textContent).toContain('Enter a name.')
    expect(posts).toHaveLength(0)

    field('lp-name').value = 'x'
    field('lp-name').dispatchEvent(new Event('input'))
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await flushPromises()
    expect(document.body.querySelector('[data-testid="policy-error"]')!.textContent).toContain('name is too long')
    w.unmount()
  })

  it('explains an empty list and offers to create the first policy', async () => {
    serve([])
    const w = await mountPage()
    expect(w.text()).toContain('No license policies yet')
    expect(w.findAll('button').some((b) => b.text() === 'Create policy')).toBe(true)
    w.unmount()
  })
})

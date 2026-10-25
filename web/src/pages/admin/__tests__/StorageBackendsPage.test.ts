// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import StorageBackendsPage from '../StorageBackendsPage.vue'

const backends = [
  {
    id: 'b1', name: 'primary', kind: 's3', is_default: true, status: 'healthy',
    last_checked_at: new Date().toISOString(),
    config: { bucket: 'oa-prod', region: 'eu-west-1', accessKey: 'AKIA1', has_secretKey: true },
  },
  { id: 'b2', name: 'local', kind: 'filesystem', is_default: false, status: 'unknown', config: { path: '/data' } },
]

interface Call { url: string; method: string; body: string | null }

function stubFetch(): Call[] {
  const calls: Call[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ url: String(input), method, body: typeof init?.body === 'string' ? init.body : null })
    if (method === 'GET') return new Response(JSON.stringify({ items: backends, total: 2 }), { status: 200 })
    return new Response(JSON.stringify({}), { status: 200 })
  }) as never)
  return calls
}

const mountPage = () => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  return mount(StorageBackendsPage, { attachTo: document.body, global: { plugins: [router] } })
}

describe('StorageBackendsPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.body.innerHTML = ''
  })

  it('lists backends with type, location, status and per-row action labels', async () => {
    stubFetch()
    const wrapper = mountPage()
    await flushPromises()

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('primary')
    expect(rows[0]!.text()).toContain('oa-prod · eu-west-1')
    expect(rows[0]!.text()).toContain('Healthy')
    expect(rows[0]!.text()).toContain('Checked ')
    expect(rows[1]!.text()).toContain('/data')
    expect(rows[1]!.text()).not.toContain('Checked')
    expect(rows[0]!.find('[aria-label="Test connection: primary"]').exists()).toBe(true)
    expect(rows[0]!.find('[aria-label="Set as default: primary"]').exists()).toBe(false)
    expect(rows[1]!.find('[aria-label="Set as default: local"]').exists()).toBe(true)
    expect(rows[0]!.find('[aria-label="Delete backend: primary"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('opens the edit drawer prefilled and saves without the has_ flags or empty secrets', async () => {
    const calls = stubFetch()
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('[aria-label="Edit: primary"]').trigger('click')
    await flushPromises()

    const body = document.body
    expect(body.textContent).toContain('Edit · primary')
    const name = body.querySelector<HTMLInputElement>('input[disabled]')
    expect(name?.value).toBe('primary')
    const secret = body.querySelector<HTMLInputElement>('input[aria-label="Secret access key"]')
    expect(secret?.type).toBe('password')
    expect(secret?.placeholder).toBe('Stored — leave blank to keep')

    const save = [...body.querySelectorAll('button')].find((b) => b.textContent?.includes('Save changes'))
    save?.click()
    await flushPromises()

    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.url).toBe('/api/v1/admin/storage/primary')
    expect(JSON.parse(patch?.body ?? '{}')).toEqual({
      is_default: true,
      config: { bucket: 'oa-prod', region: 'eu-west-1', accessKey: 'AKIA1' },
    })
    wrapper.unmount()
  })

  it('starts a blank create form on each open', async () => {
    stubFetch()
    const wrapper = mountPage()
    await flushPromises()

    const addButton = () => wrapper.findAll('button').find((b) => b.text().includes('Add storage backend'))
    await addButton()!.trigger('click')
    await flushPromises()
    const nameInput = () => document.body.querySelector<HTMLInputElement>('input[type=text]')
    nameInput()!.value = 'scratch'
    nameInput()!.dispatchEvent(new Event('input'))
    await flushPromises()
    expect(nameInput()!.value).toBe('scratch')

    document.body.querySelector<HTMLButtonElement>('button[aria-label="Close"]')?.click()
    await flushPromises()
    await addButton()!.trigger('click')
    await flushPromises()
    expect(nameInput()!.value).toBe('')
    wrapper.unmount()
  })
})

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
    id: 'b1', name: 'archive', kind: 's3', status: 'healthy',
    last_checked_at: new Date().toISOString(),
    config: { bucket: 'oa-prod', region: 'eu-west-1', accessKey: 'AKIA1', has_secretKey: true },
    usage: { blob_count: 1234, size_bytes: 5 * 1024 ** 3 },
  },
  { id: 'b2', name: 'local', kind: 'filesystem', status: 'unknown', config: { path: '/data' } },
]

const primaryStore = {
  name: 'primary', kind: 's3', source: 'config', status: 'healthy', status_message: null,
  last_checked_at: new Date().toISOString(),
  location: { bucket: 'oa-main', endpoint: 'https://s3.example.com', region: 'eu-west-1' },
  usage: { blob_count: 42, size_bytes: 2 * 1024 ** 3 },
}

interface Call { url: string; method: string; body: string | null }

function stubFetch(primary: Record<string, unknown> | null = null): Call[] {
  const calls: Call[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ url: String(input), method, body: typeof init?.body === 'string' ? init.body : null })
    if (method === 'GET') return new Response(JSON.stringify({ items: backends, total: 2, ...(primary ? { primary } : {}) }), { status: 200 })
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
    expect(rows[0]!.text()).toContain('archive')
    expect(rows[0]!.text()).toContain('oa-prod · eu-west-1')
    expect(rows[0]!.text()).toContain('Healthy')
    expect(rows[0]!.text()).toContain('Checked ')
    expect(rows[1]!.text()).toContain('/data')
    expect(rows[1]!.text()).not.toContain('Checked')
    expect(rows[0]!.find('[aria-label="Test connection: archive"]').exists()).toBe(true)
    expect(rows[0]!.find('[aria-label="Delete backend: archive"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).not.toContain('Set as default')
    wrapper.unmount()
  })

  it('shows what each backend stores, and a dash when the server reports nothing', async () => {
    stubFetch()
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.find('[data-testid="backend-usage-archive"]').text()).toBe('5.0 GBBlobs: 1,234')
    expect(wrapper.find('[data-testid="backend-usage-local"]').text()).toBe('—')
    wrapper.unmount()
  })

  it('opens the edit drawer prefilled and saves without the has_ flags or empty secrets', async () => {
    const calls = stubFetch()
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('[aria-label="Edit: archive"]').trigger('click')
    await flushPromises()

    const body = document.body
    expect(body.textContent).toContain('Edit · archive')
    const name = body.querySelector<HTMLInputElement>('input[disabled]')
    expect(name?.value).toBe('archive')
    const secret = body.querySelector<HTMLInputElement>('input[aria-label="Secret access key"]')
    expect(secret?.type).toBe('password')
    expect(secret?.placeholder).toBe('Stored — leave blank to keep')

    const save = [...body.querySelectorAll('button')].find((b) => b.textContent?.includes('Save changes'))
    save?.click()
    await flushPromises()

    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.url).toBe('/api/v1/admin/storage/archive')
    expect(JSON.parse(patch?.body ?? '{}')).toEqual({
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

  describe('primary store', () => {
    it('renders first with the configuration badge, status, location and usage, and only a Test action', async () => {
      stubFetch(primaryStore)
      const wrapper = mountPage()
      await flushPromises()

      const rows = wrapper.findAll('tbody tr')
      expect(rows).toHaveLength(3)
      const first = rows[0]!
      expect(first.attributes('data-testid')).toBe('storage-primary-row')
      expect(first.text()).toContain('primary')
      expect(first.text()).toContain('configuration')
      expect(first.text()).toContain('oa-main · https://s3.example.com')
      expect(first.text()).toContain('Healthy')
      expect(first.find('[data-testid="backend-usage-primary"]').text()).toBe('2.0 GBBlobs: 42')
      expect(first.findAll('button')).toHaveLength(1)
      expect(first.find('[aria-label="Test connection: primary"]').exists()).toBe(true)
      expect(first.find('[aria-label="Edit: primary"]').exists()).toBe(false)
      expect(first.find('[aria-label="Delete backend: primary"]').exists()).toBe(false)
      expect(rows[1]!.text()).toContain('archive')
      wrapper.unmount()
    })

    it('tests the primary through its own endpoint and refreshes', async () => {
      const calls = stubFetch(primaryStore)
      const wrapper = mountPage()
      await flushPromises()
      const before = calls.filter((c) => c.method === 'GET').length

      await wrapper.find('[aria-label="Test connection: primary"]').trigger('click')
      await flushPromises()

      const post = calls.find((c) => c.method === 'POST')
      expect(post?.url).toBe('/api/v1/admin/storage/primary/test')
      expect(calls.filter((c) => c.method === 'GET').length).toBe(before + 1)
      wrapper.unmount()
    })

    it('shows the status message when the primary is unreachable', async () => {
      stubFetch({ ...primaryStore, status: 'unreachable', status_message: 'dial tcp: connection refused' })
      const wrapper = mountPage()
      await flushPromises()

      const first = wrapper.findAll('tbody tr')[0]!
      expect(first.text()).toContain('Unreachable')
      expect(first.find('[data-testid="storage-primary-message"]').text()).toBe('dial tcp: connection refused')
      wrapper.unmount()
    })

    it('renders no primary row when the server reports none', async () => {
      stubFetch()
      const wrapper = mountPage()
      await flushPromises()

      expect(wrapper.find('[data-testid="storage-primary-row"]').exists()).toBe(false)
      expect(wrapper.findAll('tbody tr')).toHaveLength(2)
      wrapper.unmount()
    })
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import GpgKeysPage from '../GpgKeysPage.vue'

const FP = 'AAAABBBBCCCCDDDDEEEEFFFF0000111122223333'

const keysBody = (canStore: boolean) => ({
  can_store_keys: canStore,
  keys: [
    {
      id: 'k1', name: 'release', fingerprint: FP, key_id: '0000111122223333', algorithm: 'ed25519',
      source: 'generated', read_only: false, active_global: true, repositories: ['apt-main'],
      created_at: '2026-01-01T00:00:00Z', expires_at: null,
    },
    {
      id: 'config', name: 'config-key', fingerprint: 'FFFF', key_id: 'FFFF', algorithm: 'rsa4096',
      source: 'config', read_only: true, active_global: false, repositories: [],
      created_at: null, expires_at: null,
    },
  ],
})

interface Call { url: string; method: string }

function stubFetch(canStore: boolean, deleteResponses: Response[] = []): Call[] {
  const calls: Call[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ url: String(input), method })
    if (method === 'DELETE') return deleteResponses.shift() ?? new Response(null, { status: 204 })
    return new Response(JSON.stringify(keysBody(canStore)), { status: 200 })
  }) as never)
  return calls
}

const mountPage = () => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  return mount(GpgKeysPage, { attachTo: document.body, global: { plugins: [router] } })
}

describe('GpgKeysPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.body.innerHTML = ''
  })

  it('lists keys with scope badges and no delete/activate actions on the config key', async () => {
    stubFetch(true)
    const wrapper = mountPage()
    await flushPromises()

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('release')
    expect(rows[0]!.text()).toContain('Global')
    expect(rows[0]!.text()).toContain('apt-main')
    expect(rows[0]!.text()).toContain(FP)
    expect(rows[1]!.text()).toContain('Read-only')
    expect(rows[1]!.text()).toContain('Export public key')
    expect(rows[1]!.text()).not.toContain('Delete')
    expect(rows[1]!.text()).not.toContain('Activate')
    expect(wrapper.find('[role=status]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the secret-key banner and disables import/generate when keys cannot be stored', async () => {
    stubFetch(false)
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.find('[role=status]').text()).toContain('OPENARTIFACT_SECRET_KEY')
    const [importBtn, generateBtn] = wrapper.findAll('button').slice(0, 2)
    expect(importBtn!.attributes('disabled')).toBeDefined()
    expect(generateBtn!.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('offers delete anyway on 409 key_in_use and retries with force=true', async () => {
    const calls = stubFetch(true, [
      new Response(JSON.stringify({ error: 'in use', code: 'key_in_use' }), { status: 409 }),
    ])
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('button[aria-label="Delete: release"]').trigger('click')
    await flushPromises()
    // the row action is also labelled Delete; the dialog's confirm button is the last one
    const confirm = () => Array.from(document.querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Delete').at(-1)!
    confirm().click()
    await flushPromises()

    expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.url)).toEqual(['/api/v1/admin/gpg/keys/k1'])
    const anyway = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Delete anyway')
    expect(anyway).toBeDefined()
    anyway!.click()
    await flushPromises()

    expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.url)).toEqual([
      '/api/v1/admin/gpg/keys/k1',
      '/api/v1/admin/gpg/keys/k1?force=true',
    ])
    wrapper.unmount()
  })

  it('never renders private key material and clears it when the import dialog closes', async () => {
    stubFetch(true)
    const wrapper = mountPage()
    await flushPromises()

    expect(document.body.innerHTML).not.toContain('PRIVATE KEY')

    await wrapper.findAll('button')[0]!.trigger('click') // Import key
    await flushPromises()
    const area = document.getElementById('gpg-armored') as HTMLTextAreaElement
    area.value = '-----BEGIN PGP PRIVATE KEY BLOCK-----secret'
    area.dispatchEvent(new Event('input'))
    await flushPromises()

    const cancel = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Cancel')!
    cancel.click()
    await flushPromises()
    expect(document.body.innerHTML).not.toContain('PRIVATE KEY')

    await wrapper.findAll('button')[0]!.trigger('click')
    await flushPromises()
    expect((document.getElementById('gpg-armored') as HTMLTextAreaElement).value).toBe('')
    wrapper.unmount()
  })
})

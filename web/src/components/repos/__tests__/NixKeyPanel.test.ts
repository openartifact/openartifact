// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NixKeyPanel from '../NixKeyPanel.vue'
import { useToastStore } from '../../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const KEY = {
  key_name: 'openartifact-r1-1',
  public_key: 'openartifact-r1-1:AbCdEf0123456789AbCdEf0123456789AbCdEf012=',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
}

interface Call {
  method: string
  path: string
  body: Record<string, unknown> | null
}

let fetchSpy: Mock<typeof fetch> | undefined
afterEach(() => fetchSpy?.mockRestore())

/** Stubs fetch for the nix-key endpoint: `get` answers GET, `post` answers POST. */
function serve(get: () => Response, post: () => Response = () => json(KEY)): Call[] {
  const calls: Call[] = []
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = init?.body
    const call: Call = {
      method: init?.method ?? 'GET',
      path: new URL(String(input), 'http://localhost').pathname,
      body: typeof raw === 'string' ? (JSON.parse(raw) as Record<string, unknown>) : null,
    }
    calls.push(call)
    return call.method === 'POST' ? post() : get()
  }) as typeof fetch)
  return calls
}

async function mountPanel(repo = 'r1'): Promise<VueWrapper> {
  setActivePinia(createPinia())
  const wrapper = mount(NixKeyPanel, { props: { repo }, attachTo: document.body, global: { plugins: [createPinia()] } })
  await flushPromises()
  return wrapper
}

describe('NixKeyPanel', () => {
  it('shows the key name and the public key from the nix-key endpoint', async () => {
    const calls = serve(() => json(KEY))
    const wrapper = await mountPanel('my repo')
    expect(calls[0]).toMatchObject({ method: 'GET', path: '/api/v1/repositories/my%20repo/nix-key' })
    expect(wrapper.find('[data-testid="nix-key-name"]').text()).toBe(KEY.key_name)
    expect(wrapper.find('[data-testid="nix-key-public"]').text()).toBe(KEY.public_key)
    wrapper.unmount()
  })

  it('shows the server message and no generate action when the secret key is missing', async () => {
    serve(() => json({ error: 'OPENARTIFACT_SECRET_KEY must be set to store signing keys', code: 'secret_key_required' }, 409))
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="nix-key-secret-required"]').text()).toContain('OPENARTIFACT_SECRET_KEY must be set')
    expect(wrapper.find('[data-testid="nix-key-replace"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('reports another load failure as a plain error', async () => {
    serve(() => json({ error: 'boom', code: 'internal' }, 500))
    const wrapper = await mountPanel()
    expect(wrapper.find('[role="alert"]').text()).toContain('boom')
    expect(wrapper.find('[data-testid="nix-key-secret-required"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('posts an empty body to generate and shows the new key with a success toast', async () => {
    const next = { ...KEY, public_key: 'openartifact-r1-1:ZzZzZzZzZzZzZzZzZzZzZzZzZzZzZzZzZzZzZzZzZzZ=' }
    const calls = serve(() => json(KEY), () => json(next))
    const wrapper = await mountPanel()
    await wrapper.find('[data-testid="nix-key-replace"]').trigger('click')
    await flushPromises()
    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Generate key')!
    confirm.click()
    await flushPromises()
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.path).toBe('/api/v1/repositories/r1/nix-key')
    expect(post.body).toEqual({})
    expect(wrapper.find('[data-testid="nix-key-public"]').text()).toBe(next.public_key)
    expect(useToastStore().queue.some((t) => t.level === 'success')).toBe(true)
    wrapper.unmount()
  })

  it('imports a secret key with a key name', async () => {
    const calls = serve(() => json(KEY))
    const wrapper = await mountPanel()
    await wrapper.find('[data-testid="nix-key-replace"]').trigger('click')
    await flushPromises()
    const name = document.body.querySelector('#nix-key-name-input') as HTMLInputElement
    name.value = 'mine-1'
    name.dispatchEvent(new Event('input'))
    const secret = document.body.querySelector('#nix-key-private') as HTMLTextAreaElement
    secret.value = 'mine-1:c2VjcmV0\n'
    secret.dispatchEvent(new Event('input'))
    await flushPromises()
    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Import key')!
    confirm.click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ private_key: 'mine-1:c2VjcmV0', key_name: 'mine-1' })
    wrapper.unmount()
  })

  it('keeps the dialog open and shows the server error when replacing fails', async () => {
    serve(() => json(KEY), () => json({ error: 'not an ed25519 key', code: 'validation_failed' }, 400))
    const wrapper = await mountPanel()
    await wrapper.find('[data-testid="nix-key-replace"]').trigger('click')
    await flushPromises()
    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Generate key')!
    confirm.click()
    await flushPromises()
    expect(document.body.textContent).toContain('not an ed25519 key')
    expect(wrapper.find('[data-testid="nix-key-public"]').text()).toBe(KEY.public_key)
    wrapper.unmount()
  })
})

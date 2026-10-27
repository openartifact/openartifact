// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ApkKeyPanel from '../ApkKeyPanel.vue'
import { useToastStore } from '../../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const KEY = {
  key_name: 'openartifact-r1.rsa.pub',
  fingerprint: 'ab12cd34',
  public_key: '-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n',
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

/** Stubs fetch for the apk-key endpoint: `get` answers GET, `post` answers POST. */
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
  const wrapper = mount(ApkKeyPanel, { props: { repo }, attachTo: document.body, global: { plugins: [createPinia()] } })
  await flushPromises()
  return wrapper
}

describe('ApkKeyPanel', () => {
  it('shows the key name, fingerprint and PEM from the apk-key endpoint', async () => {
    const calls = serve(() => json(KEY))
    const wrapper = await mountPanel('my repo')
    expect(calls[0]).toMatchObject({ method: 'GET', path: '/api/v1/repositories/my%20repo/apk-key' })
    const details = wrapper.find('[data-testid="apk-key-details"]').text()
    expect(details).toContain('openartifact-r1.rsa.pub')
    expect(details).toContain('ab12cd34')
    expect(wrapper.find('[data-testid="apk-key-pem"]').text()).toBe(KEY.public_key.trim())
    wrapper.unmount()
  })

  it('shows the server message and no generate action when the secret key is missing', async () => {
    serve(() => json({ error: 'OPENARTIFACT_SECRET_KEY must be set to store signing keys', code: 'secret_key_required' }, 409))
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="apk-key-secret-required"]').text()).toContain('OPENARTIFACT_SECRET_KEY must be set')
    expect(wrapper.find('[data-testid="apk-key-replace"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="apk-key-details"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('reports a 404 (repository not found) as a plain error, not as the secret-key notice', async () => {
    serve(() => json({ error: 'repository not found', code: 'not_found' }, 404))
    const wrapper = await mountPanel()
    expect(wrapper.find('[role="alert"]').text()).toContain('repository not found')
    expect(wrapper.find('[data-testid="apk-key-secret-required"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('reports a load failure other than 404', async () => {
    serve(() => json({ error: 'boom', code: 'internal' }, 500))
    const wrapper = await mountPanel()
    expect(wrapper.find('[role="alert"]').text()).toContain('boom')
    wrapper.unmount()
  })

  it('downloads the PEM under the key file name', async () => {
    serve(() => json(KEY))
    const wrapper = await mountPanel()
    let name = ''
    let blob: Blob | undefined
    const create = spyOn(URL, 'createObjectURL').mockImplementation((b) => {
      blob = b as Blob
      return 'blob:x'
    })
    const revoke = spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      name = this.download
    })
    await wrapper.find('[data-testid="apk-key-download"]').trigger('click')
    expect(name).toBe('openartifact-r1.rsa.pub')
    expect(await blob!.text()).toBe(KEY.public_key)
    create.mockRestore()
    revoke.mockRestore()
    click.mockRestore()
    wrapper.unmount()
  })

  it('posts an empty body to generate and shows the new key with a warning toast', async () => {
    const next = { ...KEY, fingerprint: 'ffff0000' }
    const calls = serve(() => json(KEY), () => json(next))
    const wrapper = await mountPanel()
    await wrapper.find('[data-testid="apk-key-replace"]').trigger('click')
    await flushPromises()
    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Generate key')!
    confirm.click()
    await flushPromises()
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.path).toBe('/api/v1/repositories/r1/apk-key')
    expect(post.body).toEqual({})
    expect(wrapper.find('[data-testid="apk-key-details"]').text()).toContain('ffff0000')
    expect(useToastStore().queue.some((t) => t.level === 'success')).toBe(true)
    wrapper.unmount()
  })

  it('imports a private key with a key name', async () => {
    const calls = serve(() => json(KEY))
    const wrapper = await mountPanel()
    await wrapper.find('[data-testid="apk-key-replace"]').trigger('click')
    await flushPromises()
    const name = document.body.querySelector('#apk-key-name') as HTMLInputElement
    name.value = 'mine.rsa.pub'
    name.dispatchEvent(new Event('input'))
    const pem = document.body.querySelector('#apk-key-private') as HTMLTextAreaElement
    pem.value = '-----BEGIN PRIVATE KEY-----\nAA\n-----END PRIVATE KEY-----\n'
    pem.dispatchEvent(new Event('input'))
    await flushPromises()
    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Import key')!
    confirm.click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({
      private_key: '-----BEGIN PRIVATE KEY-----\nAA\n-----END PRIVATE KEY-----',
      key_name: 'mine.rsa.pub',
    })
    wrapper.unmount()
  })

  it('keeps the dialog open and shows the server error when replacing fails', async () => {
    serve(() => json(KEY), () => json({ error: 'not an RSA key', code: 'validation_failed' }, 400))
    const wrapper = await mountPanel()
    await wrapper.find('[data-testid="apk-key-replace"]').trigger('click')
    await flushPromises()
    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Generate key')!
    confirm.click()
    await flushPromises()
    expect(document.body.textContent).toContain('not an RSA key')
    expect(wrapper.find('[data-testid="apk-key-details"]').text()).toContain('ab12cd34')
    wrapper.unmount()
  })
})

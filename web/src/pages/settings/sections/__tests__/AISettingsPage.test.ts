// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import AISettingsPage from '../AISettingsPage.vue'
import { useToastStore } from '../../../../stores/toast'
import { t } from '../../../../i18n'

// The server never returns the stored key: GET answers this placeholder when one is stored.
const MASK = '••••••••'
const REAL_KEY = 'sk-live-0123456789abcdef'

interface Call {
  method: string
  url: string
  body: Record<string, unknown>
}

interface Server {
  calls: Call[]
  config: Record<string, unknown>
  putError?: { status: number; error: string; code?: string }
  testResponse: { status: number; body: unknown }
  getError?: { status: number; error: string; code?: string }
}

const STORED = {
  enabled: true,
  auto_analyze: false,
  provider: 'ollama',
  base_url: 'http://ollama.internal:11434/v1',
  api_key: MASK,
  has_api_key: true,
  model: 'llama3.2',
  max_tokens: 4096,
  timeout_secs: 60,
  system_prompt: 'Be terse.',
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

function startServer(config: Record<string, unknown> = STORED): Server {
  const server: Server = { calls: [], config, testResponse: { status: 200, body: { ok: true, models: [] } } }
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'GET' && url === '/api/v1/admin/ai/config') {
      return server.getError ? json({ error: server.getError.error, code: server.getError.code }, server.getError.status) : json(server.config)
    }
    server.calls.push({ method, url, body: JSON.parse(String(init?.body ?? '{}')) })
    if (method === 'PUT') return server.putError ? json({ error: server.putError.error, code: server.putError.code }, server.putError.status) : json(server.config)
    if (method === 'POST' && url === '/api/v1/admin/ai/test') return json(server.testResponse.body, server.testResponse.status)
    return json({ error: `unexpected ${method} ${url}` }, 500)
  }) as never)
  return server
}

async function mountPage() {
  const pinia = createPinia()
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/ai')
  const wrapper = mount(AISettingsPage, { attachTo: document.body, global: { plugins: [pinia, router] } })
  await flushPromises()
  return Object.assign(wrapper, { toasts: useToastStore(pinia) })
}

const input = (id: string) => document.getElementById(id) as HTMLInputElement
const toggles = () => Array.from(document.querySelectorAll('[role=switch]')) as HTMLButtonElement[]
const actionButton = (label: string) => Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.includes(label)) as HTMLButtonElement

async function typeInto(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('input'))
  await flushPromises()
}

async function click(el: HTMLElement) {
  el.click()
  await flushPromises()
}

describe('AISettingsPage load', () => {
  it('shows the stored configuration with the API key masked and never in the DOM', async () => {
    startServer({ ...STORED, api_key: MASK })
    const wrapper = await mountPage()

    expect(input('ai-base-url').value).toBe('http://ollama.internal:11434/v1')
    expect(input('ai-model').value).toBe('llama3.2')
    expect(input('ai-max-tokens').value).toBe('4096')
    expect(input('ai-timeout').value).toBe('60')
    expect((document.getElementById('ai-system-prompt') as HTMLTextAreaElement).value).toBe('Be terse.')
    const key = input('ai-api-key')
    expect(key.type).toBe('password')
    expect(key.value).toBe(MASK)
    expect(document.body.innerHTML).not.toContain(REAL_KEY)
    wrapper.unmount()
  })

  it('reflects the disabled state and falls back to provider defaults for blank fields', async () => {
    startServer({ enabled: false, auto_analyze: false, provider: '', base_url: '', api_key: '', model: '', max_tokens: 0, timeout_secs: 0, system_prompt: '' })
    const wrapper = await mountPage()

    const [enable, auto] = toggles()
    expect(enable!.getAttribute('aria-checked')).toBe('false')
    expect(auto!.getAttribute('aria-checked')).toBe('false')
    expect(wrapper.find('[aria-disabled=true]').exists()).toBe(true)
    // Blank provider falls back to OpenAI with its default endpoint and sane limits.
    expect(input('ai-base-url').value).toBe('https://api.openai.com/v1')
    expect(input('ai-max-tokens').value).toBe('2048')
    expect(input('ai-timeout').value).toBe('30')
    expect(input('ai-api-key').value).toBe('')
    wrapper.unmount()
  })

  it('treats an enabled configuration as active', async () => {
    startServer()
    const wrapper = await mountPage()

    expect(toggles()[0]!.getAttribute('aria-checked')).toBe('true')
    expect(wrapper.find('[aria-disabled=false]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('shows the load error instead of a form, and does not offer to save over unknown settings', async () => {
    const server = startServer()
    server.getError = { status: 403, error: 'admin access required' }
    const wrapper = await mountPage()

    expect(wrapper.find('[role=alert]').text()).toBe('admin access required')
    expect(document.getElementById('ai-base-url')).toBeNull()
    expect(wrapper.toasts.visible.some((x) => x.level === 'error')).toBe(true)
    // Saving the untouched defaults would disable AI and wipe the stored key.
    expect(actionButton('Save').disabled).toBe(true)
    expect(actionButton('Test').disabled).toBe(true)
    await click(actionButton('Save'))
    expect(server.calls).toEqual([])
    wrapper.unmount()
  })

  it('says the build has no AI analysis (501 feature_unavailable): no form, no error toast, nothing to save', async () => {
    const server = startServer()
    server.getError = { status: 501, error: 'AI analysis is not compiled into this build (built with -tags noai)', code: 'feature_unavailable' }
    const wrapper = await mountPage()

    expect(wrapper.get('[data-testid="ai-unavailable"]').text()).toContain('not part of this build')
    expect(wrapper.find('[role=alert]').exists()).toBe(false)
    expect(document.getElementById('ai-base-url')).toBeNull()
    expect(wrapper.toasts.visible.some((x) => x.level === 'error')).toBe(false)
    expect(actionButton('Save').disabled).toBe(true)
    expect(actionButton('Test').disabled).toBe(true)
    wrapper.unmount()
  })
})

describe('AISettingsPage save', () => {
  it('sends the mask back untouched so the stored key is kept, plus edits to other fields', async () => {
    const server = startServer()
    const wrapper = await mountPage()

    await typeInto(input('ai-model'), 'mistral')
    await typeInto(input('ai-max-tokens'), '8192')
    await click(actionButton('Save'))

    expect(server.calls).toHaveLength(1)
    const [put] = server.calls
    expect(put!.method).toBe('PUT')
    expect(put!.url).toBe('/api/v1/admin/ai/config')
    expect(put!.body).toMatchObject({ enabled: true, provider: 'ollama', model: 'mistral', max_tokens: 8192, timeout_secs: 60, api_key: MASK })
    expect(wrapper.toasts.visible.some((x) => x.level === 'success')).toBe(true)
    wrapper.unmount()
  })

  it('sends a newly typed key and keeps the field masked after saving', async () => {
    const server = startServer()
    const wrapper = await mountPage()

    await typeInto(input('ai-api-key'), REAL_KEY)
    await click(actionButton('Save'))

    expect(server.calls[0]!.body.api_key).toBe(REAL_KEY)
    expect(input('ai-api-key').type).toBe('password')
    expect(document.body.textContent).not.toContain(REAL_KEY)
    wrapper.unmount()
  })

  it('keeps the key hidden until revealed and re-hides it on the second click', async () => {
    startServer()
    const wrapper = await mountPage()

    const reveal = () => document.querySelector('#ai-api-key + button') as HTMLButtonElement
    expect(input('ai-api-key').type).toBe('password')
    await click(reveal())
    expect(input('ai-api-key').type).toBe('text')
    await click(reveal())
    expect(input('ai-api-key').type).toBe('password')
    wrapper.unmount()
  })

  it('saves a configuration with no key as an empty key rather than the mask', async () => {
    const server = startServer({ ...STORED, api_key: '', has_api_key: false })
    const wrapper = await mountPage()

    await click(actionButton('Save'))

    expect(server.calls[0]!.body.api_key).toBe('')
    wrapper.unmount()
  })

  it('saves the enable and auto-analyze toggles', async () => {
    const server = startServer()
    const wrapper = await mountPage()

    const [enable, auto] = toggles()
    await click(enable!)
    await click(auto!)
    await click(actionButton('Save'))

    expect(server.calls[0]!.body).toMatchObject({ enabled: false, auto_analyze: true })
    wrapper.unmount()
  })

  it('reports a server rejection (e.g. a disallowed base URL) and does not claim success', async () => {
    const server = startServer()
    server.putError = { status: 400, error: 'base_url is not allowed: private address' }
    const wrapper = await mountPage()

    await typeInto(input('ai-base-url'), 'http://169.254.169.254/')
    await click(actionButton('Save'))

    expect(server.calls[0]!.body.base_url).toBe('http://169.254.169.254/')
    expect(wrapper.toasts.visible.find((x) => x.level === 'error')?.title).toBe('base_url is not allowed: private address')
    expect(wrapper.toasts.visible.some((x) => x.level === 'success')).toBe(false)
    // The edited value stays so the admin can fix it.
    expect(input('ai-base-url').value).toBe('http://169.254.169.254/')
    wrapper.unmount()
  })

  it('tells the admin to enter the key again when the endpoint changed under the stored key (400 api_key_required)', async () => {
    const server = startServer()
    server.putError = { status: 400, error: 'api_key is required when the provider or base_url changes', code: 'api_key_required' }
    const wrapper = await mountPage()

    await typeInto(input('ai-base-url'), 'http://other.example/v1')
    await click(actionButton('Save'))

    expect(wrapper.toasts.visible.find((x) => x.level === 'error')?.title).toBe(t('adm.ai.apiKeyRequired'))
    wrapper.unmount()
  })
})

describe('AISettingsPage provider switch', () => {
  const pick = (label: string) =>
    Array.from(document.querySelectorAll('[role=radio]')).find((b) => b.textContent?.includes(label)) as HTMLButtonElement

  it('replaces the endpoint only while it is still the previous provider default', async () => {
    startServer({ ...STORED, provider: 'openai', base_url: 'https://api.openai.com/v1' })
    const wrapper = await mountPage()

    await click(pick('Ollama'))
    expect(input('ai-base-url').value).toBe('http://localhost:11434/v1')
    expect(pick('Ollama').getAttribute('aria-checked')).toBe('true')

    await typeInto(input('ai-base-url'), 'https://gateway.example.com/v1')
    await click(pick('OpenAI'))
    expect(input('ai-base-url').value).toBe('https://gateway.example.com/v1')
    wrapper.unmount()
  })

  it('saves the chosen provider', async () => {
    const server = startServer({ ...STORED, provider: 'openai', base_url: 'https://api.openai.com/v1' })
    const wrapper = await mountPage()

    await click(pick('Azure'))
    await click(actionButton('Save'))

    expect(server.calls[0]!.body.provider).toBe('azure')
    wrapper.unmount()
  })
})

describe('AISettingsPage connection test', () => {
  it('posts the form (with the mask, never a real key) and lists the discovered models', async () => {
    const server = startServer()
    server.testResponse = { status: 200, body: { ok: true, models: ['llama3.2', 'qwen2.5'] } }
    const wrapper = await mountPage()

    await click(actionButton('Test'))

    expect(server.calls).toHaveLength(1)
    expect(server.calls[0]!.url).toBe('/api/v1/admin/ai/test')
    expect(server.calls[0]!.body).toMatchObject({ provider: 'ollama', base_url: STORED.base_url, api_key: MASK })
    const chips = Array.from(document.querySelectorAll('[role=status] button[title]')).map((b) => b.textContent?.trim())
    expect(chips).toEqual(['llama3.2', 'qwen2.5'])
    wrapper.unmount()
  })

  it('picking a discovered model fills the model field', async () => {
    const server = startServer()
    server.testResponse = { status: 200, body: { ok: true, models: ['llama3.2', 'qwen2.5'] } }
    const wrapper = await mountPage()

    await click(actionButton('Test'))
    const chip = Array.from(document.querySelectorAll('[role=status] button[title]')).find((b) => b.textContent?.trim() === 'qwen2.5') as HTMLButtonElement
    await click(chip)

    expect(input('ai-model').value).toBe('qwen2.5')
    wrapper.unmount()
  })

  it('shows the provider error of a failed test and offers no models', async () => {
    const server = startServer()
    server.testResponse = { status: 200, body: { ok: false, error: '401 invalid api key' } }
    const wrapper = await mountPage()

    await click(actionButton('Test'))

    const panel = document.querySelector('[role=status]') as HTMLElement
    expect(panel.textContent).toContain('401 invalid api key')
    expect(panel.querySelectorAll('button[title]')).toHaveLength(0)
    wrapper.unmount()
  })

  it('turns an HTTP failure of the test call into a failed result and clears the previous one', async () => {
    const server = startServer()
    server.testResponse = { status: 200, body: { ok: true, models: ['llama3.2'] } }
    const wrapper = await mountPage()
    await click(actionButton('Test'))
    expect(document.querySelectorAll('[role=status] button[title]')).toHaveLength(1)

    server.testResponse = { status: 502, body: { error: 'upstream unreachable' } }
    await click(actionButton('Test'))

    const panel = document.querySelector('[role=status]') as HTMLElement
    expect(panel.textContent).toContain('upstream unreachable')
    expect(panel.querySelectorAll('button[title]')).toHaveLength(0)
    wrapper.unmount()
  })

  it('Load fills model suggestions from the server and reports an empty list', async () => {
    const server = startServer()
    server.testResponse = { status: 200, body: { ok: true, models: ['m-a', 'm-b', 'm-c'] } }
    const wrapper = await mountPage()

    await click(actionButton('Load'))
    const options = Array.from(document.querySelectorAll('#model-suggestions option')).map((o) => (o as HTMLOptionElement).value)
    expect(options).toEqual(expect.arrayContaining(['m-a', 'm-b', 'm-c']))
    expect(wrapper.toasts.visible.some((x) => x.level === 'success')).toBe(true)

    server.testResponse = { status: 200, body: { ok: true, models: [] } }
    await click(actionButton('Load'))
    expect(wrapper.toasts.visible.some((x) => x.level === 'info')).toBe(true)
    wrapper.unmount()
  })

  it('Load surfaces the provider error without touching the suggestions', async () => {
    const server = startServer()
    server.testResponse = { status: 200, body: { ok: false, error: 'connection refused' } }
    const wrapper = await mountPage()

    await click(actionButton('Load'))

    expect(wrapper.toasts.visible.find((x) => x.level === 'error')?.title).toBe('connection refused')
    expect(Array.from(document.querySelectorAll('#model-suggestions option')).map((o) => (o as HTMLOptionElement).value)).not.toContain('connection refused')
    wrapper.unmount()
  })
})

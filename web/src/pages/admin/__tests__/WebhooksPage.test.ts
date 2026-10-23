// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import WebhooksPage from '../WebhooksPage.vue'
import { WEBHOOK_EVENTS, WEBHOOK_PAYLOAD_FORMATS } from '../../../lib/webhookEvents'

const LIST = {
  webhooks: [
    { id: 'w1', name: 'chat', url: 'https://hooks.example.com/x', events: ['artifact.uploaded'], enabled: true, payload_format: 'slack', created_at: '2026-01-01T00:00:00Z' },
    { id: 'w2', name: 'legacy', url: 'https://hooks.example.com/y', events: ['repository.created'], enabled: true, created_at: '2026-01-01T00:00:00Z' },
  ],
}

const mountPage = () => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  return mount(WebhooksPage, { attachTo: document.body, global: { plugins: [router] } })
}

describe('WebhooksPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.body.innerHTML = ''
  })

  it('never offers artifact.downloaded, which the server does not emit', () => {
    expect(WEBHOOK_EVENTS as readonly string[]).not.toContain('artifact.downloaded')
  })

  it('lists the payload format of each webhook, defaulting to OpenArtifact', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () => new Response(JSON.stringify(LIST), { status: 200 })) as never)

    const wrapper = mountPage()
    await flushPromises()

    const rows = wrapper.findAll('tbody tr')
    expect(rows[0]!.text()).toContain('Slack')
    expect(rows[1]!.text()).toContain('OpenArtifact (JSON)')
    wrapper.unmount()
  })

  it('creates a webhook with the chosen payload format', async () => {
    const calls: { url: string; body: unknown }[] = []
    spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') {
        calls.push({ url: String(input), body: JSON.parse(String(init.body)) })
        return new Response(JSON.stringify({ id: 'w3' }), { status: 201 })
      }
      return new Response(JSON.stringify({ webhooks: [] }), { status: 200 })
    }) as never)

    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('button').trigger('click') // header "Add webhook"
    await flushPromises()

    const byId = (id: string) => document.getElementById(id) as HTMLInputElement | HTMLSelectElement
    const set = async (id: string, value: string) => {
      const el = byId(id)
      el.value = value
      el.dispatchEvent(new Event('input'))
      el.dispatchEvent(new Event('change'))
      await flushPromises()
    }

    const select = byId('wh-format') as HTMLSelectElement
    expect(Array.from(select.options).map((o) => o.value)).toEqual([...WEBHOOK_PAYLOAD_FORMATS])
    expect(select.value).toBe('openartifact')

    await set('wh-name', 'ce')
    await set('wh-url', 'https://hooks.example.com/ce')
    await set('wh-format', 'cloudevents')
    ;(document.querySelector('fieldset input[type=checkbox]') as HTMLInputElement).click()
    await flushPromises()
    ;(document.getElementById('webhook-form') as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }))
    await flushPromises()

    expect(calls).toHaveLength(1)
    expect(calls[0]!.url).toBe('/api/v1/webhooks')
    expect(calls[0]!.body).toMatchObject({
      name: 'ce',
      url: 'https://hooks.example.com/ce',
      payload_format: 'cloudevents',
      events: ['artifact.uploaded'],
    })
    wrapper.unmount()
  })
})

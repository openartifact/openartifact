// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import TierMoverStatus from '../../../components/TierMoverStatus.vue'
import StorageTiersPage from '../StorageTiersPage.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

const tier = (name: string) => ({ name, backend: 'b', rank: 0, promote_on_read: false })

describe('StorageTiersPage', () => {
  it('keeps the newest reload when an older one resolves last, and Refresh re-reads the mover', async () => {
    const tierCalls: Array<(r: Response) => void> = []
    let statusCalls = 0
    spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: string) => {
      const path = new URL(String(input), 'http://localhost').pathname
      if (path.endsWith('/tiers')) return new Promise<Response>((resolve) => tierCalls.push(resolve))
      if (path.endsWith('/status')) {
        statusCalls++
        return json({ enabled: true, running: false, interval_seconds: 300, pending_total: 0, pending: [] })
      }
      return json({ items: [] })
    }) as never)

    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div />' } }] })
    await router.push('/')
    const wrapper = mount(StorageTiersPage, { global: { plugins: [createPinia(), router] } })
    await flushPromises()
    tierCalls[0]!(json({ items: [tier('initial')] }))
    await flushPromises()
    expect(statusCalls).toBe(1)

    const refresh = wrapper.findAll('button').find((b) => b.text().includes('Refresh'))!
    await refresh.trigger('click') // reload A
    await flushPromises()
    wrapper.findComponent(TierMoverStatus).vm.$emit('pass-finished') // reload B (newer)
    await flushPromises()
    expect(statusCalls).toBeGreaterThanOrEqual(2)

    tierCalls[2]!(json({ items: [tier('newest')] }))
    await flushPromises()
    tierCalls[1]!(json({ items: [tier('stale')] }))
    await flushPromises()

    expect(wrapper.text()).toContain('newest')
    expect(wrapper.text()).not.toContain('stale')
  })
})

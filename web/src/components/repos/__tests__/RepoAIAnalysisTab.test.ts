// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import RepoAIAnalysisTab from '../RepoAIAnalysisTab.vue'
import Pagination from '../../Pagination.vue'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

function analysis(id: string, status: string) {
  return {
    id,
    artifact_id: `art-${id}`,
    artifact_name: `pkg-${id}`,
    artifact_version: '1.0.0',
    status,
    license_status: 'UNKNOWN',
    tags: [],
    updated_at: new Date().toISOString(),
  }
}

const mountTab = () => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  return mount(RepoAIAnalysisTab, { props: { repo: 'libs' }, global: { plugins: [router] } })
}

let spy: Mock<typeof fetch> | undefined

afterEach(() => {
  spy?.mockRestore()
  spy = undefined
})

describe('RepoAIAnalysisTab', () => {
  it('starts a status filter on the first page', async () => {
    const urls: string[] = []
    spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
      urls.push(String(input))
      return json({ analyses: [analysis('a', 'completed')], total: 45 })
    }) as never)
    const wrapper = mountTab()
    await flushPromises()

    wrapper.findComponent(Pagination).vm.$emit('change', 2)
    await flushPromises()
    expect(new URL(urls.at(-1)!, 'http://x').searchParams.get('offset')).toBe('20')

    await wrapper.find('[data-status="failed"]').trigger('click')
    await flushPromises()
    const last = new URL(urls.at(-1)!, 'http://x').searchParams
    expect(last.get('status')).toBe('failed')
    expect(last.get('offset')).toBe('0')
    expect(wrapper.find('[data-status="failed"]').attributes('aria-pressed')).toBe('true')
    wrapper.unmount()
  })

  it('shows the answer of the latest filter when an earlier request answers last', async () => {
    const pending: Array<{ status: string | null; resolve: (r: Response) => void }> = []
    spy = spyOn(globalThis, 'fetch').mockImplementation(((input: RequestInfo | URL) => {
      const { promise, resolve } = Promise.withResolvers<Response>()
      pending.push({ status: new URL(String(input), 'http://x').searchParams.get('status'), resolve })
      return promise
    }) as never)
    const wrapper = mountTab()
    await flushPromises()

    await wrapper.find('[data-status="failed"]').trigger('click')
    await flushPromises()
    expect(pending.map((p) => p.status)).toEqual([null, 'failed'])

    pending[1]!.resolve(json({ analyses: [analysis('f', 'failed')], total: 1 }))
    await flushPromises()
    pending[0]!.resolve(json({ analyses: [analysis('a', 'completed'), analysis('b', 'completed')], total: 2 }))
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="ai-row"]')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('pkg-f')
    wrapper.unmount()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import PackagesPage from '../PackagesPage.vue'

const json = (body: unknown): Response => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

const pkg = (name: string, version: string, repository: string) => ({ repository, format: 'npm', name, version, created_at: '2026-01-01T00:00:00Z' })

async function mountPage(recent: unknown[]) {
  setActivePinia(createPinia())
  spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
    const url = String(input)
    if (url.includes('/packages/recent')) return json({ packages: recent })
    if (url.includes('/repositories')) return json({ repositories: [], total: 0 })
    return json({})
  }) as never)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: PackagesPage }] })
  await router.push('/packages')
  await router.isReady()
  const w = mount(PackagesPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

describe('PackagesPage start screen', () => {
  it('offers names to try from the latest publications, one per package', async () => {
    const w = await mountPage([pkg('ui-kit', '2.0.0', 'web'), pkg('ui-kit', '1.0.0', 'web'), pkg('left-pad', '1.0.0', 'libs')])
    const examples = w.findAll('[data-testid="search-example"]')
    expect(examples.map((e) => e.find('code').text())).toEqual(['ui-kit', 'left-pad'])
    expect(w.text()).not.toContain('Nothing here yet')
    w.unmount()
  })

  it('teaches how to publish when nothing was published yet', async () => {
    const w = await mountPage([])
    expect(w.findAll('[data-testid="search-example"]')).toHaveLength(0)
    expect(w.text()).toContain('Nothing here yet')
    w.unmount()
  })
})

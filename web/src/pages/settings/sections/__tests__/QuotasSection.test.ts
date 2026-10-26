// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import QuotasSection from '../QuotasSection.vue'

const GB = 1024 ** 3

const QUOTAS = [
  { scope: 'repository', name: 'calm', limit_bytes: 10 * GB, used_bytes: GB, usage_percent: 10, exceeded: false },
  { scope: 'repository', name: 'full', limit_bytes: 10 * GB, used_bytes: 11 * GB, usage_percent: 110, exceeded: true },
  { scope: 'project', name: 'team', limit_bytes: 100 * GB, used_bytes: 85 * GB, usage_percent: 85, exceeded: false, artifact_count: 40, max_artifact_count: 1000 },
  { scope: 'repository', name: 'strict', project: 'team', limit_bytes: 10 * GB, used_bytes: 6 * GB, usage_percent: 60, warn_threshold_percent: 50, exceeded: false },
]

let spy: Mock<typeof fetch> | undefined
let urls: URL[] = []

function serve(quotas: unknown[], total = quotas.length): void {
  urls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    urls.push(new URL(String(input), 'http://localhost'))
    return new Response(JSON.stringify({ quotas, total, page: 1, limit: 2000 }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as never)
}

async function mountPage(path = '/settings/quotas') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  const w = mount(QuotasSection, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return Object.assign(w, { router })
}

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('QuotasSection', () => {
  it('asks the server for limited quotas once and lists the most used first', async () => {
    serve(QUOTAS)
    const w = await mountPage()
    expect(urls).toHaveLength(1)
    expect(urls[0].pathname).toBe('/api/v1/admin/quotas')
    expect(urls[0].searchParams.get('limited')).toBe('true')
    const names = w.findAll('tbody tr').map((r) => r.find('td span.font-mono').text())
    expect(names).toEqual(['full', 'team', 'strict', 'calm'])
    w.unmount()
  })

  it('says over, near and within limit in words, honouring the entry warn threshold', async () => {
    serve(QUOTAS)
    const w = await mountPage()
    const state = (name: string) => w.findAll('tbody tr').find((r) => r.find('td').text().startsWith(name))!.text()
    expect(state('full')).toContain('Over limit')
    expect(state('team')).toContain('Near limit')
    expect(state('strict')).toContain('Near limit')
    expect(state('calm')).toContain('Within limit')
    expect(w.find('[data-testid="quota-summary"]').text()).toBe('1 over limit, 2 near limit, 1 within limit.')
    w.unmount()
  })

  it('filters to near or over limit and by scope', async () => {
    serve(QUOTAS)
    const w = await mountPage()
    await w.findAll('[data-testid="scope-filter"] button').find((b) => b.text() === 'Near or over limit')!.trigger('click')
    await flushPromises()
    expect(w.findAll('tbody tr')).toHaveLength(3)

    await w.findAll('[data-testid="scope-filter"] button').find((b) => b.text() === 'Projects')!.trigger('click')
    await flushPromises()
    expect(w.router.currentRoute.value.query.scope).toBe('project')
    expect(w.findAll('tbody tr')).toHaveLength(1)
    expect(urls).toHaveLength(1)
    w.unmount()
  })

  it('links a repository to its configuration and a project to its usage tab', async () => {
    serve(QUOTAS)
    const w = await mountPage()
    const hrefs = w.findAll('tbody a').map((a) => a.attributes('href'))
    expect(hrefs).toContain('/repos/full?tab=configuration')
    expect(hrefs).toContain('/projects/team?tab=usage')
    w.unmount()
  })

  it('tells when there are more quotas than were loaded', async () => {
    serve(QUOTAS, 2500)
    const w = await mountPage()
    expect(w.find('[data-testid="quota-truncated"]').text()).toContain('first 4 of 2500')
    w.unmount()
  })

  it('explains an empty list', async () => {
    serve([])
    const w = await mountPage()
    expect(w.text()).toContain('No quotas set')
    w.unmount()
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import FederationSection from '../FederationSection.vue'

const REPOS = {
  repositories: [
    { id: '1', name: 'npm-a', format: 'npm', repo_type: 'hosted', federated: true },
    { id: '2', name: 'raw-b', format: 'raw', repo_type: 'hosted', federated: true },
    { id: '3', name: 'npm-c', format: 'npm', repo_type: 'hosted', federated: false },
  ],
  total: 3,
}

const STATUS: Record<string, unknown> = {
  'npm-a': { server_enabled: true, instance_id: 'inst-1', head_seq: 12, peers: [{ id: 'p1', name: 'eu', state: 'ok' }, { id: 'p2', name: 'us', state: 'error' }] },
  'raw-b': { server_enabled: true, instance_id: 'inst-1', head_seq: 3, peers: [] },
}

let spy: Mock<typeof fetch> | undefined

function serve(opts: { repos?: unknown; serverEnabled?: boolean; failRepo?: string } = {}): void {
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost')
    const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
    if (url.pathname === '/api/v1/repositories') return json(opts.repos ?? REPOS)
    const repo = url.pathname.split('/')[4]
    if (repo === opts.failRepo) return json({ error: 'boom' }, 500)
    if (opts.serverEnabled === false) return json({ server_enabled: false, instance_id: 'inst-1', peers: [] })
    return json(STATUS[repo])
  }) as never)
}

async function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/federation')
  const w = mount(FederationSection, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('FederationSection', () => {
  it('lists federated repositories with how their peers are doing, in words', async () => {
    serve()
    const w = await mountPage()
    expect(w.find('[data-testid="federation-on"]').text()).toContain('inst-1')
    expect(w.find('[data-testid="federation-summary"]').text()).toBe('Federated repositories: 2. Peers: 2.')
    const rows = w.findAll('tbody tr').map((r) => r.text())
    expect(rows).toHaveLength(2)
    expect(rows[0]).toContain('npm-a')
        expect(rows[0]).toContain('Needs attention: 1')
    expect(rows[1]).toContain('No peers')
    w.unmount()
  })

  it('links each repository to its Federation tab and to the inbound tokens', async () => {
    serve()
    const w = await mountPage()
    expect(w.find('tbody a').attributes('href')).toBe('/repos/npm-a?tab=federation')
    expect(w.find('[data-testid="federation-tokens"] a').attributes('href')).toBe('/settings/tokens?type=federation')
    w.unmount()
  })

  it('says federation is off and how to turn it on when the server has it disabled', async () => {
    serve({ serverEnabled: false })
    const w = await mountPage()
    const off = w.find('[data-testid="federation-off"]')
    expect(off.text()).toContain('Federation is off for this instance')
    expect(off.text()).toContain('federation.enabled: true')
    expect(w.find('table').exists()).toBe(false)
    w.unmount()
  })

  it('says the server state is unknown while no repository can federate', async () => {
    serve({ repos: { repositories: [{ id: '9', name: 'debs', format: 'deb', repo_type: 'hosted' }], total: 1 } })
    const w = await mountPage()
    expect(w.find('[data-testid="federation-unknown"]').exists()).toBe(true)
    w.unmount()
  })

  it('names the repositories whose status could not be read', async () => {
    serve({ failRepo: 'raw-b' })
    const w = await mountPage()
    expect(w.find('[data-testid="federation-partial"]').text()).toContain('raw-b')
    expect(w.findAll('tbody tr')).toHaveLength(1)
    w.unmount()
  })
})

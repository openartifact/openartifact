// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, h } from 'vue'
import ArtifactVersionsTab from '../artifact/ArtifactVersionsTab.vue'
import { useArtifact } from '../../composables/useArtifact'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const PACKAGES = [
  { id: 'f-1', name: 'lib', version: '1.0.0', format: 'npm', filename: 'lib-1.0.0.tgz', created_at: '2026-01-01T00:00:00Z' },
  { id: 'f-2', name: 'lib', version: '1.1.0', format: 'npm', filename: 'lib-1.1.0.tgz', created_at: '2026-02-01T00:00:00Z' },
]

let repoType: string
let held: { supported: boolean; versions: unknown[] }
let fetchSpy: Mock<typeof fetch>

beforeEach(() => {
  repoType = 'proxy'
  held = { supported: true, versions: [] }
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input)
    if (url === '/api/v1/repository/px') return json({ name: 'px', format: 'npm', repo_type: repoType })
    if (url.startsWith('/api/v1/repositories/px/packages?')) return json({ packages: PACKAGES, total: PACKAGES.length })
    if (url.startsWith('/api/v1/repositories/px/held-back-versions?')) return json(held)
    return json({})
  })
})
afterEach(() => fetchSpy.mockRestore())

async function mountTab(): Promise<VueWrapper> {
  setActivePinia(createPinia())
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div/>' } }] })
  await router.push('/')
  await router.isReady()
  const Host = defineComponent({
    setup() {
      useArtifact({ repo: () => 'px', pkg: () => 'lib', version: () => undefined })
      return () => h(ArtifactVersionsTab)
    },
  })
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return w
}

const heldUrls = (): string[] => fetchSpy.mock.calls.map((c) => String(c[0])).filter((u) => u.includes('held-back-versions'))
const inHours = (h: number): string => new Date(Date.now() + h * 3_600_000 - 1000).toISOString()

describe('ArtifactVersionsTab with held-back versions', () => {
  it('shows held-back versions greyed, with when they become available or why they are blocked', async () => {
    held = {
      supported: true,
      versions: [
        { version: '2.0.0', published_at: '2026-10-31T08:00:00Z', reasons: [{ source: 'min_age', detail: 'young', until: inHours(5) }] },
        { version: '1.5.0', reasons: [{ source: 'curation', detail: 'blocked by repository curation: malware', rule_id: 'r1' }] },
      ],
    }
    const w = await mountTab()
    expect(heldUrls()).toEqual(['/api/v1/repositories/px/held-back-versions?package=lib'])
    const greyed = w.findAll('[data-testid="held-version"]')
    expect(greyed.map((g) => g.text())).toEqual(['2.0.0', '1.5.0'])
    // Not served, so not a link to a page that would be empty.
    expect(w.findAll('a').map((a) => a.text())).not.toContain('2.0.0')
    expect(w.findAll('[data-testid="held-status"]').map((s) => s.text())).toEqual(['Available in 5h', 'Blocked: malware'])
    expect(w.text()).toContain('1.1.0')
  })

  it('adds nothing for a repository that holds nothing back, nor asks a hosted one', async () => {
    const w = await mountTab()
    expect(w.find('[data-testid="held-version"]').exists()).toBe(false)
    expect(w.text()).not.toContain('Availability')
    fetchSpy.mockClear()
    repoType = 'hosted'
    await mountTab()
    expect(heldUrls()).toEqual([])
  })

  it('marks a stored version the gate holds back too, without listing it twice', async () => {
    held = { supported: true, versions: [{ version: '1.1.0', reasons: [{ source: 'curation', detail: 'blocked by repository curation', rule_id: 'r1' }] }] }
    const w = await mountTab()
    expect(w.findAll('[data-testid="held-version"]')).toHaveLength(0)
    expect(w.findAll('[data-testid="held-status"]')).toHaveLength(1)
    expect(w.findAll('tbody tr')).toHaveLength(2)
  })
})

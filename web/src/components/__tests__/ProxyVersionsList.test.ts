// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ProxyVersionsList from '../artifact/ProxyVersionsList.vue'
import type { ProxyVersion } from '../../api/curation'

const available = (version: string, extra: Partial<ProxyVersion> = {}): ProxyVersion => ({ version, status: 'available', cached: false, reasons: [], ...extra })

let fetchSpy: Mock<typeof fetch>
beforeEach(() => {
  // The list reads what the page loaded; any request would be a bug.
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async () => new Response('{}', { status: 404 }))
})
afterEach(() => fetchSpy.mockRestore())

async function mountList(versions: ProxyVersion[], format = 'npm', url = '/'): Promise<VueWrapper> {
  setActivePinia(createPinia())
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div/>' } }] })
  await router.push(url)
  await router.isReady()
  const w = mount(ProxyVersionsList, { props: { format, repo: 'px', pkg: 'lib', versions }, global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return w
}

const rowsOf = (w: VueWrapper): string[] => w.findAll('[data-testid="proxy-version"]').map((r) => r.text())

describe('ProxyVersionsList', () => {
  it('badges each version Available, Cached or Held and says when a held one opens', async () => {
    const until = new Date(Date.now() + 5 * 3_600_000 - 1000).toISOString()
    const w = await mountList([
      { version: '3.0.0', status: 'held', cached: false, reasons: [{ source: 'min_age', detail: 'young', until }] },
      { version: '2.0.0', status: 'held', cached: true, reasons: [{ source: 'curation', detail: 'blocked by repository curation: malware' }] },
      available('1.1.0', { cached: true }),
      available('1.0.0'),
    ])
    const rows = w.findAll('tbody tr')
    expect(rows).toHaveLength(4)
    const badges = rows.map((r) => ['held', 'available', 'cached'].filter((b) => r.find(`[data-testid="badge-${b}"]`).exists()))
    expect(badges).toEqual([['held'], ['held', 'cached'], ['available', 'cached'], ['available']])
    expect(w.findAll('[data-testid="held-status"]').map((s) => s.text())).toEqual(['Available in 5h', 'Blocked: malware'])
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('shows the install command of the newest available version and lets a row pick another', async () => {
    const w = await mountList([{ version: '2.0.0', status: 'held', cached: false, reasons: [{ source: 'min_age', detail: 'young' }] }, available('1.1.0'), available('1.0.0')])
    expect(w.get('[data-testid="install-command"]').text()).toContain('lib@1.1.0')
    await w.get('[data-testid="action-select"][aria-label="Show install command: 1.0.0"]').trigger('click')
    expect(w.get('[data-testid="install-command"]').text()).toContain('lib@1.0.0')
    // A held version cannot be installed: no command to show or copy for it.
    const held = w.get('tbody tr')
    expect(held.get('[data-testid="action-select"]').attributes('disabled')).toBeDefined()
    expect(held.find('[aria-label^="Copy install command"]').exists()).toBe(false)
    expect(w.find('[aria-label="Copy install command: 1.1.0"]').exists()).toBe(true)
  })

  it('has no install command when every version is held', async () => {
    const w = await mountList([{ version: '2.0.0', status: 'held', cached: false, reasons: [{ source: 'min_age', detail: 'young' }] }])
    expect(w.find('[data-testid="artifact-install"]').exists()).toBe(false)
    expect(rowsOf(w)).toEqual(['2.0.0'])
  })

  it('pages a long list client-side, newest first, and filters it', async () => {
    const versions = Array.from({ length: 60 }, (_, i) => available(`1.${60 - i}.0`, { published_at: new Date(Date.UTC(2026, 0, 1) + (60 - i) * 86_400_000).toISOString() }))
    const w = await mountList(versions)
    expect(rowsOf(w)).toHaveLength(25)
    expect(rowsOf(w)[0]).toBe('1.60.0')
    await w.findAll('button').find((b) => b.text() === 'Next')!.trigger('click')
    await flushPromises()
    expect(rowsOf(w)[0]).toBe('1.35.0')
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('filters by version text from the URL', async () => {
    const w = await mountList([available('1.70.0'), available('1.7.0'), available('1.6.0')], 'npm', '/?q=1.7')
    expect(rowsOf(w)).toEqual(['1.70.0', '1.7.0'])
  })

  it('builds the snippet of the format, from the repository URL', async () => {
    const w = await mountList([available('2.31.0', { cached: true })], 'pypi')
    expect(w.get('[data-testid="install-command"]').text()).toContain('pip install lib==2.31.0')
    expect(w.get('[data-testid="install-command"]').text()).toContain('/repos/px/simple/')
  })
})

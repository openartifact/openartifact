// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import VexFeedDialog from '../VexFeedDialog.vue'
import VexFeedsPage from '../VexFeedsPage.vue'
import type { VexFeed } from '../../../../api/vexFeeds'
import { useToastStore } from '../../../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const FEED: VexFeed = {
  id: 'f1',
  name: 'acme',
  url: 'https://acme.example/.well-known/csaf/provider-metadata.json',
  enabled: true,
  require_signature: true,
  pgp_keys: '',
  interval_hours: 24,
  cursor_at: null,
  last_attempt_at: '2026-11-08T10:05:00Z',
  last_success_at: '2026-11-08T10:00:00Z',
  last_error: 'signature check failed for 2 documents',
  documents_total: 12,
  rejected_total: 2,
  statements: 40,
  applied: 7,
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
}

let wrapper: VueWrapper | undefined
let fetchSpy: Mock<typeof fetch>

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  fetchSpy?.mockRestore()
})

const q = <T extends HTMLElement>(testId: string): T => document.body.querySelector<T>(`[data-testid="${testId}"]`)!

async function type(testId: string, value: string): Promise<void> {
  const el = q<HTMLInputElement>(testId)
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
}

async function submit(): Promise<void> {
  q('vex-feed-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await flushPromises()
}

const requests = (method: string): { url: string; body: unknown }[] =>
  fetchSpy.mock.calls
    .filter(([, init]) => init?.method === method)
    .map(([url, init]) => ({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined }))

describe('VexFeedDialog', () => {
  async function mountDialog(feed: VexFeed | null): Promise<void> {
    fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (_url, init) => json({ ...FEED, ...JSON.parse(String(init?.body ?? '{}')) }, 201))
    wrapper = mount(VexFeedDialog, { props: { open: true, feed }, attachTo: document.body })
    await flushPromises()
  }

  it('requires the name and the URL before sending anything', async () => {
    await mountDialog(null)
    await submit()
    expect(requests('POST')).toEqual([])
    expect(document.body.querySelector('#vex-feed-name-error')?.textContent).toContain('Enter a name')
    expect(document.body.querySelector('#vex-feed-url-error')?.textContent).toContain('Enter the feed URL')
  })

  it('keeps the interval between 1 and 720 hours', async () => {
    await mountDialog(null)
    await type('vex-feed-name', 'acme')
    await type('vex-feed-url', FEED.url)
    for (const hours of ['0', '721']) {
      await type('vex-feed-interval', hours)
      await submit()
      expect(document.body.querySelector('#vex-feed-interval-error')?.textContent).toContain('between 1 and 720')
    }
    expect(requests('POST')).toEqual([])

    await type('vex-feed-interval', '720')
    await submit()
    expect(requests('POST')).toEqual([
      { url: '/api/v1/vex/feeds', body: { name: 'acme', url: FEED.url, enabled: true, require_signature: true, pgp_keys: '', interval_hours: 720 } },
    ])
    expect(wrapper!.emitted('saved')).toHaveLength(1)
  })

  it('edits a feed with PUT, prefilled', async () => {
    await mountDialog(FEED)
    expect(q<HTMLInputElement>('vex-feed-name').value).toBe('acme')
    await type('vex-feed-interval', '6')
    await submit()
    expect(requests('PUT')).toEqual([
      { url: '/api/v1/vex/feeds/f1', body: { name: 'acme', url: FEED.url, enabled: true, require_signature: true, pgp_keys: '', interval_hours: 6 } },
    ])
  })
})

describe('VexFeedsPage', () => {
  async function mountPage(sync: Response): Promise<void> {
    fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (url, init) =>
      init?.method === 'POST' && String(url).endsWith('/sync') ? sync.clone() : json({ feeds: [FEED] }),
    )
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
    wrapper = mount(VexFeedsPage, { attachTo: document.body, global: { plugins: [router] } })
    await flushPromises()
  }

  it('lists a feed with its sync state and counters', async () => {
    await mountPage(json({ status: 'started' }, 202))
    const row = wrapper!.get('[data-testid="vex-feed-row"]')
    expect(row.text()).toContain('acme')
    expect(row.text()).toContain('Failing')
    expect(row.text()).toContain('signature check failed for 2 documents')
    expect(row.text()).toContain('12')
    expect(row.text()).toContain('2 rejected')
    expect(row.text()).toContain('40')
    expect(row.text()).toContain('7 applied')
    expect(row.text()).toContain('Every 24 h')
  })

  it('starts a sync and says so', async () => {
    await mountPage(json({ status: 'started' }, 202))
    await wrapper!.get('[data-testid="action-sync"]').trigger('click')
    await flushPromises()
    expect(requests('POST').map((r) => r.url)).toEqual(['/api/v1/vex/feeds/f1/sync'])
    expect(useToastStore().queue.map((t) => [t.level, t.title])).toEqual([['success', 'Sync of acme started']])
  })

  it('reports a sync that is already running', async () => {
    await mountPage(json({ error: 'a sync of this feed is already running', code: 'conflict' }, 409))
    await wrapper!.get('[data-testid="action-sync"]').trigger('click')
    await flushPromises()
    expect(useToastStore().queue.map((t) => [t.level, t.title])).toEqual([['info', 'A sync of acme is already running']])
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createRouter, createMemoryHistory, type Router } from 'vue-router'
import { setActivePinia, createPinia } from 'pinia'
import CommandPalette from '../CommandPalette.vue'
import { useUIStore } from '../../stores/ui'
import { ApiError } from '../../api/client'
import * as searchApi from '../../api/search'
import type { SearchPackageHit, SearchResult } from '../../api/search'

// The real Headless UI dialog is mounted; the API module is stubbed with
// spyOn(), which test/isolation.ts reverts after every test. Nothing sleeps:
// the 200 ms debounce is awaited by polling for the outcome with a deadline.

const DEADLINE_MS = 3000

async function waitFor(cond: () => boolean, what: string): Promise<void> {
  const end = Date.now() + DEADLINE_MS
  while (!cond()) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}; body: ${document.body.textContent}`)
    await new Promise((r) => setTimeout(r, 5))
    await flushPromises()
  }
}

const hit = (name: string, version: string, repository = 'npmjs'): SearchPackageHit => ({
  id: `${repository}/${name}/${version}`,
  name,
  version,
  format: 'npm',
  description: '',
  repository,
})

const found = (packages: SearchPackageHit[]): SearchResult => ({ packages, repositories: [] })

const text = (): string => document.body.textContent ?? ''
const liveRegion = (): string => document.body.querySelector('[aria-live="polite"]')?.textContent ?? ''

describe('CommandPalette search', () => {
  let search: Mock<typeof searchApi.search>
  let router: Router
  let wrapper: VueWrapper

  beforeEach(async () => {
    setActivePinia(createPinia())
    search = spyOn(searchApi, 'search').mockResolvedValue(found([]))
    router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/:rest(.*)*', component: { template: '<div/>' } }],
    })
    await router.push('/')
    useUIStore().openCommandPalette()
    wrapper = mount(CommandPalette, { global: { plugins: [router] }, attachTo: document.body })
    await flushPromises()
  })

  afterEach(() => wrapper.unmount())

  function input(): HTMLInputElement {
    return document.body.querySelector('input') as HTMLInputElement
  }

  async function type(value: string): Promise<void> {
    input().value = value
    input().dispatchEvent(new Event('input', { bubbles: true }))
    input().dispatchEvent(new Event('change', { bubbles: true }))
    await flushPromises()
  }

  async function press(key: string): Promise<void> {
    input().dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
    await flushPromises()
  }

  const retryButton = (): HTMLButtonElement | undefined =>
    [...document.body.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Retry')

  it('shows a refine hint, not an error or an empty state, for a too broad query', async () => {
    search.mockRejectedValue(new ApiError('too many', 400, 'query_too_broad'))
    await type('a')
    await waitFor(() => text().includes('Refine your query'), 'the refine hint')

    expect(text()).not.toContain('Search failed')
    expect(text()).not.toContain('No matches')
    expect(retryButton()).toBeUndefined()
    expect(liveRegion()).toContain('Refine your query')
  })

  it('shows an inline error with a working retry for a real failure', async () => {
    search.mockRejectedValueOnce(new ApiError('boom', 500, 'internal'))
    await type('lodash')
    await waitFor(() => text().includes('Search failed.'), 'the error')

    expect(text()).not.toContain('No matches')
    expect(text()).not.toContain('Refine your query')
    expect(document.body.querySelector('[role="alert"]')).not.toBeNull()

    search.mockResolvedValue(found([hit('lodash', '4.17.21')]))
    retryButton()!.click()
    await waitFor(() => text().includes('4.17.21'), 'results after retry')

    expect(search).toHaveBeenCalledTimes(2)
    expect(text()).not.toContain('Search failed.')
    expect(document.body.querySelector('[role="alert"]')).toBeNull()
  })

  it('treats a network failure as an error too', async () => {
    search.mockRejectedValue(new TypeError('Failed to fetch'))
    await type('lodash')
    await waitFor(() => text().includes('Search failed.'), 'the error')
    expect(retryButton()).toBeDefined()
  })

  it('says "No matches" only when the search really found nothing', async () => {
    const pending = Promise.withResolvers<SearchResult>()
    search.mockReturnValue(pending.promise)
    await type('zzz')
    await waitFor(() => search.mock.calls.length === 1, 'the search request')

    expect(text()).toContain('Searching...')
    expect(text()).not.toContain('No matches')
    expect(liveRegion()).toBe('Searching...')

    pending.resolve(found([]))
    await waitFor(() => text().includes('No matches'), 'the empty state')
    expect(text()).not.toContain('Searching...')
  })

  it('asks the server for one row per package', async () => {
    search.mockResolvedValue(found([hit('lodash', '4.17.21')]))
    await type('lodash')
    await waitFor(() => text().includes('4.17.21'), 'results')

    expect(search.mock.calls[0]![0]).toMatchObject({ q: 'lodash', group: 'package', limit: 8 })
    expect(search.mock.calls[0]![0].signal).toBeInstanceOf(AbortSignal)
  })

  it('lists each version of a package on a row of its own', async () => {
    search.mockResolvedValue(found([hit('lodash', '4.17.21', 'npmjs'), hit('lodash', '4.17.20', 'npmjs'), hit('lodash', '4.17.21', 'mirror')]))
    await type('lodash')
    await waitFor(() => text().includes('4.17.20'), 'results')

    expect(document.body.querySelectorAll('[role="option"]').length).toBe(3 + 1) // + "view all"
  })

  it('aborts the previous request and drops its late response', async () => {
    const first = Promise.withResolvers<SearchResult>()
    search.mockReturnValueOnce(first.promise)
    await type('lo')
    await waitFor(() => search.mock.calls.length === 1, 'the first request')
    const firstSignal = search.mock.calls[0]![0].signal!

    search.mockResolvedValue(found([hit('lodash', '4.17.21')]))
    await type('lodash')
    await waitFor(() => text().includes('4.17.21'), 'the second result')
    expect(firstSignal.aborted).toBe(true)

    // The aborted request finishes late with a different answer: ignored.
    first.resolve(found([hit('stale-package', '0.0.1')]))
    await flushPromises()
    expect(text()).not.toContain('stale-package')
    expect(text()).toContain('lodash')
  })

  it('ignores the failure of an aborted request', async () => {
    const first = Promise.withResolvers<SearchResult>()
    search.mockReturnValueOnce(first.promise)
    await type('lo')
    await waitFor(() => search.mock.calls.length === 1, 'the first request')

    search.mockResolvedValue(found([hit('lodash', '4.17.21')]))
    await type('lodash')
    await waitFor(() => text().includes('4.17.21'), 'the second result')

    first.reject(new DOMException('aborted', 'AbortError'))
    await flushPromises()
    expect(text()).not.toContain('Search failed.')
    expect(text()).toContain('4.17.21')
  })

  it('aborts the request in flight when unmounted', async () => {
    search.mockReturnValue(Promise.withResolvers<SearchResult>().promise)
    await type('lodash')
    await waitFor(() => search.mock.calls.length === 1, 'the request')
    const signal = search.mock.calls[0]![0].signal!

    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    // afterEach unmounts again; remount a spare so that stays valid.
    wrapper = mount(CommandPalette, { global: { plugins: [router] }, attachTo: document.body })
  })

  it('does not search for a blank query', async () => {
    await type('   ')
    await flushPromises()
    expect(search).not.toHaveBeenCalled()
  })

  it('announces the number of results in a polite live region', async () => {
    search.mockResolvedValue(found([hit('lodash', '4.17.21'), hit('lodash-es', '4.17.21')]))
    await type('lodash')
    await waitFor(() => /results$/.test(liveRegion()), 'the count')

    expect(liveRegion()).toBe('2 results')
    expect(document.body.querySelector('[aria-live="polite"]')!.getAttribute('role')).toBe('status')
  })

  it('opens the first result with Enter', async () => {
    search.mockResolvedValue(found([hit('lodash', '4.17.21', 'npmjs')]))
    await type('lodash')
    await waitFor(() => text().includes('4.17.21'), 'results')

    await press('Enter')
    await waitFor(() => !useUIStore().commandPaletteOpen, 'the palette to close')
    await waitFor(() => router.currentRoute.value.path !== '/', 'navigation')

    expect(router.currentRoute.value.fullPath).toBe('/repos/npmjs/packages/npm/lodash')
  })

  it('moves the selection with the arrow keys down to "view all results"', async () => {
    search.mockResolvedValue(found([hit('lodash', '4.17.21', 'npmjs')]))
    await type('lodash')
    await waitFor(() => text().includes('4.17.21'), 'results')

    expect(text()).toContain('View all results for "lodash"')
    await press('ArrowDown')
    await press('Enter')
    await waitFor(() => router.currentRoute.value.path === '/search', 'navigation')

    expect(router.currentRoute.value.query.q).toBe('lodash')
  })
})

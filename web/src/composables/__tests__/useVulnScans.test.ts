// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { defineComponent, h } from 'vue'
import { useVulnScans } from '../useVulnScans'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

const row = (id: string, status = 'completed') => ({
  id,
  artifact_id: `art-${id}`,
  repository_name: 'main',
  repository_format: 'npm',
  package_name: `pkg-${id}`,
  package_version: '1.0.0',
  scan_status: status,
  scanner: 'osv',
  quarantined: false,
})

interface ListCall {
  search: URLSearchParams
  respond: (body: unknown) => void
}

/** Every scan-list request waits until the test answers it; the summary answers at once. */
function mockApi() {
  const lists: ListCall[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: string) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname === '/api/v1/vuln/scans/summary') {
      return json({ total: 0, completed: 0, partial: 0, failed: 0, scanning: 0, quarantined: 0, severity_at_least: {} })
    }
    if (url.pathname === '/api/v1/vuln/scans') {
      return new Promise<Response>((resolve) => {
        lists.push({ search: url.searchParams, respond: (body) => resolve(json(body)) })
      })
    }
    return json({})
  }) as never)
  return lists
}

async function setup(path: string) {
  const router: Router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }],
  })
  await router.push(path)
  await router.isReady()
  let state!: ReturnType<typeof useVulnScans>
  const Host = defineComponent({
    setup() {
      state = useVulnScans()
      return () => h('div')
    },
  })
  mount(Host, { global: { plugins: [createPinia(), router] } })
  await flushPromises()
  return { state, router }
}

describe('useVulnScans', () => {
  it('ignores a stale list response after the filters changed', async () => {
    const lists = mockApi()
    const { state } = await setup('/security/vulns')
    expect(lists).toHaveLength(1)

    state.setFilter({ severity: 'high' })
    await flushPromises()
    expect(lists).toHaveLength(2)
    expect(lists[1]!.search.get('severity')).toBe('high')

    lists[1]!.respond({ scans: [row('fresh')], has_more: false })
    await flushPromises()
    lists[0]!.respond({ scans: [row('stale')], has_more: false })
    await flushPromises()

    expect(state.scans.value.map((s) => s.id)).toEqual(['fresh'])
    expect(state.loading.value).toBe(false)
  })

  it('starts over with the first page when a filter changes', async () => {
    const lists = mockApi()
    const { state } = await setup('/security/vulns')
    lists[0]!.respond({ scans: [row('a')], has_more: true, next_cursor: 'cursor-1' })
    await flushPromises()
    expect(state.hasMore.value).toBe(true)

    void state.loadMore()
    await flushPromises()
    expect(lists).toHaveLength(2)
    expect(lists[1]!.search.get('cursor')).toBe('cursor-1')
    lists[1]!.respond({ scans: [row('a'), row('b')], has_more: false })
    await flushPromises()
    // Rows already shown are not duplicated.
    expect(state.scans.value.map((s) => s.id)).toEqual(['a', 'b'])

    state.setFilter({ status: 'failed' })
    await flushPromises()
    expect(lists).toHaveLength(3)
    expect(lists[2]!.search.get('cursor')).toBeNull()
    expect(lists[2]!.search.get('status')).toBe('failed')
    expect(state.scans.value).toEqual([])
    expect(state.hasMore.value).toBe(false)
  })

  it('discards a load-more response that arrives after the filters changed', async () => {
    const lists = mockApi()
    const { state } = await setup('/security/vulns')
    lists[0]!.respond({ scans: [row('a')], has_more: true, next_cursor: 'cursor-1' })
    await flushPromises()

    void state.loadMore()
    await flushPromises()
    state.setFilter({ severity: 'critical' })
    await flushPromises()
    lists[2]!.respond({ scans: [row('c')], has_more: false })
    await flushPromises()
    lists[1]!.respond({ scans: [row('late')], has_more: true, next_cursor: 'cursor-2' })
    await flushPromises()

    expect(state.scans.value.map((s) => s.id)).toEqual(['c'])
    expect(state.hasMore.value).toBe(false)
    expect(state.loadingMore.value).toBe(false)
  })

  it('reports the filters it reads from the URL and clears them', async () => {
    const lists = mockApi()
    const { state, router } = await setup('/security/vulns?repo=main&quarantined=true')
    expect(state.filters.value.repo).toBe('main')
    expect(state.hasFilters.value).toBe(true)
    lists[0]!.respond({ scans: [], has_more: false })

    state.clearFilters()
    await flushPromises()
    expect(state.hasFilters.value).toBe(false)
    expect(router.currentRoute.value.query).toEqual({})
  })
})

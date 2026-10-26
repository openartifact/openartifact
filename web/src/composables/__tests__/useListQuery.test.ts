// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { useListQuery, type ListQuery, type ListRequest, type ListResponse } from '../useListQuery'
import { enumParam, strParam } from '../useUrlState'

const filters = { q: strParam(), severity: enumParam(['all', 'high', 'low'] as const, 'all') }
type Row = { id: string }
type Req = ListRequest<typeof filters>

interface Pending {
  req: Req
  resolve: (res: ListResponse<Row>) => void
  reject: (err: Error) => void
}

let router: Router
let calls: Pending[]

function fetcher(req: Req): Promise<ListResponse<Row>> {
  return new Promise((resolve, reject) => calls.push({ req, resolve, reject }))
}

async function setup(path: string, opts: Partial<Parameters<typeof useListQuery<typeof filters, Row>>[0]> = {}) {
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
  await router.push(path)
  let list!: ListQuery<typeof filters, Row>
  mount(
    { setup: () => { list = useListQuery<typeof filters, Row>({ filters, fetch: fetcher, debounce: ['q'], debounceMs: 20, limit: 2, ...opts }); return () => null } },
    { global: { plugins: [router] } },
  )
  return list
}

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))
const last = () => calls[calls.length - 1]

beforeEach(() => {
  calls = []
})

describe('useListQuery (page paging)', () => {
  it('fetches on mount with filters restored from the URL', async () => {
    const list = await setup('/?q=abc&severity=high&page=3')
    expect(calls).toHaveLength(1)
    expect(calls[0].req).toMatchObject({ filters: { q: 'abc', severity: 'high' }, page: 3, limit: 2 })
    expect(list.loading.value).toBe(true)
    calls[0].resolve({ rows: [{ id: 'a' }], total: 7 })
    await flushPromises()
    expect(list.rows.value).toEqual([{ id: 'a' }])
    expect(list.total.value).toBe(7)
    expect(list.loading.value).toBe(false)
    expect(list.filtered.value).toBe(true)
  })

  it('writes a filter change to the URL with replaceState and resets the page', async () => {
    const list = await setup('/?page=3')
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    const position = router.options.history.state.position
    list.setFilter('severity', 'low')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ severity: 'low' })
    expect(router.options.history.state.position).toBe(position)
    expect(last().req).toMatchObject({ page: 1, filters: { severity: 'low' } })
  })

  it('debounces text: the draft is live, the URL and the fetch follow once', async () => {
    const list = await setup('/')
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    list.setFilter('q', 'a')
    list.setFilter('q', 'ab')
    list.setFilter('q', 'abc')
    expect(list.text.q).toBe('abc')
    expect(router.currentRoute.value.query.q).toBeUndefined()
    expect(calls).toHaveLength(1)
    await wait(40)
    await flushPromises()
    expect(router.currentRoute.value.query.q).toBe('abc')
    expect(calls).toHaveLength(2)
    expect(last().req.filters.q).toBe('abc')
  })

  it('a select change carries pending text with it', async () => {
    const list = await setup('/')
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    list.setFilter('q', 'xy')
    list.setFilter('severity', 'high')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ q: 'xy', severity: 'high' })
    await wait(40)
    expect(calls).toHaveLength(2)
  })

  it('drops a stale response that arrives after a newer request', async () => {
    const list = await setup('/')
    list.setFilter('severity', 'high')
    await flushPromises()
    expect(calls).toHaveLength(2)
    calls[1].resolve({ rows: [{ id: 'new' }], total: 1 })
    await flushPromises()
    calls[0].resolve({ rows: [{ id: 'stale' }], total: 1 })
    await flushPromises()
    expect(list.rows.value).toEqual([{ id: 'new' }])
    expect(list.loading.value).toBe(false)
  })

  it('ignores a stale failure too', async () => {
    const list = await setup('/')
    list.setFilter('severity', 'high')
    await flushPromises()
    calls[1].resolve({ rows: [{ id: 'ok' }], total: 1 })
    await flushPromises()
    calls[0].reject(new Error('boom'))
    await flushPromises()
    expect(list.error.value).toBe('')
    expect(list.rows.value).toEqual([{ id: 'ok' }])
  })

  it('exposes a failure and clears it on reload', async () => {
    const list = await setup('/')
    calls[0].reject(new Error('boom'))
    await flushPromises()
    expect(list.error.value).toBe('boom')
    expect(list.rows.value).toEqual([])
    const reload = list.reload()
    calls[1].resolve({ rows: [{ id: 'a' }], total: 1 })
    await reload
    expect(list.error.value).toBe('')
  })

  it('does not refetch when an unrelated query key changes', async () => {
    await setup('/?tab=files')
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    await router.replace({ query: { tab: 'versions' } })
    await flushPromises()
    expect(calls).toHaveLength(1)
  })

  it('refetches with the restored state when the URL is navigated (back/forward, links)', async () => {
    const list = await setup('/')
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    await router.push('/?severity=high&page=2')
    await flushPromises()
    expect(last().req).toMatchObject({ page: 2, filters: { severity: 'high' } })
    expect(list.page.value).toBe(2)
  })

  it('clear() resets filters and the text draft', async () => {
    const list = await setup('/?q=abc&severity=high&page=2&tab=x')
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    list.clear()
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ tab: 'x' })
    expect(list.text.q).toBe('')
    expect(list.filtered.value).toBe(false)
  })

  it('syncs the text draft when the URL changes behind its back', async () => {
    const list = await setup('/?q=abc')
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    await router.push('/?q=zzz')
    await flushPromises()
    expect(list.text.q).toBe('zzz')
  })
})

describe('useListQuery (sorting)', () => {
  it('toggles direction on the same column and starts ascending on a new one', async () => {
    const list = await setup('/', { sorts: ['name', 'size'] })
    calls[0].resolve({ rows: [], total: 0 })
    await flushPromises()
    expect(list.sort.value).toBe('name')
    list.setSort('name')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ dir: 'desc' })
    list.setSort('size')
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ sort: 'size' })
    expect(last().req).toMatchObject({ sort: 'size', dir: 'asc', page: 1 })
  })
})

describe('useListQuery (cursor paging)', () => {
  it('loads more chunks with the cursor and appends', async () => {
    const list = await setup('/', { paging: 'cursor' })
    expect(calls[0].req.cursor).toBeUndefined()
    calls[0].resolve({ rows: [{ id: '1' }], hasMore: true, nextCursor: 'c1' })
    await flushPromises()
    expect(list.hasMore.value).toBe(true)
    const more = list.loadMore()
    expect(calls[1].req.cursor).toBe('c1')
    calls[1].resolve({ rows: [{ id: '2' }], hasMore: false })
    await more
    expect(list.rows.value.map((r) => r.id)).toEqual(['1', '2'])
    expect(list.hasMore.value).toBe(false)
    await list.loadMore()
    expect(calls).toHaveLength(2)
  })

  it('a filter change starts over without a cursor and keeps the URL free of cursors', async () => {
    const list = await setup('/', { paging: 'cursor' })
    calls[0].resolve({ rows: [{ id: '1' }], hasMore: true, nextCursor: 'c1' })
    await flushPromises()
    list.setFilter('severity', 'low')
    await flushPromises()
    expect(last().req.cursor).toBeUndefined()
    expect(router.currentRoute.value.query).toEqual({ severity: 'low' })
    last().resolve({ rows: [{ id: 'x' }], hasMore: false })
    await flushPromises()
    expect(list.rows.value).toEqual([{ id: 'x' }])
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, beforeEach, type Mock } from 'bun:test'
import { useSearch } from '../useSearch'
import * as searchApi from '../../api/search'

// The API module is stubbed with spyOn() on its export, which the global
// afterEach (test/isolation.ts) reverts via mock.restore(). mock.module() would
// replace api/search for every test file that runs afterwards.
let mockSearch: Mock<typeof searchApi.search>

describe('useSearch', () => {
  beforeEach(() => {
    mockSearch = spyOn(searchApi, 'search').mockResolvedValue({ repositories: [], packages: [] })
  })

  it('starts with empty query, no results, loading false, searched false', () => {
    const s = useSearch()
    expect(s.query.value).toBe('')
    expect(s.repoResults.value).toEqual([])
    expect(s.packageResults.value).toEqual([])
    expect(s.loading.value).toBe(false)
    expect(s.searched.value).toBe(false)
  })

  it('clear() resets all state', () => {
    const s = useSearch()
    s.query.value = 'react'
    s.searched.value = true
    s.clear()
    expect(s.query.value).toBe('')
    expect(s.repoResults.value).toEqual([])
    expect(s.packageResults.value).toEqual([])
    expect(s.searched.value).toBe(false)
  })

  it('execute() with empty query calls clear() and skips API', async () => {
    const s = useSearch()
    s.query.value = ''
    await s.execute()
    expect(s.searched.value).toBe(false)
    expect(mockSearch).not.toHaveBeenCalled()
  })

  it('execute() with whitespace-only query calls clear() and skips API', async () => {
    const s = useSearch()
    s.query.value = '   '
    await s.execute()
    expect(s.searched.value).toBe(false)
    expect(mockSearch).not.toHaveBeenCalled()
  })

  it('scheduleSearch fires execute() after delay', async () => {
    const s = useSearch()
    s.query.value = 'react'
    mockSearch.mockResolvedValue({
      repositories: [{ id: '1', name: 'repo', format: 'npm', type: 'hosted' }],
      packages: [],
    })
    s.scheduleSearch(0)
    await new Promise(r => setTimeout(r, 20))
    expect(mockSearch).toHaveBeenCalled()
    expect(s.repoResults.value).toHaveLength(1)
  })

  it('scheduleSearch does not fire before delay', () => {
    const s = useSearch()
    s.query.value = 'react'
    s.scheduleSearch(500)
    expect(mockSearch).not.toHaveBeenCalled()
    // Cancel the pending timer so it cannot fire into a later test or file.
    s.clear()
  })

  it('calling scheduleSearch again resets the debounce', async () => {
    const s = useSearch()
    mockSearch.mockResolvedValue({ repositories: [], packages: [] })
    s.query.value = 'rea'
    s.scheduleSearch(50)
    s.query.value = 'react'
    s.scheduleSearch(50)
    await new Promise(r => setTimeout(r, 70))
    expect(mockSearch).toHaveBeenCalledTimes(1)
    expect(mockSearch).toHaveBeenCalledWith({ q: 'react', limit: 20 })
  })

  it('execute() with non-empty query calls API and populates results', async () => {
    const s = useSearch()
    s.query.value = 'vue'
    mockSearch.mockResolvedValue({
      repositories: [{ id: '1', name: 'vue-repo', format: 'npm', type: 'hosted' }],
      packages: [{ id: '1', name: 'vue', version: '3.0.0', format: 'npm', description: '', repository: 'npmjs' }],
    })
    await s.execute()
    expect(mockSearch).toHaveBeenCalledWith({ q: 'vue', limit: 20 })
    expect(s.repoResults.value).toHaveLength(1)
    expect(s.packageResults.value).toHaveLength(1)
  })

  it('execute() uses trimmed query', async () => {
    const s = useSearch()
    s.query.value = '  react  '
    mockSearch.mockResolvedValue({ repositories: [], packages: [] })
    await s.execute()
    expect(mockSearch).toHaveBeenCalledWith({ q: 'react', limit: 20 })
  })

  it('loading is true during fetch, false after success', async () => {
    const s = useSearch()
    s.query.value = 'react'
    const { promise, resolve } = Promise.withResolvers<{ repositories: unknown[]; packages: unknown[] }>()
    mockSearch.mockReturnValue(promise)
    const execPromise = s.execute()
    expect(s.loading.value).toBe(true)
    resolve({ repositories: [], packages: [] })
    await execPromise
    expect(s.loading.value).toBe(false)
  })

  it('loading is false after API failure (execute catches internally via finally)', async () => {
    const s = useSearch()
    s.query.value = 'react'
    mockSearch.mockRejectedValue('Network error')
    await s.execute().catch(() => {})
    expect(s.loading.value).toBe(false)
  })

  it('API error leaves results empty', async () => {
    const s = useSearch()
    s.query.value = 'react'
    mockSearch.mockRejectedValue('search failed')
    await s.execute().catch(() => {})
    expect(s.repoResults.value).toEqual([])
    expect(s.packageResults.value).toEqual([])
  })

  it('searched is true after execute() is called even on error', async () => {
    const s = useSearch()
    s.query.value = 'react'
    mockSearch.mockRejectedValue('search failed')
    await s.execute().catch(() => {})
    expect(s.searched.value).toBe(true)
  })

  it('clear() invalidates an in-flight request so it cannot repopulate results', async () => {
    const s = useSearch()
    s.query.value = 'react'
    const { promise, resolve } = Promise.withResolvers<{ repositories: unknown[]; packages: unknown[] }>()
    mockSearch.mockReturnValue(promise)
    const execPromise = s.execute()
    s.clear()
    resolve({ repositories: [{ id: '1', name: 'late', format: 'npm' }], packages: [] })
    await execPromise
    expect(s.repoResults.value).toEqual([])
    expect(s.loading.value).toBe(false)
  })

  it('records an error message when the API fails', async () => {
    const s = useSearch()
    s.query.value = 'react'
    mockSearch.mockRejectedValue(new Error('boom'))
    await s.execute()
    expect(s.error.value).toBe('boom')
  })
})

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { getCurrentScope, onScopeDispose, ref, readonly } from 'vue'
import { ApiError } from '../api/client'
import { QUERY_TOO_BROAD, search } from '../api/search'
import type { SearchPackageHit, SearchRepoHit } from '../api/search'

/**
 * - `idle`: no query, nothing requested.
 * - `loading`: a query is waiting for its debounce or its response.
 * - `ready`: the last response arrived; empty lists mean nothing matched.
 * - `tooBroad`: the server refused the query as matching too much.
 * - `error`: the request failed; `retry()` repeats it.
 */
type PaletteSearchStatus = 'idle' | 'loading' | 'ready' | 'tooBroad' | 'error'

interface PaletteSearchOptions {
  debounceMs?: number
  /** Hits per group. */
  limit?: number
}

/**
 * Debounced search for the command palette. Each new query aborts the request
 * of the previous one, so a slow earlier response can never replace a newer
 * result, and disposing the owning scope aborts whatever is still pending.
 */
export function usePaletteSearch(options: PaletteSearchOptions = {}) {
  const debounceMs = options.debounceMs ?? 200
  const limit = options.limit ?? 8

  const status = ref<PaletteSearchStatus>('idle')
  const packages = ref<SearchPackageHit[]>([])
  const repositories = ref<SearchRepoHit[]>([])

  let query = ''
  let timer: ReturnType<typeof setTimeout> | undefined
  let controller: AbortController | undefined

  // Drops the pending debounce and aborts the request in flight.
  function cancel(): void {
    clearTimeout(timer)
    timer = undefined
    controller?.abort()
    controller = undefined
  }

  function clearResults(): void {
    packages.value = []
    repositories.value = []
  }

  async function run(): Promise<void> {
    timer = undefined
    const own = new AbortController()
    controller = own
    try {
      const res = await search({ q: query, limit, group: 'package', signal: own.signal })
      // A response that raced its own abort must not be applied.
      if (own.signal.aborted) return
      packages.value = res.packages
      repositories.value = res.repositories
      status.value = 'ready'
    } catch (err) {
      if (own.signal.aborted) return
      clearResults()
      status.value = err instanceof ApiError && err.code === QUERY_TOO_BROAD ? 'tooBroad' : 'error'
    }
  }

  /** Sets the text to search for; a blank text clears the results. */
  function setQuery(text: string): void {
    cancel()
    query = text.trim()
    if (!query) {
      clearResults()
      status.value = 'idle'
      return
    }
    status.value = 'loading'
    timer = setTimeout(() => void run(), debounceMs)
  }

  /** Repeats the current query at once, e.g. after an error. */
  function retry(): void {
    if (!query) return
    cancel()
    status.value = 'loading'
    void run()
  }

  function reset(): void {
    setQuery('')
  }

  if (getCurrentScope()) onScopeDispose(cancel)

  return {
    status: readonly(status),
    packages: readonly(packages),
    repositories: readonly(repositories),
    setQuery,
    retry,
    reset,
  }
}

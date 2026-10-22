// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { getCurrentScope, onScopeDispose, ref } from 'vue'
import { search } from '../api/search'
import type { SearchPackageHit, SearchRepoHit } from '../api/search'

export function useSearch() {
  const query = ref('')
  const repoResults = ref<SearchRepoHit[]>([])
  const packageResults = ref<SearchPackageHit[]>([])
  const loading = ref(false)
  const searched = ref(false)
  const error = ref('')

  let debounceTimer: ReturnType<typeof setTimeout> | undefined
  let requestId = 0

  function scheduleSearch(delayMs = 300): void {
    clearTimeout(debounceTimer)
    debounceTimer = setTimeout(() => execute(), delayMs)
  }

  async function execute(): Promise<void> {
    const q = query.value.trim()
    if (!q) {
      clear()
      return
    }
    const currentRequestId = ++requestId
    loading.value = true
    searched.value = true
    error.value = ''
    try {
      const res = await search({ q, limit: 20 })
      if (currentRequestId !== requestId) return
      repoResults.value = res.repositories || []
      packageResults.value = res.packages || []
    } catch (err) {
      if (currentRequestId !== requestId) return
      repoResults.value = []
      packageResults.value = []
      error.value = err instanceof Error ? err.message : 'Search failed'
    } finally {
      if (currentRequestId === requestId) loading.value = false
    }
  }

  function clear(): void {
    // Invalidate any in-flight request so it cannot repopulate the results.
    requestId++
    loading.value = false
    error.value = ''
    query.value = ''
    repoResults.value = []
    packageResults.value = []
    searched.value = false
    clearTimeout(debounceTimer)
    debounceTimer = undefined
  }

  // A page that unmounts mid-typing must not fire the pending search or apply
  // an in-flight response afterwards.
  if (getCurrentScope()) {
    onScopeDispose(() => {
      requestId++
      clearTimeout(debounceTimer)
    })
  }

  return { query, repoResults, packageResults, loading, searched, error, scheduleSearch, execute, clear }
}

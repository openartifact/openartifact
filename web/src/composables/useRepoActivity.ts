// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { listRepoActivity, type RepoActivityEntry } from '../api/repositories'
import type { RepoActivityCategory } from '../lib/repoActivity'

export const REPO_ACTIVITY_PAGE_SIZE = 25

/**
 * The timeline of one repository: the first page of the chosen event type, then further pages
 * appended by cursor. Only the latest request for the current repository and filter may write
 * state, so a slow response for a previous filter or page never overwrites newer data.
 */
export function useRepoActivity(repo: Ref<string>) {
  const entries = ref<RepoActivityEntry[]>([])
  const nextCursor = ref<string | null>(null)
  const category = ref<RepoActivityCategory | null>(null)
  const loading = ref(true)
  const loadingMore = ref(false)
  /** The first page failed (nothing to show). */
  const failed = ref(false)
  /** A further page failed (the loaded entries stay). */
  const moreFailed = ref(false)

  let generation = 0
  onScopeDispose(() => generation++)

  async function fetchPage(cursor: string | null): Promise<void> {
    const mine = generation
    try {
      const page = await listRepoActivity(repo.value, {
        limit: REPO_ACTIVITY_PAGE_SIZE,
        ...(cursor ? { cursor } : {}),
        ...(category.value ? { category: category.value } : {}),
      })
      if (mine !== generation) return
      entries.value = cursor ? [...entries.value, ...page.items] : page.items
      nextCursor.value = page.hasMore && page.nextCursor ? page.nextCursor : null
    } catch {
      if (mine !== generation) return
      if (cursor) moreFailed.value = true
      else failed.value = true
    } finally {
      if (mine === generation) {
        loading.value = false
        loadingMore.value = false
      }
    }
  }

  async function reload(): Promise<void> {
    generation++
    loading.value = true
    loadingMore.value = false
    failed.value = false
    moreFailed.value = false
    entries.value = []
    nextCursor.value = null
    await fetchPage(null)
  }

  async function loadMore(): Promise<void> {
    if (!nextCursor.value || loadingMore.value || loading.value) return
    loadingMore.value = true
    moreFailed.value = false
    await fetchPage(nextCursor.value)
  }

  function setCategory(next: RepoActivityCategory | null): void {
    if (next === category.value) return
    category.value = next
    void reload()
  }

  watch(
    repo,
    () => {
      category.value = null
      void reload()
    },
    { immediate: true },
  )

  return { entries, nextCursor, category, loading, loadingMore, failed, moreFailed, reload, loadMore, setCategory }
}

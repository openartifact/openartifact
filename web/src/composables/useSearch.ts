// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed } from 'vue'
import { searchChecksum, searchPackageGroups } from '../api/search'
import { effectiveMode } from '../lib/checksum'
import {
  checksumRow,
  groupRow,
  isAutoChecksum,
  planSearch,
  type SearchRow,
  type SearchFilters,
} from '../lib/packageSearch'
import { useListQuery, type ListRequest, type ListResponse } from './useListQuery'
import { boolParam, enumParam, strParam } from './useUrlState'

const FILTERS = {
  q: strParam(),
  format: strParam(),
  repo: strParam(),
  flagged: boolParam(),
  mode: enumParam(['', 'name', 'checksum'] as const, ''),
}

const PAGE_SIZE = 20

async function fetchRows(req: ListRequest<typeof FILTERS>): Promise<ListResponse<SearchRow>> {
  const f: SearchFilters = req.filters
  const plan = planSearch(f)
  const scope = { format: f.format || undefined, repo: f.repo || undefined, flagged: f.flagged }
  if (plan.kind === 'name') {
    const res = await searchPackageGroups({ q: plan.q, ...scope, limit: req.limit, page: req.page })
    return { rows: res.packages.map(groupRow), total: res.total }
  }
  if (plan.kind === 'checksum') {
    const res = await searchChecksum({ checksum: plan.checksum, ...scope })
    return { rows: res.packages.map(checksumRow), total: res.total }
  }
  return { rows: [], total: 0 }
}

/**
 * Package search bound to the URL (`?q=&format=&repo=&flagged=1&mode=checksum&page=`):
 * the query is debounced, a digest-shaped query switches to the checksum lookup
 * unless the toggle says otherwise, and nothing is requested for an empty query
 * or a checksum search whose text is not a full-length hex digest.
 */
export function useSearch() {
  const list = useListQuery({
    filters: FILTERS,
    fetch: fetchRows,
    paging: 'page',
    limit: PAGE_SIZE,
    debounce: ['q'],
    debounceMs: 250,
  })

  const plan = computed(() => planSearch(list.filters.value))
  const mode = computed(() => effectiveMode(list.filters.value.mode, list.filters.value.q.trim()))
  const autoChecksum = computed(() => isAutoChecksum(list.filters.value))
  /** A query (or filters) is present, so an empty result is "no match" rather than the start screen. */
  const active = computed(() => plan.value.kind !== 'idle')

  function setMode(next: '' | 'name' | 'checksum'): void {
    list.setFilter('mode', next)
  }

  return { list, plan, mode, autoChecksum, active, setMode }
}
